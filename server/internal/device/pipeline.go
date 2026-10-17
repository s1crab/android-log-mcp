package device

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/s1crab/android-log-mcp/server/internal/analysis"
	"github.com/s1crab/android-log-mcp/server/internal/logcat"
	"github.com/s1crab/android-log-mcp/server/internal/model"
	"github.com/s1crab/android-log-mcp/server/internal/procs"
	"github.com/s1crab/android-log-mcp/server/internal/store"
)

// Pipeline is the adb-independent ingest path of one boot session:
// record → process tracking → (short delay) → annotate → store → crash
// detection. The live recorder feeds it from `adb logcat`; tests replay
// captured logcat/dropbox/ps output through exactly the same code.
type Pipeline struct {
	Store *store.Store
	Procs *procs.Tracker
	Det   *analysis.Detector
	Boot  string
	Loc   *time.Location
	// Delay lets lifecycle lines that arrive slightly late (events vs main
	// buffer) name a new pid before its first lines are stored.
	Delay time.Duration

	OnUnknownPID func(newestNs int64) // stored lines had an unknown pid; the newest one's time
	NeedDropbox  func(tag string)     // a crash wants DropBox evidence
	OnUnknownUID func(uid int)        // a device process has an app uid missing from the package list
	BeforeAppend func(first *model.Record)
	// DeviceNow returns device now in ns (0 = unknown); it sets the
	// DropBox import cutoff (issue #53). Nil: the newest stored line.
	DeviceNow func() int64

	// Clock jump detection (issue #48). Live: lines arrive as they are
	// logged (catch-up is done), so their host arrival time is meaningful.
	Live         bool
	Clock        *LiveClock // live lines set the clock offset (nil: not tracked)
	OnClockEvent func()     // a clock jump or an auto_time change was seen
	jump         jumpState

	accepted uint64
	// seqMiss counts records whose predicted seq (stamped in Accept) was not
	// the one the store assigned; they are attributed by device time.
	seqMiss uint64

	queue   []pending
	crashMu sync.Mutex
}

// jumpState follows device time in arrival order for clock jumps.
type jumpState struct {
	prev, max int64 // device time of the previous line; highest since the last back jump
	set       bool  // the previous line announced a clock set
}

// possibleJumpNs: in lines without a usable arrival time (backfill), a step
// of the device time this large between consecutive lines is reported as
// a possible forward jump.
const possibleJumpNs = int64(time.Hour)

// detectJumps checks stored lines (in arrival order) for device clock
// jumps (issue #48). Live lines go to the LiveClock (two-line rule against
// the clock offset). Replayed lines never touch the offset: a set is found
// from the AlarmManagerService "Setting time of day to sec=N" line (exact,
// at the next line), a step back of more than the threshold between
// consecutive lines, or a step forward of more than possibleJumpNs
// (a forward jump or a quiet period).
// It returns the jumps found, in arrival order.
func (p *Pipeline) detectJumps(batch []*model.Record, arrived []time.Time, live, late []bool) []store.ClockJump {
	th := store.ClockJumpThreshold
	event := false
	var found []store.ClockJump
	for i, r := range batch {
		if late[i] {
			continue
		}
		js := &p.jump
		announced := js.set
		_, js.set = clockSetLine(r)
		if r.Tag == "SettingsProvider" && strings.Contains(r.Msg, "settings/global/auto_time") {
			event = true
		}
		var j *store.ClockJump
		switch {
		case live[i] && p.Clock != nil:
			j = p.Clock.Line(r.Seq, r.TimeNs, js.prev, arrived[i], announced)
		case js.prev == 0:
		case announced && (r.TimeNs-js.prev > th || js.prev-r.TimeNs > th):
			j = &store.ClockJump{Seq: r.Seq, DeltaNs: r.TimeNs - js.prev, BeforeNs: js.prev, AfterNs: r.TimeNs, Source: "set"}
		case r.TimeNs < js.max-th:
			// far below the lines before it: the clock was set back
			// (buffers interleave by seconds, a backlog keeps the order)
			j = &store.ClockJump{Seq: r.Seq, DeltaNs: r.TimeNs - js.prev, BeforeNs: js.prev, AfterNs: r.TimeNs, Source: "lines"}
		case r.TimeNs > js.prev+possibleJumpNs:
			j = &store.ClockJump{Seq: r.Seq, DeltaNs: r.TimeNs - js.prev, BeforeNs: js.prev, AfterNs: r.TimeNs, Source: "possible"}
		}
		if j != nil {
			event = true
			found = append(found, *j)
			if j.DeltaNs < 0 {
				js.max = r.TimeNs
			}
			if p.Store.IsWriter() {
				p.Store.AddClockJump(*j)
			}
		}
		js.prev = r.TimeNs
		js.max = max(js.max, r.TimeNs)
	}
	if event && p.OnClockEvent != nil {
		p.OnClockEvent()
	}
	return found
}

