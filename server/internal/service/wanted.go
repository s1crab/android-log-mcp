package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/s1crab/android-log-mcp/server/internal/device"
)

// run/wanted.json (issue #226, design 3.3): the devices a service process
// was recording, written by it while it runs so that the successor of a
// replaced, handed-off or crashed service resumes them (the set lives only in
// memory otherwise). It is additive and optional: no version field, readers
// ignore unknown keys, a 0.9.x binary never reads it (its sweep may delete
// it: the carry is lost, nothing else changes).
//
//	{"writer":{"pid":4242,"start_time":123456789},"written_at":1792000000000,
//	 "devices":{"avd-Pixel_6":{"idle_ms":240000,"linger_ms":3600000}}}
//
// writer is the pid and start time of the writing service (the replaced
// holder cannot be taken from service.json, which a stale-state cleanup may
// have removed). There is deliberately no top-level "pid" key: layout.SweepRun
// would read the file as a process endpoint (it is in layout.KeepRun too).
// Lazy recording (#11) is unchanged: no file, an expired file or the file of
// an idle exit or explicit stop restores nothing and runs no adb.

type wantedWriterID struct {
	PID       int   `json:"pid"`
	StartTime int64 `json:"start_time,omitempty"`
}

type wantedDevice struct {
	IdleMS   int64 `json:"idle_ms"`   // time since its last log-tool call, at write time
	LingerMS int64 `json:"linger_ms"` // the linger in force (-1: forever)
}

type wantedFile struct {
	Writer    wantedWriterID          `json:"writer"`
	WrittenAt int64                   `json:"written_at"` // wall clock, ms
	Devices   map[string]wantedDevice `json:"devices"`
}

// WantedTiming holds the durations of the wanted.json carry; zero values are
// the defaults (tests shorten them per service, like RepairTiming).
type WantedTiming struct {
	Debounce       time.Duration // set changes are written after this (1 s)
	Refresh        time.Duration // a call that only refreshes "last" rewrites at most this often (60 s)
	RetryBase      time.Duration // first retry of a failed rename (1 s, doubling)
	RetryMax       time.Duration // cap of the retry backoff (30 s)
	WriterExitWait time.Duration // how long a still-alive writer may take to exit (2 s)
	TTL            time.Duration // longest downtime that is carried over (LingerMax)
	FinalTries     int           // rename/remove attempts in Stop (5)
	FinalGap       time.Duration // between them (50 ms)

	// Rename and Remove replace os.Rename and os.Remove for wanted.json
	// (tests simulate a Windows sharing violation, another process holding
	// the file open, deterministically on every OS).
	Rename func(oldpath, newpath string) error
	Remove func(path string) error
}

func (t WantedTiming) withDefaults() WantedTiming {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&t.Debounce, time.Second)
	def(&t.Refresh, 60*time.Second)
	def(&t.RetryBase, time.Second)
	def(&t.RetryMax, 30*time.Second)
	def(&t.WriterExitWait, 2*time.Second)
	def(&t.TTL, LingerMax)
	def(&t.FinalGap, 50*time.Millisecond)
	if t.FinalTries <= 0 {
		t.FinalTries = 5
	}
	if t.Rename == nil {
		t.Rename = os.Rename
	}
	if t.Remove == nil {
		t.Remove = os.Remove
	}
	return t
}

// readWanted reads run/wanted.json (nil, nil: there is none).
func readWanted(path string) (*wantedFile, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f wantedFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if f.Writer.PID <= 0 || f.WrittenAt <= 0 {
		return nil, errors.New("no writer or write time")
	}
	return &f, nil
}

