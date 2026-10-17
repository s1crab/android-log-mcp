package device

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/s1crab/android-log-mcp/server/internal/analysis"
	"github.com/s1crab/android-log-mcp/server/internal/store"
)

// crashIndex is the shared in-memory index of one device's crashes.jsonl
// (issue #22). It holds a small summary of the newest version of each
// crash (no stack, traces or DropBox text) and the file offset of that
// version, so:
//   - the recorder and tool readers no longer re-read and decode the whole
//     file for every crash and every call: only bytes appended since the
//     last read are decoded;
//   - the full crash is decoded on demand from its offset;
//   - a rewrite of the file (CompactAux, or another process compacting)
//     is detected (different file, shorter file, or changed bytes before
//     the indexed end) and the index is rebuilt.
//
// One index per crashes.jsonl path, shared by every Store of that device
// directory in this process.
type crashIndex struct {
	mu     sync.Mutex
	path   string
	fi     os.FileInfo // the file indexed (nil: none yet)
	off    int64       // bytes indexed, always at a line boundary
	tail   []byte      // the last bytes before off (rewrite check)
	ents   map[string]*crashEnt
	order  []string          // ids in order of first appearance
	sorted []*analysis.Crash // cached summaries, newest first (nil: rebuild)
	// open-fact lookup (issue #225): ids per (boot, kind, pid) and per
	// (boot, kind), in order of first appearance
	byKey  map[openKey][]string
	byKind map[openKey][]string
}

type openKey struct {
	boot, kind string
	pid        int
}

type crashEnt struct {
	sum *analysis.Crash // summary: heavy text fields empty
	off int64           // newest version: byte offset and length (without '\n')
	n   int
}

// crashSummary is the subset of analysis.Crash kept in the index: the
// fields list views, matching and grouping use. Body, Traces, CPU, Dropbox
// and Tombstone are decoded only on demand.
type crashSummary struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Boot      string   `json:"boot"`
	TimeNs    int64    `json:"t"`
	Seq       uint64   `json:"seq,omitempty"`
	PID       int      `json:"pid"`
	Process   string   `json:"process,omitempty"`
	Package   string   `json:"package,omitempty"`
	Thread    string   `json:"thread,omitempty"`
	Headline  string   `json:"headline"`
	Signature string   `json:"sig,omitempty"`
	Sources   []string `json:"sources,omitempty"`
	Location  string   `json:"location,omitempty"`
	// persisted-fact fields (issue #225): small, kept for the open-fact lookup
	Inc     string              `json:"inc,omitempty"`
	IncWeak bool                `json:"incw,omitempty"`
	N       int                 `json:"n,omitempty"`
	Ev      []analysis.Evidence `json:"ev,omitempty"`
}

const crashTailBytes = 64

var crashIndexes sync.Map // abs path of crashes.jsonl → *crashIndex

// crashLinesIndexed counts crashes.jsonl lines decoded into summaries
// (tests: ingest must stay linear in the number of crashes).
var crashLinesIndexed atomic.Int64

func crashIndexOf(st *store.Store) *crashIndex {
	p := filepath.Join(st.Dir, "crashes.jsonl")
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	if x, ok := crashIndexes.Load(p); ok {
		return x.(*crashIndex)
	}
	x, _ := crashIndexes.LoadOrStore(p, &crashIndex{path: p})
	return x.(*crashIndex)
}

func (x *crashIndex) resetLocked() {
	x.fi, x.off, x.tail = nil, 0, nil
	x.ents, x.order, x.sorted = map[string]*crashEnt{}, nil, nil
	x.byKey, x.byKind = map[openKey][]string{}, map[openKey][]string{}
}

// openCrashes opens crashes.jsonl for the index. A variable so that a test
// can fail it the way Windows does while another goroutine replaces the
// file (a sharing violation or access denied that clears in milliseconds).
var openCrashes = os.Open

// openCrashRetry opens crashes.jsonl, retrying for up to about 200 ms when
// the failure is not "does not exist": on Windows an open can fail with a
// sharing violation or access denied while CompactAux replaces the file
// (rename), and that is gone in milliseconds. Giving up at the first
// failure made readers see "no such crash" for a stored one (issue #244).
var crashOpenTries = 40