// clockSetLine: the framework logs "Setting time of day to sec=N" right
// before it changes the device clock (main buffer; tag AlarmManagerService
// on API 21–33, SystemClockTime on API 34+; a trailing newline on old
// versions), and "Current time only <ms>, advancing to build time <ms>"
// at boot (AlarmManager / SystemClockTime). An optional anchor only:
// `adb shell date` as root sets the clock without logging anything, so
// jumps are detected from the lines' device times (issue #48).
func clockSetLine(r *model.Record) (int64, bool) {
	switch r.Tag {
	case "AlarmManagerService", "SystemClockTime", "AlarmManager":
	default:
		return 0, false
	}
	if m := reClockSet.FindStringSubmatch(r.Msg); m != nil && r.Tag != "AlarmManager" {
		n, err := strconv.ParseInt(m[1], 10, 64)
		return n * 1e9, err == nil
	}
	if m := reClockBoot.FindStringSubmatch(r.Msg); m != nil && r.Tag != "AlarmManagerService" {
		n, err := strconv.ParseInt(m[1], 10, 64)
		return n * 1e6, err == nil
	}
	return 0, false
}

var (
	reClockSet  = regexp.MustCompile(`^Setting time of day to sec=(-?\d+)\s*$`)
	reClockBoot = regexp.MustCompile(`^Current time only -?\d+, advancing to build time (\d+)`)
)

type pending struct {
	r       *model.Record
	arrived time.Time
	live    bool // accepted while Live
	late    bool // recovered from the device buffer after later lines (AcceptLate)
}

// NewPipeline prepares ingestion for boot (short boot id) into st.
func NewPipeline(st *store.Store, boot string, loc *time.Location) *Pipeline {
	if loc == nil {
		loc = time.Local
	}
	p := &Pipeline{Store: st, Procs: procs.New(), Det: analysis.NewDetector(), Boot: boot, Loc: loc, Delay: 800 * time.Millisecond}
	p.Procs.Reset(boot)
	var ps []procs.Proc
	for _, x := range LoadProcs(st) {
		if x.Boot == boot {
			ps = append(ps, x)
		}
	}
	p.Procs.Restore(ps)
	for _, j := range st.Meta().ClockJumps {
		if j.Source != "possible" {
			p.Procs.ClockJump(j.Seq)
		}
	}
	if m := st.Meta(); len(m.Sessions) > 0 && m.Sessions[len(m.Sessions)-1].Boot == boot {
		if n := st.Stats().NewestNs; n != 0 {
			p.jump.prev, p.jump.max = n, n // continue the session's timeline
		}
	}
	// before SetPackages: entries it re-attributes are persisted (issue #41)
	p.Procs.OnChange = func(x procs.Proc) { appendProc(st, x) }
	if pk := LoadPackages(st); len(pk) > 0 {
		p.Procs.SetPackages(pk)
	}
	p.Procs.OnUnknownUID = func(uid int) {
		if f := p.OnUnknownUID; f != nil {
			f(uid)
		}
	}
	p.Det.Emit = p.EmitCrash
	p.Det.PairInStore = true
	p.Det.Lifetime = func(pid int, devNs int64, seq uint64) string {
		if x := p.resolveAnchor(pid, devNs, seq); x != nil {
			return x.ID
		}
		return ""
	}
	p.Det.PackageOf = p.Procs.OwnerOf // dotted process names (issue #41)
	p.Det.NeedDropbox = func(tag string) {
		if p.NeedDropbox != nil {
			p.NeedDropbox(tag)
		}
	}
	return p
}

