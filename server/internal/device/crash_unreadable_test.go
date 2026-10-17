package device

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"
)

// Issue #244: on Windows crashes.jsonl cannot be opened for a few
// milliseconds while a compaction replaces it. A reader that gave up (or
// reset the index) made attach merge onto "no stored version" and append a
// bare snapshot for an existing id: last line wins, so the events evidence
// and the ordinal n of the ANR were erased. Deterministic stand-in for the
// race: the open fails a fixed number of times at a chosen moment.
func failOpens(t *testing.T, n int64) *atomic.Int64 {
	t.Helper()
	var left atomic.Int64
	left.Store(n)
	old := openCrashes
	openCrashes = func(p string) (*os.File, error) {
		if left.Add(-1) >= 0 {
			return nil, errors.New("The process cannot access the file because it is being used by another process.")
		}
		return old(p)
	}
	t.Cleanup(func() { openCrashes = old })
	return &left
}

func TestAttachSurvivesTransientUnreadableCrashFile(t *testing.T) {
	r := newFactRig(t) // not parallel: the hook is package-wide
	r.feed(r.amANR(100, 100, "com.foo", reasonA))
	if len(r.facts()) != 1 {
		t.Fatal("setup")
	}
	failOpens(t, 3) // the next attach finds the file unreadable three times
	r.feed(r.anrBlock(102, 100, "com.foo", reasonA))
	fs := r.facts()
	if len(fs) != 1 {
		t.Fatalf("%d facts", len(fs))
	}
	f := fs[0]
	if f.N != 1 || !hasAll(f, "events:am_anr", "logcat:system") {
		t.Fatalf("n=%d sources=%v: the stored evidence was overwritten", f.N, f.Sources)
	}
}

func TestAttachNeverOverwritesAStoredFactItCannotRead(t *testing.T) {
	r := newFactRig(t)
	r.feed(r.amANR(100, 100, "com.foo", reasonA))
	before := len(r.versions(r.facts()[0].ID))
	failOpens(t, 1<<40) // unreadable for the whole attach
	crashOpenTries = 2
	t.Cleanup(func() { crashOpenTries = 40 })
	r.feed(r.anrBlock(102, 100, "com.foo", reasonA))
	openCrashes = os.Open
	fs := r.facts()
	if len(fs) != 1 {
		t.Fatalf("%d facts", len(fs))
	}
	if n := len(r.versions(fs[0].ID)); n != before {
		t.Errorf("%d versions, want %d: a partial version was appended", n, before)
	}
	if fs[0].N != 1 || !hasSource(fs[0], "events:am_anr") {
		t.Errorf("n=%d sources=%v", fs[0].N, fs[0].Sources)
	}
}