func openCrashRetry(path string) (*os.File, error) {
	var f *os.File
	var err error
	for i := 0; i < crashOpenTries; i++ {
		if f, err = openCrashes(path); err == nil || os.IsNotExist(err) {
			return f, err
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil, err
}

// stored reports that the index knows crash id (after a refresh); false
// also when the file cannot be read right now.
func (x *crashIndex) stored(id string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if f := x.refreshLocked(); f != nil {
		f.Close()
	}
	return x.ents[id] != nil
}

// CrashStored reports that crash id is in the persisted index.
func CrashStored(st *store.Store, id string) bool { return crashIndexOf(st).stored(id) }

// refreshLocked brings the index up to date and returns the open file
// (nil when there is none); the caller closes it.
func (x *crashIndex) refreshLocked() *os.File {
	if x.ents == nil {
		x.resetLocked()
	}
	f, err := openCrashRetry(x.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil // not "the file is gone": keep the index; callers treat nil as "unreadable now"
		}
		if x.fi != nil || x.off > 0 {
			x.resetLocked()
		}
		return nil
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil
	}
	if x.fi != nil && (!os.SameFile(x.fi, fi) || fi.Size() < x.off || !x.tailMatches(f)) {
		x.resetLocked() // rewritten (compaction) or replaced: rebuild
	}
	x.fi = fi
	if fi.Size() > x.off {
		buf := make([]byte, fi.Size()-x.off)
		n, _ := f.ReadAt(buf, x.off)
		buf = buf[:n]
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			x.addLocked(buf[:i+1], x.off)
			x.off += int64(i + 1)
			x.tail = append(x.tail[:0], buf[max(0, i+1-crashTailBytes):i+1]...)
			if len(x.tail) < crashTailBytes && x.off > int64(len(x.tail)) {
				x.tail = x.readTail(f)
			}
		}
	}
	return f
}

func (x *crashIndex) readTail(f *os.File) []byte {
	n := min(int64(crashTailBytes), x.off)
	b := make([]byte, n)
	if m, _ := f.ReadAt(b, x.off-n); int64(m) != n {
		return nil
	}
	return b
}

func (x *crashIndex) tailMatches(f *os.File) bool {
	if x.off == 0 {
		return true
	}
	b := x.readTail(f)
	return b != nil && bytes.Equal(b, x.tail)
}

// addLocked indexes complete lines (data ends with '\n') starting at base.
func (x *crashIndex) addLocked(data []byte, base int64) {
	pos := 0
	for pos < len(data) {
		i := bytes.IndexByte(data[pos:], '\n')
		line := data[pos : pos+i]
		crashLinesIndexed.Add(1)
		var s crashSummary
		if len(line) > 0 && json.Unmarshal(line, &s) == nil && s.ID != "" {
			c := &analysis.Crash{ID: s.ID, Kind: s.Kind, Boot: s.Boot, TimeNs: s.TimeNs, Seq: s.Seq, PID: s.PID,
				Process: s.Process, Package: s.Package, Thread: s.Thread, Headline: s.Headline, Signature: s.Signature,
				Sources: s.Sources[:len(s.Sources):len(s.Sources)], Location: s.Location, // full slice: appends by callers copy
				Inc: s.Inc, IncWeak: s.IncWeak, N: s.N, Ev: s.Ev[:len(s.Ev):len(s.Ev)]}
			e := x.ents[s.ID]
			if e == nil {
				e = &crashEnt{}
				x.ents[s.ID] = e
				x.order = append(x.order, s.ID)
				x.keyLocked(c, s.ID)
			} else if e.sum.Boot != c.Boot || e.sum.Kind != c.Kind || e.sum.PID != c.PID {
				x.unkeyLocked(e.sum, s.ID)
				x.keyLocked(c, s.ID)
			}
			e.sum, e.off, e.n = c, base+int64(pos), len(line)
			x.sorted = nil
		}
		pos += i + 1
	}
}

func (x *crashIndex) keyLocked(c *analysis.Crash, id string) {
	k := openKey{c.Boot, c.Kind, c.PID}
	x.byKey[k] = append(x.byKey[k], id)
	bk := openKey{c.Boot, c.Kind, 0}
	x.byKind[bk] = append(x.byKind[bk], id)
}