// Accept takes one assembled record (already deduplicated by the caller).
func (p *Pipeline) Accept(r *model.Record, arrived time.Time) {
	if r == nil {
		return
	}
	r.Boot = p.Boot
	p.stampSeq(r)
	p.Procs.Observe(r)
	p.queue = append(p.queue, pending{r, arrived, p.Live, false})
	p.accepted++
}

// AcceptLate takes a record the live stream skipped and a later dump of
// the device buffer recovered (issue #65). It is stored like any other,
// but its device time says nothing about clock jumps: lines around it
// arrived long before.
func (p *Pipeline) AcceptLate(r *model.Record, arrived time.Time) {
	if r == nil {
		return
	}
	r.Boot = p.Boot
	p.stampSeq(r)
	p.Procs.Observe(r)
	p.queue = append(p.queue, pending{r, arrived, false, true})
	p.accepted++
}

// stampSeq sets the seq the store will give r: it appends the queue in
// order, so the next free seq plus the records ahead of r. Process tracking
// orders lifetimes by seq, and it sees a record before the store does
// (issue #55). Drain checks the prediction against the store.
func (p *Pipeline) stampSeq(r *model.Record) {
	r.Seq = p.Store.LastSeq() + uint64(len(p.queue)) + 1
}

// SeqMismatches counts records whose predicted seq was wrong (they fell
// back to attribution by device time).
func (p *Pipeline) SeqMismatches() uint64 { return p.seqMiss }

// Accepted / Appended count the records accepted and appended to the
// store so far (catch-up: the line at T_end is stored once Appended
// reaches the Accepted count at the time it was accepted).
func (p *Pipeline) Accepted() uint64 { return p.accepted }
func (p *Pipeline) Appended() uint64 { return p.accepted - uint64(len(p.queue)) }

// Pending reports queued records.
func (p *Pipeline) Pending() int { return len(p.queue) }

// Drain stores queued records older than Delay (all: everything).
func (p *Pipeline) Drain(all bool, now time.Time) error {
	cut := now.Add(-p.Delay)
	n := 0
	for n < len(p.queue) && (all || p.queue[n].arrived.Before(cut) || len(p.queue)-n > 5000) {
		n++
	}
	if n == 0 {
		return nil
	}
	batch := make([]*model.Record, n)
	arrived := make([]time.Time, n)
	live := make([]bool, n)
	late := make([]bool, n)
	unknown := false
	var unknownNs int64
	first := p.Store.LastSeq() + 1
	miss := make([]bool, n)
	for i := 0; i < n; i++ {
		r := p.queue[i].r
		// the store assigns first+i; a record stamped differently is
		// attributed by device time (seq 0), and still gets its real seq
		if want := first + uint64(i); r.Seq != want {
			p.seqMiss++
			r.Seq, miss[i] = want, true
		}
		batch[i], arrived[i], live[i], late[i] = r, p.queue[i].arrived, p.queue[i].live, p.queue[i].late
	}
	// jumps first: attribution compares device times only inside one clock
	// epoch (issue #55)
	jumps := p.detectJumps(batch, arrived, live, late)
	for _, j := range jumps {
		if j.Source != "possible" {
			p.Procs.ClockJump(j.Seq)
		}
	}
	for i, r := range batch {
		seq := r.Seq
		if miss[i] {
			r.Seq = 0
		}
		known := p.Procs.Annotate(r)
		r.Seq = seq
		if !known && r.PID > 0 {
			unknown = true
			unknownNs = max(unknownNs, r.TimeNs)
		}
	}
	if p.BeforeAppend != nil {
		p.BeforeAppend(batch[0])
	}
	if err := p.Store.Append(batch); err != nil {
		return err
	}

	// the batch leaves the queue only once the store has it: a failed
	// Append keeps the records queued for the next Drain, and Appended()
	// counts stored records only (issue #191)
	p.queue = append(p.queue[:0], p.queue[n:]...)
	for _, r := range batch {
		// the detector sees each clock set before the first line that
		// carries the new clock (issue #55); a "possible" jump is a quiet
		// period, not a clock change
		for len(jumps) > 0 && jumps[0].Seq <= r.Seq {
			if jumps[0].Source != "possible" {
				p.Det.ClockJump(jumps[0].DeltaNs)
			}
			jumps = jumps[1:]
		}
		p.Det.Feed(r)
	}
	if unknown && p.OnUnknownPID != nil {
		p.OnUnknownPID(unknownNs)
	}
	return nil
}