// removeWantedFile removes path; a file that is already gone is fine.
func removeWantedFile(rm func(string) error, path string) error {
	if err := rm(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ClearWanted removes run/wanted.json after an explicit `service stop
// --force` (the service cannot tell that from an automatic replacement, the
// CLI can). On Windows the rename/remove can fail while another process has
// the file open, so it is retried a few times; a file that stays is covered by
// the TTL and the per-device linger.
func ClearWanted(ctrl Control) {
	t := WantedTiming{}.withDefaults()
	var err error
	for i := 0; i < t.FinalTries; i++ {
		if err = removeWantedFile(t.Remove, ctrl.WantedPath()); err == nil {
			return
		}
		time.Sleep(t.FinalGap)
	}
	log.Printf("service: removing %s: %v (it expires by itself)", ctrl.WantedPath(), err)
}

// wantedWriter keeps run/wanted.json current. One object, no goroutine: a
// time.AfterFunc debounces the writes (set changes: Debounce; a call that
// only refreshes a device's last call: at most one per Refresh, so a "last"
// is at most about Refresh old after a crash: a device may expire that much
// early, never late). All writes are serialized by mu; closed makes a timer
// that fires after Close a no-op.
type wantedWriter struct {
	path  string
	id    wantedWriterID
	tm    WantedTiming
	infos func(time.Time) map[string]device.WantInfo

	mu        sync.Mutex
	timer     *time.Timer
	closed    bool
	dirty     bool          // the file differs from the wanted content (a failed rename)
	touched   bool          // this writer has written the file (an empty set may then remove it)
	backoff   time.Duration // next retry delay after a failure
	lastWrite time.Time
}

func newWantedWriter(path string, tm WantedTiming, infos func(time.Time) map[string]device.WantInfo) *wantedWriter {
	w := &wantedWriter{path: path, tm: tm.withDefaults(), infos: infos}
	w.id.PID = os.Getpid()
	w.id.StartTime, _, _ = inspectProc(w.id.PID)
	return w
}

// Changed is Manager.OnWantChange (never blocks).
func (w *wantedWriter) Changed(setChanged bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.timer != nil {
		return // a pending write recomputes the content when it runs
	}
	if !setChanged && !w.lastWrite.IsZero() && time.Since(w.lastWrite) < w.tm.Refresh {
		return
	}
	w.arm(w.tm.Debounce)
}

func (w *wantedWriter) arm(d time.Duration) {
	w.timer = time.AfterFunc(d, w.fire)
}

func (w *wantedWriter) fire() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer = nil
	if w.closed {
		return
	}
	if err := w.apply(); err != nil {
		w.dirty = true
		if w.backoff <= 0 {
			w.backoff = w.tm.RetryBase
		} else {
			w.backoff = min(w.backoff*2, w.tm.RetryMax)
		}
		log.Printf("service: %s: %v; retrying in %s", w.path, err, w.backoff)
		w.arm(w.backoff)
		return
	}
	w.dirty, w.backoff = false, 0
}

// apply writes the current content once (removing the file when nothing is
// wanted and this writer wrote it before). A failed rename leaves no temp file.
func (w *wantedWriter) apply() error {
	now := time.Now()
	infos := w.infos(now)
	if len(infos) == 0 {
		if !w.touched {
			return nil // never overwrite or remove a file nobody of ours wrote
		}
		if err := removeWantedFile(w.tm.Remove, w.path); err != nil {
			return err
		}
		w.lastWrite = now
		return nil
	}
	f := wantedFile{Writer: w.id, WrittenAt: now.UnixMilli(), Devices: map[string]wantedDevice{}}
	for id, i := range infos {
		l := int64(-1)
		if i.Linger >= 0 {
			l = i.Linger.Milliseconds()
		}
		f.Devices[id] = wantedDevice{IdleMS: i.Idle.Milliseconds(), LingerMS: l}
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := w.path + ".tmp" // one serialized writer: a fixed name is enough
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := w.tm.Rename(tmp, w.path); err != nil {
		os.Remove(tmp)
		return err
	}
	w.touched = true
	w.lastWrite = now
	return nil
}

// Close is the final synchronous flush of Stop: it stops the timer, forbids
// later writes (a callback that started earlier finished first, it held mu),
// then removes the file (clear: idle exit, `stop` op) or writes the final
// state (handoff, signal), retrying up to FinalTries x FinalGap so that Stop
// is not delayed beyond that. A file that cannot be changed is harmless: the
// TTL and each device's linger bound what a successor restores from it.
func (w *wantedWriter) Close(clear bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	var err error
	for i := 0; i < w.tm.FinalTries; i++ {
		if clear {
			err = removeWantedFile(w.tm.Remove, w.path)
		} else {
			err = w.apply()
		}
		if err == nil {
			return
		}
		if i+1 < w.tm.FinalTries {
			time.Sleep(w.tm.FinalGap)
		}
	}
	log.Printf("service: final update of %s: %v (it expires by itself)", w.path, err)
}

// restoreItem is one device a successor resumes.
type restoreItem struct {
	ID   string
	Last time.Time // the last call to assume (never in the future)
	Left time.Duration
}

// planRestore applies the TTL and the per-device linger to f at now (design
// 3.3): gap = now - written_at (wall clock, clamped to >= 0); a device is
// restored iff gap <= ttl and (linger < 0 or linger - idle - gap > 0), with
// last = now - (idle + gap), so its normal linger timer ends the recording at
// the remaining time. A forward clock jump makes gap too large (nothing or
// less is restored: the lazy rule), a backward one clamps it to 0 (a device
// records at most one more linger).
func planRestore(f *wantedFile, now time.Time, ttl time.Duration) []restoreItem {
	gap := max(0, now.Sub(time.UnixMilli(f.WrittenAt)))
	if gap > ttl {
		return nil
	}
	var out []restoreItem
	for id, d := range f.Devices {
		if id == "" {
			continue
		}
		idle := max(0, time.Duration(d.IdleMS)*time.Millisecond)
		var left time.Duration = -1
		if d.LingerMS >= 0 {
			left = time.Duration(d.LingerMS)*time.Millisecond - idle - gap
			if left <= 0 {
				continue
			}
		}
		out = append(out, restoreItem{ID: id, Last: now.Add(-(idle + gap)), Left: left})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// writerGone decides whether f may be used (design 3.3 "Ownership during
// handoff"): its writer is dead or its pid was reused (accepted); alive with
// the same start time (the replaced holder still exiting, or an unrelated
// process): waited for up to wait, then ignored; this process: ignored; an OS
// that cannot read start times: only the TTL applies.
func writerGone(w wantedWriterID, wait time.Duration, stop <-chan struct{}) (ok bool, why string) {
	if w.PID == os.Getpid() {
		return false, "its writer is this process"
	}
	start, _, alive := inspectProc(w.PID)
	if !alive {
		return true, ""
	}
	if w.StartTime == 0 || start == 0 {
		return true, "" // no start time on this OS: the TTL is the only check
	}
	if start != w.StartTime {
		return true, "" // the pid was reused: the writer is dead
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		select {
		case <-stop:
			return false, "the service is stopping"
		case <-time.After(20 * time.Millisecond):
		}
		if s2, _, a2 := inspectProc(w.PID); !a2 || s2 != w.StartTime {
			return true, ""
		}
	}
	return false, fmt.Sprintf("its writer (pid %d) is still running and is not the replaced service", w.PID)
}

// restoreWanted is the one goroutine that applies the carry after Start has
// the socket up and service.json written: Start never waits for it, and Want
// does not wait for the device list (it attaches the device when it
// appears). It ends on Stop; Stop waits for it so nothing is started after the
// manager closed.
func (s *Service) restoreWanted(f *wantedFile) {
	defer s.restoreWG.Done()
	if d, _ := lingerFor(0, s.getenv(LingerEnv)); d == 0 {
		log.Printf("service: not resuming the devices of %s: %s=0", s.opt.Control.WantedPath(), LingerEnv)
		return
	}
	if ok, why := writerGone(f.Writer, s.wantedTm.WriterExitWait, s.stopping); !ok {
		log.Printf("service: ignoring %s: %s", s.opt.Control.WantedPath(), why)
		return
	}
	items := planRestore(f, time.Now(), s.wantedTm.TTL)
	if len(items) == 0 {
		log.Printf("service: %s has no device to resume (expired or past its linger)", s.opt.Control.WantedPath())
		return
	}
	for _, it := range items {
		select {
		case <-s.stopping:
			return
		default:
		}
		m := s.Manager()
		if m == nil {
			return
		}
		if err := m.RestoreWant(it.ID, it.Last, time.Time{}); err != nil {
			continue // logged; ErrMaxDevices must not stop the others
		}
		if it.Left < 0 {
			log.Printf("%s: resumed after the replaced or crashed service (linger: forever)", it.ID)
		} else {
			log.Printf("%s: resumed after the replaced or crashed service (%s of its linger left)", it.ID, it.Left.Round(time.Second))
		}
	}
}