func (x *crashIndex) unkeyLocked(c *analysis.Crash, id string) {
	drop := func(m map[openKey][]string, k openKey) {
		l := m[k]
		for i, v := range l {
			if v == id {
				m[k] = append(l[:i:i], l[i+1:]...)
				return
			}
		}
	}
	drop(x.byKey, openKey{c.Boot, c.Kind, c.PID})
	drop(x.byKind, openKey{c.Boot, c.Kind, 0})
}

// openFor returns the newest versions' summaries of the facts of boot and
// kind with this pid (pid 0: every pid), newest first by arrival. The
// index is refreshed first. Shared: callers must not modify them.
func (x *crashIndex) openFor(boot, kind string, pid int) []*analysis.Crash {
	x.mu.Lock()
	defer x.mu.Unlock()
	if f := x.refreshLocked(); f != nil {
		f.Close()
	}
	ids := x.byKey[openKey{boot, kind, pid}]
	if pid == 0 {
		ids = x.byKind[openKey{boot, kind, 0}]
	}
	out := make([]*analysis.Crash, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		if e := x.ents[ids[i]]; e != nil {
			out = append(out, e.sum)
		}
	}
	return out
}

func (x *crashIndex) summariesLocked() []*analysis.Crash {
	if x.sorted == nil {
		// newest first by arrival: the order of first appearance in
		// crashes.jsonl, reversed. Device time is not an order: after a
		// device clock jump back, newer crashes have older device times
		// (issue #55). DropBox-only crashes (Seq 0) have a position too.
		out := make([]*analysis.Crash, 0, len(x.order))
		for i := len(x.order) - 1; i >= 0; i-- {
			out = append(out, x.ents[x.order[i]].sum)
		}
		x.sorted = out
	}
	return x.sorted
}

// summaries returns the crash summaries, newest first. The slice and the
// crashes are shared: callers must not modify them.
func (x *crashIndex) summaries() []*analysis.Crash {
	x.mu.Lock()
	defer x.mu.Unlock()
	if f := x.refreshLocked(); f != nil {
		f.Close()
	}
	return x.summariesLocked()
}

// full decodes the newest version of the given crashes (nil entries for
// ids no longer present). The returned crashes are the caller's.
func (x *crashIndex) full(ids ...string) []*analysis.Crash {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make([]*analysis.Crash, len(ids))
	f := x.refreshLocked()
	if f == nil {
		return out
	}
	defer f.Close()
	var buf []byte
	for i, id := range ids {
		e := x.ents[id]
		if e == nil {
			continue
		}
		if cap(buf) < e.n {
			buf = make([]byte, e.n)
		}
		b := buf[:e.n]
		if _, err := f.ReadAt(b, e.off); err != nil && err != io.EOF {
			continue
		}
		c := new(analysis.Crash)
		if json.Unmarshal(b, c) == nil && c.ID == id {
			out[i] = c
		}
	}
	return out
}

// CrashSummaries returns the device's crashes (newest version of each,
// newest first) without their stack, traces and DropBox text; use
// CrashFull for those. Shared: callers must not modify the crashes.
func CrashSummaries(st *store.Store) []*analysis.Crash {
	return crashIndexOf(st).summaries()
}

// CrashFull decodes the newest version of each given crash id in full
// (nil for ids that are not stored).
func CrashFull(st *store.Store, ids ...string) []*analysis.Crash {
	return crashIndexOf(st).full(ids...)
}

// OpenFacts returns the stored facts of boot and kind with this pid (0:
// every pid), newest first, as summaries (no stack or DropBox text), found
// through the index and not through detector memory (issue #225).
func OpenFacts(st *store.Store, boot, kind string, pid int) []*analysis.Crash {
	return crashIndexOf(st).openFor(boot, kind, pid)
}

// LoadCrashes reads all crashes in full (newest version of each ID, newest
// first). Prefer CrashSummaries + CrashFull for list views.
func LoadCrashes(st *store.Store) []*analysis.Crash {
	x := crashIndexOf(st)
	x.mu.Lock()
	var ids []string
	f := x.refreshLocked()
	if f != nil {
		f.Close()
	}
	for _, c := range x.summariesLocked() {
		ids = append(ids, c.ID)
	}
	x.mu.Unlock()
	out := x.full(ids...)
	n := 0
	for _, c := range out {
		if c != nil {
			out[n] = c
			n++
		}
	}
	return out[:n]
}