// Tick finalizes quiet native crashes (deviceNs 0: newest record time).
func (p *Pipeline) Tick(deviceNs int64) { p.Det.Tick(deviceNs) }

// EmitCrash assigns an ID (crN / anrN) to new crashes and persists the
// current version (later lines with the same ID supersede earlier ones). The
// snapshot c is merged into the latest stored version of its fact, under
// crashMu, by the one attach function (attachLocked).
func (p *Pipeline) EmitCrash(c *analysis.Crash) {
	p.crashMu.Lock()
	defer p.crashMu.Unlock()
	p.attachLocked(c, false)
}

// attachLocked is the only writer of a fact version (issue #225, design
// 3.2.1): it finds the fact the snapshot in belongs to (its own ID, a
// DropBox-only twin to adopt, or an open fact in the persisted index, so a
// restart of the service process between two sources of one crash does not
// split it), re-reads the latest stored version, merges the snapshot into
// it and appends one line. The snapshot is never the version to write: a
// detector or DropBox update written since it was built is not lost. A
// snapshot that adds nothing is not appended. crashMu is held.
func (p *Pipeline) attachLocked(in *analysis.Crash, fromDropbox bool) {
	mode := analysis.MergeMode{}
	if in.ID == "" && !fromDropbox {
		jumps := p.dropboxJumps()
		if !strings.HasPrefix(firstSource(in), "dropbox:") {
			// pick the DropBox-only crash to adopt from the summaries, then
			// adopt from its full version (one decode, not the whole file)
			probe := *in
			same := func(x, c *analysis.Crash) bool { return p.twinCompatible(x, c, jumps) }
			if analysis.AdoptDropboxIn(CrashSummaries(p.Store), &probe, jumps, same) {
				if full := CrashFull(p.Store, probe.ID)[0]; full != nil {
					analysis.AdoptDropboxIn([]*analysis.Crash{full}, in, jumps, same)
					mode.NoDropboxFill = true
				}
			}
		}
		if in.ID == "" {
			pid := in.PID
			if in.Kind == "native" {
				pid = 0
			}
			cands := OpenFacts(p.Store, in.Boot, in.Kind, pid)
			if f, _ := analysis.FindOpenFact(cands, in, jumps, func(f *analysis.Crash) bool { return p.incCompatible(f, in) }); f != nil {
				in.ID = f.ID
				mode.Looked = true
			}
		}
	}
	var latest *analysis.Crash
	if in.ID != "" {
		latest = CrashFull(p.Store, in.ID)[0]
		for try := 0; latest == nil && try < 20 && CrashStored(p.Store, in.ID); try++ {
			// The fact is stored but its line could not be read just now
			// (Windows: crashes.jsonl being replaced by a compaction).
			// Merging onto nothing would append a bare version and, last
			// line wins, erase the stored evidence and ordinal (issue #244).
			time.Sleep(5 * time.Millisecond)
			latest = CrashFull(p.Store, in.ID)[0]
		}
		if latest == nil && CrashStored(p.Store, in.ID) {
			log.Printf("crash %s is stored but unreadable; not overwriting it with a partial version", in.ID)
			return
		}
	}
	out := analysis.MergeFact(latest, in, mode)
	now := time.Now().UnixNano()
	for i := range out.Ev {
		if out.Ev[i].HT == 0 {
			out.Ev[i].HT = now
		}
	}
	p.bindInc(out, latest, fromDropbox)
	if latest != nil && sameVersion(latest, out) {
		return // nothing new: a repeat of evidence already attached
	}
	if out.ID == "" {
		if out.Kind == "anr" && out.N == 0 {
			out.N = p.nextOrdinal(out)
		}
		p.Store.UpdateMeta(func(m *store.Meta) {
			if out.Kind == "anr" {
				m.AnrSeq++
				out.ID = "anr" + strconv.Itoa(m.AnrSeq)
			} else {
				m.CrashSeq++
				out.ID = "cr" + strconv.Itoa(m.CrashSeq)
			}
		})
	}
	in.ID = out.ID
	p.Store.AppendAux("crashes", crashAuxLine{Crash: out, HostNs: now})
}

// nextOrdinal is the 1-based ordinal of the new ANR fact f within its
// process lifetime (issue #225): one more than the stored ANR facts of the
// same boot and pid that are bound to the same lifetime (a weak fact counts
// the weak ones of a compatible process). The lifetime is the tuple's
// (boot, kind, inc, n); n tells a second ANR of one process from the first.
func (p *Pipeline) nextOrdinal(f *analysis.Crash) int {
	n := 1
	for _, x := range OpenFacts(p.Store, f.Boot, "anr", f.PID) {
		if x.PID != f.PID || x.ID == f.ID {
			continue
		}
		switch {
		case !f.IncWeak && !x.IncWeak && x.Inc == f.Inc,
			f.IncWeak && x.IncWeak && analysis.ProcCompatible(x.Process, f.Process):
			n++
		}
	}
	return n
}

// sameVersion reports that b carries nothing a (the stored version) lacks.
func sameVersion(a, b *analysis.Crash) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// weakInc is the incarnation token of a fact whose lifetime is unknown.
func weakInc(pid int, seq uint64) string {
	return "p" + strconv.Itoa(pid) + "@" + strconv.FormatUint(seq, 10)
}

// resolveAnchor is the lifetime the record (pid, device time ns, seq)
// belongs to in the current process table (nil: unknown).
func (p *Pipeline) resolveAnchor(pid int, ns int64, seq uint64) *procs.Proc {
	if pid <= 0 || seq == 0 {
		return nil // no arrival position: no reliable lookup (design 3.1.1)
	}
	return p.Procs.LookupSeq(pid, ns, seq)
}

// incCompatible: the snapshot's source and the stored fact f belong to the
// same process lifetime (design 3.1.1 rule 2): both resolve to the same
// lifetime, or one side is unknown (weak) and the process names are
// compatible. Re-resolved at every attempt, never from the stored Inc alone.
func (p *Pipeline) incCompatible(f, in *analysis.Crash) bool {
	a := p.resolveAnchor(f.PID, f.TimeNs, f.Seq)
	b := p.resolveAnchor(in.PID, in.TimeNs, in.Seq)
	if a != nil && b != nil {
		return a.ID == b.ID
	}
	return analysis.ProcCompatible(f.Process, in.Process)
}

// entryCompatible is the incarnation veto for a DropBox entry (design
// 3.1.1, issue #225 PR3): the entry names a pid and a device time but has
// no seq, so its lifetime is the one that owned pid at that time, and the
// stored fact x is re-resolved from its anchor. Two different lifetimes
// veto the pairing. An unknown side (no table entry, weak binding) does
// not veto: the name, order, reason and cap checks of the matcher decide.
// With clock jumps stored for the boot a device time cannot be placed
// against the table, so nothing is vetoed here either.
func (p *Pipeline) entryCompatible(x *analysis.Crash, pid int, ns int64, jumps []analysis.ClockShift) bool {
	if len(jumps) > 0 || pid <= 0 || ns == 0 {
		return true
	}
	return p.sameLifetime(x, p.Procs.Lookup(pid, ns))
}

// twinCompatible is the same veto for the adoption of a DropBox-only fact x
// by the logcat crash c.
func (p *Pipeline) twinCompatible(x, c *analysis.Crash, jumps []analysis.ClockShift) bool {
	if len(jumps) > 0 || x.PID <= 0 {
		return true
	}
	var b *procs.Proc
	if x.TimeNs != 0 {
		b = p.Procs.Lookup(x.PID, x.TimeNs)
	}
	return p.sameLifetime(c, b)
}

// sameLifetime reports that fact f is not known to belong to another
// lifetime than b (nil b: unknown, no veto).
func (p *Pipeline) sameLifetime(f *analysis.Crash, b *procs.Proc) bool {
	if b == nil {
		return true
	}
	if a := p.resolveAnchor(f.PID, f.TimeNs, f.Seq); a != nil {
		return a.ID == b.ID
	}
	if f.Inc != "" && !f.IncWeak {
		return f.Inc == b.ID
	}
	return true
}

// bindInc sets the incarnation binding of the version about to be written
// (design 3.1.1): bound at creation to the lifetime of the anchor, a weak
// token when none is known; a legacy fact is bound on its first rewrite; an
// open fact whose binding is weak, or real but not what its anchor resolves
// to now, is re-bound forward to that lifetime (same pid, compatible
// name, later start), never to a weak token or nil, never when complete.
func (p *Pipeline) bindInc(out, latest *analysis.Crash, fromDropbox bool) {
	a := p.resolveAnchor(out.PID, out.TimeNs, out.Seq)
	bind := func() {
		if a != nil {
			out.Inc, out.IncWeak = a.ID, false
		} else {
			out.Inc, out.IncWeak = weakInc(out.PID, out.Seq), true
		}
	}
	if latest == nil || latest.Inc == "" {
		bind() // new fact, or a legacy one rewritten for the first time
		return
	}
	out.Inc, out.IncWeak = latest.Inc, latest.IncWeak
	if a == nil || a.ID == latest.Inc || a.PID != out.PID || !analysis.ProcCompatible(a.Name, out.Process) {
		return
	}
	const all = analysis.ClassEvents | analysis.ClassLogcat | analysis.ClassDropbox
	if latest.Classes()&all == all {
		return // a complete fact keeps its binding
	}
	if !latest.IncWeak {
		cur := p.Procs.ByID(latest.Inc)
		if cur != nil && (cur.PID != a.PID || cur.StartSeq >= a.StartSeq) {
			return // forward only
		}
	}
	out.Inc, out.IncWeak = a.ID, false
}

// dropboxJumps are the stored clock jumps of this pipeline's boot session
// that DropBox matching shifts crash times by (issue #93): "possible"
// jumps carry no reliable delta and are left out, and so are jumps before
// the session's first line (an earlier boot).
func (p *Pipeline) dropboxJumps() []analysis.ClockShift {
	m := p.Store.Meta()
	var first uint64
	for _, s := range m.Sessions {
		if s.Boot == p.Boot {
			first = s.FirstSeq
		}
	}
	var out []analysis.ClockShift
	for _, j := range p.Store.ClockJumps() {
		if j.Source != "possible" && j.Seq > first {
			out = append(out, analysis.ClockShift{Seq: j.Seq, DeltaNs: j.DeltaNs})
		}
	}
	return out
}

// ApplyDropbox merges `dumpsys dropbox --print <tag>` output into the crash list.
func (p *Pipeline) ApplyDropbox(out string) {
	seen := map[string]bool{}
	for _, b := range p.Store.ReadAux("dropbox_seen") {
		if k, _, ok := parseSeen(b); ok {
			seen[k] = true
		}
	}
	entries := analysis.ParseDropbox(out, p.Loc)
	// Entries older than the retention window are not imported: their
	// dropbox_seen keys may already have been cleaned up (the device keeps
	// DropBox for ~3 days, the default retention is 1 day), and re-importing
	// them would show old crashes as new. DropBox stamps entries with the
	// device clock, so the cutoff is device now − retention, not the host
	// clock and not the highest device time ever stored, which a clock
	// jump leaves far ahead (issue #53). Entries dated after device now
	// (written before the clock was set back) are kept out of a second
	// import by their dropbox_seen keys.
	var cutoff int64
	now := int64(0)
	if p.DeviceNow != nil {
		now = p.DeviceNow()
	}
	if now == 0 {
		now = p.Store.Stats().NewestNs // newest arrival
	}
	if now != 0 {
		cutoff = now - int64(p.Store.Config().MaxAge)
	}
	for _, e := range entries {
		if t := e.RealTimeNs(); t != 0 && t < cutoff {
			continue
		}
		// Keyed by content when the body names the crashed pid (a pid
		// crashes once, so the body is unique): DropBox re-times entries
		// dated after device now when the clock was set back, and a time
		// key would import the entry again (issue #63). Time keys of
		// older versions are still honoured.
		key := e.Tag + "|" + strconv.FormatInt(e.TimeNs, 10)
		if seen[key] {
			continue
		}
		if e.Note == "" && namesPid(e.Body) {
			key = dropboxBodyKey(e)
			if seen[key] {
				continue
			}
		}
		p.attachDropbox(e, key)
	}
}

// attachDropbox matches one DropBox entry against the stored facts and
// attaches it, all under crashMu: the candidate is chosen from the
// summaries and re-checked on the re-read full version, and the merge is
// onto the latest stored version, so a detector update written meanwhile is
// kept (issue #225, design 3.2.1).
func (p *Pipeline) attachDropbox(e analysis.DropboxEntry, key string) {
	p.crashMu.Lock()
	defer p.crashMu.Unlock()
	jumps := p.dropboxJumps()
	same := func(x *analysis.Crash, pid int, ns int64) bool { return p.entryCompatible(x, pid, ns, jumps) }
	c, isNew := analysis.MatchDropboxIn(CrashSummaries(p.Store), e, p.Boot, jumps, same)
	if c != nil && !isNew {
		full := CrashFull(p.Store, c.ID)[0]
		if full == nil {
			return
		}
		c, isNew = analysis.MatchDropboxIn([]*analysis.Crash{full}, e, p.Boot, jumps, same)
	}
	p.Store.AppendAux("dropbox_seen", seenLine{Key: key, HostNs: time.Now().UnixNano()})
	if c == nil {
		return
	}
	if isNew {
		c.ID = ""
		if c.Boot == "" {
			c.Boot = p.Boot
		}
	}
	if c.Process != "" && (c.Package == "" || c.Package == analysis.PackageOfName(c.Process)) {
		c.Package = p.Procs.OwnerOf(c.Process) // dotted process names (issue #41)
	}
	c.AddEvidence(analysis.Evidence{S: "dropbox:" + e.Tag, T: e.RealTimeNs(), CK: key})
	p.attachLocked(c, true)
}

// SeedPS reconciles processes with `ps -A -o PID,UID,NAME` output taken at device time atNs.
func (p *Pipeline) SeedPS(out string, atNs int64) {
	if e := procs.ParsePS(out); len(e) > 0 {
		p.Procs.Seed(e, atNs)
	}
}

// ReplayText feeds raw `logcat -v threadtime -v epoch -v uid -v usec
// -v printable -D` text through the pipeline as if it arrived live (arrival
// time = record time), then stores everything and finalizes crashes.
func (p *Pipeline) ReplayText(text string) error {
	asm := &logcat.Assembler{Loc: p.Loc, Year: time.Now().Year()}
	var clock time.Time
	feed := func(r *model.Record) error {
		if r == nil {
			return nil
		}
		if t := time.Unix(0, r.TimeNs); t.After(clock) {
			clock = t
		}
		p.Accept(r, clock)
		return p.Drain(false, clock)
	}
	for _, ln := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if err := feed(asm.Push(ln)); err != nil {
			return err
		}
	}
	if err := feed(asm.Flush()); err != nil {
		return err
	}
	if err := p.Drain(true, clock); err != nil {
		return err
	}
	p.Tick(clock.Add(time.Minute).UnixNano())
	p.Store.Flush()
	return nil
}

// LoadProcs reads process lifetimes (last version of each entry wins).
func LoadProcs(st *store.Store) []procs.Proc {
	m := map[string]procs.Proc{}
	var order []string
	for _, b := range st.ReadAux("procs") {
		var p procs.Proc
		if json.Unmarshal(b, &p) != nil {
			continue
		}
		k := p.ID
		if k == "" { // pre-ID files
			k = fmt.Sprintf("%s|%d|%d|%s", p.Boot, p.PID, p.StartNs, p.Name)
		}
		if _, ok := m[k]; !ok {
			order = append(order, k)
		}
		m[k] = p
	}
	out := make([]procs.Proc, 0, len(order))
	for _, k := range order {
		out = append(out, m[k])
	}
	return out
}

// LoadPackages reads the shared uid → packages cache.
func LoadPackages(st *store.Store) map[int][]string {
	b, err := os.ReadFile(filepath.Join(st.Dir, "packages.json"))
	if err != nil {
		return nil
	}
	var m map[int][]string
	json.Unmarshal(b, &m)
	return m
}

// WritePackages stores the uid → packages cache.
func WritePackages(st *store.Store, m map[int][]string) {
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(st.Dir, "packages.json"), b, 0o644)
}

// CloseOtherBoots ends process entries of earlier boots that never got an
// end (the device rebooted under them). endNs is the last record time of
// that boot, if known.
func CloseOtherBoots(st *store.Store, boot string) {
	var open []procs.Proc
	for _, p := range LoadProcs(st) {
		if p.Boot != boot && p.Alive() {
			open = append(open, p)
		}
	}
	for _, p := range open {
		e := BootEndNs(st, p.Boot)
		if e <= 0 {
			e = max(p.StartNs, p.SeenNs)
		}
		p.EndNs, p.EndReason = e, "device rebooted"
		appendProc(st, p)
	}
}

// BootEndNs is the time of the last stored record of a boot session (0 if unknown).
func BootEndNs(st *store.Store, boot string) int64 {
	m := st.Meta()
	for i, s := range m.Sessions {
		if s.Boot != boot {
			continue
		}
		last := st.LastSeq()
		if i+1 < len(m.Sessions) {
			last = m.Sessions[i+1].FirstSeq - 1
		}
		var t int64
		st.Scan(0, last+1, true, func(r *model.Record) bool {
			if r.Boot == boot {
				t = r.TimeNs
			}
			return false
		})
		return t
	}
	return 0
}

func firstSource(c *analysis.Crash) string {
	if len(c.Sources) == 0 {
		return ""
	}
	return c.Sources[0]
}

// dropboxBodyKey is the dropbox_seen key of an entry with contents:
// "tag|b:<FNV-64a of the body, hex>". Stable when DropBox re-times the
// entry (issue #63).
func dropboxBodyKey(e analysis.DropboxEntry) string {
	h := fnv.New64a()
	h.Write([]byte(e.Body))
	return e.Tag + "|b:" + strconv.FormatUint(h.Sum64(), 16)
}

// namesPid reports whether a DropBox body names the crashed pid ("PID: n"
// in crash and ANR headers, "pid: n," in tombstones).
func namesPid(body string) bool {
	return strings.Contains(body, "\nPID: ") || strings.HasPrefix(body, "PID: ") || strings.Contains(body, "pid: ")
}
