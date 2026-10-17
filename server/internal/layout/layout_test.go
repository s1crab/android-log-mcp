package layout

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/s1crab/android-log-mcp/server/internal/store"
)

func write(t *testing.T, p string, size int, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		os.Chtimes(p, mod, mod)
	}
}

// devDir creates a device directory with meta.json and n segments of size
// bytes each, all last modified at mod (segment i at mod + i s).
func devDir(t *testing.T, dir string, n, size int, mod time.Time) {
	t.Helper()
	b, _ := json.Marshal(store.Meta{Serial: filepath.Base(dir)})
	write(t, filepath.Join(dir, "meta.json"), 0, mod)
	os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644)
	os.Chtimes(filepath.Join(dir, "meta.json"), mod, mod)
	write(t, filepath.Join(dir, "crashes.jsonl"), 10, mod)
	for i := 0; i < n; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("seg-%020d.jsonl", 1+i*100)), size, mod.Add(time.Duration(i)*time.Second))
	}
}

// Old layouts (pre-0.4 flat <serial>/ dirs, legacy/, android-log-mcp.*
// siblings) are ignored: not read, not migrated, not deleted, even when old.
func oldLayout(t *testing.T, base string, mod time.Time) []string {
	devDir(t, filepath.Join(base, "emulator-5554"), 2, 100, mod)
	devDir(t, filepath.Join(base, "legacy", "v1-backup", "emulator-5556"), 2, 100, mod)
	devDir(t, filepath.Join(filepath.Dir(base), filepath.Base(base)+".v1-backup", "emulator-5556"), 2, 100, mod)
	return []string{
		filepath.Join(base, "emulator-5554", "seg-00000000000000000001.jsonl"),
		filepath.Join(base, "emulator-5554", "meta.json"),
		filepath.Join(base, "legacy", "v1-backup", "emulator-5556", "seg-00000000000000000101.jsonl"),
		filepath.Join(filepath.Dir(base), filepath.Base(base)+".v1-backup", "emulator-5556", "meta.json"),
	}
}

// Retention applies to every format version and tmp/; what this process
// records is left to its store; old layouts and unknown files are never
// touched.
func TestSweepMaxAgeAcrossVersions(t *testing.T) {
	base := filepath.Join(t.TempDir(), "android-log-mcp")
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	fresh := now.Add(-time.Hour)
	devDir(t, filepath.Join(base, "v1", "avd-old"), 2, 100, old)   // current format, unowned, expired
	devDir(t, filepath.Join(base, "v1", "avd-mixed"), 2, 100, old) // one expired segment + fresh aux
	write(t, filepath.Join(base, "v1", "avd-mixed", "seg-00000000000000009999.jsonl"), 100, fresh)
	devDir(t, filepath.Join(base, "v1", "avd-owned"), 2, 100, old) // recorded by us: untouched
	devDir(t, filepath.Join(base, "v0", "dev"), 2, 100, old)       // older format
	devDir(t, filepath.Join(base, "v2", "dev"), 2, 100, fresh)     // other format, still fresh
	ignored := oldLayout(t, base, old)
	write(t, filepath.Join(base, "tmp", "report-123.zip"), 50, now.Add(-2*time.Hour))
	write(t, filepath.Join(base, "tmp", "fresh.tmp"), 50, now)
	write(t, filepath.Join(base, "my-notes.txt"), 5, old) // not ours
	write(t, filepath.Join(base, "photos", "a.jpg"), 5, old)

	owned := &store.Store{Dir: filepath.Join(base, "v1", "avd-owned")}
	res := Sweep(SweepOptions{Base: base, MaxAge: 24 * time.Hour, Owned: map[string]*store.Store{owned.Dir: owned}, Now: now})

	gone := []string{"v1/avd-old", "v0", "tmp/report-123.zip",
		"v1/avd-mixed/seg-00000000000000000001.jsonl", "v1/avd-mixed/seg-00000000000000000101.jsonl"}
	for _, g := range gone {
		if exists(filepath.Join(base, g)) {
			t.Errorf("%s survived the sweep", g)
		}
	}
	kept := []string{"v1/avd-owned/seg-00000000000000000001.jsonl", "v1/avd-mixed/seg-00000000000000009999.jsonl",
		"v2/dev/seg-00000000000000000001.jsonl", "tmp/fresh.tmp", "my-notes.txt", "photos/a.jpg", "v1"}
	for _, k := range kept {
		if !exists(filepath.Join(base, k)) {
			t.Errorf("%s was removed", k)
		}
	}
	for _, p := range ignored {
		if !exists(p) {
			t.Errorf("old-layout file %s was touched", p)
		}
	}
	if len(res.RemovedDirs) < 2 {
		t.Errorf("removed dirs %v", res.RemovedDirs)
	}
}

// The total cap counts every format version, tmp/, run/ and owners/ and
// removes the oldest files first; each current-format device keeps its
// newest segment. Old layouts are neither counted nor removed.
func TestSweepTotalCapAcrossVersions(t *testing.T) {
	base := filepath.Join(t.TempDir(), "android-log-mcp")
	now := time.Now()
	ignored := oldLayout(t, base, now.Add(-20*time.Hour))
	devDir(t, filepath.Join(base, "v0", "e"), 3, 1000, now.Add(-10*time.Hour)) // oldest
	devDir(t, filepath.Join(base, "v1", "avd-a"), 3, 1000, now.Add(-6*time.Hour))
	write(t, filepath.Join(base, "tmp", "x.zip"), 1000, now.Add(-7*time.Hour))
	write(t, filepath.Join(base, "run", "junk.txt"), 10, now)
	total := TotalBytes(base)
	if total < 7000 || total > 7100 {
		t.Fatalf("TotalBytes %d (old layouts must not count)", total)
	}
	res := Sweep(SweepOptions{Base: base, MaxTotal: 3500, Now: now})
	if !res.CapTriggered || res.TotalAfter > 3500 {
		t.Fatalf("after sweep %d bytes (%+v)", res.TotalAfter, res)
	}
	if exists(filepath.Join(base, "v0")) || exists(filepath.Join(base, "tmp", "x.zip")) {
		t.Error("oldest (v0, tmp) not removed first")
	}
	if !exists(filepath.Join(base, "v1", "avd-a", "seg-00000000000000000201.jsonl")) {
		t.Error("newest segment of a current device removed")
	}
	// with an impossible cap everything but the newest current segment goes
	Sweep(SweepOptions{Base: base, MaxTotal: 1, Now: now})
	if segs := store.ListSegments(filepath.Join(base, "v1", "avd-a")); len(segs) != 1 {
		t.Errorf("avd-a has %d segments", len(segs))
	}
	for _, p := range ignored {
		if !exists(p) {
			t.Errorf("old-layout file %s was touched", p)
		}
	}
}

func TestSweepSkipsLiveWriter(t *testing.T) {
	base := t.TempDir()
	old := time.Now().Add(-72 * time.Hour)
	devDir(t, filepath.Join(base, "v0", "dev"), 2, 100, old) // an old version records it
	f, ok := store.TryLock(filepath.Join(base, "v0", "dev", "writer.lock"))
	if !ok {
		t.Fatal("lock")
	}
	defer store.ReleaseLock(f)
	Sweep(SweepOptions{Base: base, MaxAge: time.Hour, MaxTotal: 1})
	if !exists(filepath.Join(base, "v0/dev/seg-00000000000000000001.jsonl")) {
		t.Error("removed although written by a live process")
	}
}

// owners/ of 0.4 development builds (per-device owner locks) is cleaned up:
// unheld lock files, then the directory.
func TestSweepRemovesOldOwnersDir(t *testing.T) {
	base := t.TempDir()
	owners := filepath.Join(base, "owners")
	write(t, filepath.Join(owners, "crashed.lock"), 20, time.Now().Add(-2*time.Minute))
	live, ok := store.TryLock(filepath.Join(owners, "live.lock"))
	if !ok {
		t.Fatal("lock")
	}
	res := Sweep(SweepOptions{Base: base})
	if res.StaleOwners != 1 || exists(filepath.Join(owners, "crashed.lock")) || !exists(filepath.Join(owners, "live.lock")) {
		t.Fatalf("stale owners %d", res.StaleOwners)
	}
	store.ReleaseLock(live)
	Sweep(SweepOptions{Base: base})
	if exists(owners) {
		t.Fatal("empty owners/ not removed")
	}
}

// bin/ keeps the running service's version plus the newest other one;
// bin/ and log/ count towards the total.
func TestSweepBinKeepsRunningAndNewest(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	now := time.Now()
	mk := func(name string, age time.Duration) string {
		d := filepath.Join(bin, name)
		write(t, filepath.Join(d, "android-log-mcp"), 1000, now.Add(-age))
		os.Chtimes(d, now.Add(-age), now.Add(-age))
		return d
	}
	running := mk("0.4.0-aaa", 5*time.Hour) // oldest, but running
	mk("0.4.1-bbb", 3*time.Hour)
	newest := mk("0.4.2-ccc", time.Hour)
	mk("0.3.9-ddd", 4*time.Hour)
	os.WriteFile(filepath.Join(bin, "0.3.9-ddd", "unknown.txt"), []byte("x"), 0o644) // never deleted
	write(t, filepath.Join(base, "log", "service.log"), 500, now)
	res := Sweep(SweepOptions{Base: base, KeepBin: running})
	if !exists(running) || !exists(newest) || exists(filepath.Join(bin, "0.4.1-bbb")) {
		t.Fatalf("bin after sweep: running %v newest %v", exists(running), exists(newest))
	}
	if exists(filepath.Join(bin, "0.3.9-ddd", "android-log-mcp")) || !exists(filepath.Join(bin, "0.3.9-ddd", "unknown.txt")) {
		t.Fatal("unknown file deleted or known binary kept")
	}
	if res.OldBins != 1 { // 0.3.9-ddd still has the unknown file
		t.Fatalf("removed %d bin dirs", res.OldBins)
	}
	if got := TotalBytes(base); got != 1000+1000+1+500 {
		t.Fatalf("TotalBytes %d (bin/ and log/ must count)", got)
	}
}

func TestSweepRunStaleSockets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets in run/ are a unix feature here")
	}
	dir, err := os.MkdirTemp("", "almrun") // short path: sun_path is ~104 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	live := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	stale := filepath.Join(dir, "stale.sock")
	ln2, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln2.(*net.UnixListener).SetUnlinkOnClose(false)
	ln2.Close() // file stays, nobody listens
	if !exists(stale) {
		t.Fatal("setup: stale socket file missing")
	}
	js := func(name string, st RunState) {
		b, _ := json.Marshal(st)
		os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	js("live.json", RunState{PID: os.Getpid(), Socket: live})
	js("deadpid.json", RunState{PID: 1 << 30})
	js("deadsock.json", RunState{PID: os.Getpid(), Socket: stale})
	write(t, filepath.Join(dir, "old.pid"), 3, time.Now().Add(-48*time.Hour))

	n := SweepRun(dir, 24*time.Hour, time.Now())
	for _, p := range []string{"stale.sock", "deadpid.json", "deadsock.json", "old.pid"} {
		if exists(filepath.Join(dir, p)) {
			t.Errorf("%s not swept", p)
		}
	}
	for _, p := range []string{"live.sock", "live.json"} {
		if !exists(filepath.Join(dir, p)) {
			t.Errorf("%s removed although live", p)
		}
	}
	if n != 4 {
		t.Errorf("removed %d entries, want 4", n)
	}
}

// run/wanted.json (issue #226) is the carry for the successor of a crashed or
// replaced service: its writer is dead exactly when the sweep runs, and it has
// no pid key. SweepRun must keep it (KeepRun) and still remove a leftover
// wanted.json.tmp by age and the unrelated state files as before.
func TestSweepRunKeepsWantedJSON(t *testing.T) {
	dir := t.TempDir()
	wanted := `{"writer":{"pid":1073741824,"start_time":1},"written_at":1792000000000,"devices":{"avd-X":{"idle_ms":1,"linger_ms":-1}}}`
	os.WriteFile(filepath.Join(dir, "wanted.json"), []byte(wanted), 0o600)
	// old, unreadable and empty: still kept (it is not an endpoint file)
	os.WriteFile(filepath.Join(dir, "record.json"), []byte("{}"), 0o600)
	os.Chtimes(filepath.Join(dir, "wanted.json"), time.Now().Add(-72*time.Hour), time.Now().Add(-72*time.Hour))
	write(t, filepath.Join(dir, "wanted.json.tmp"), 3, time.Now().Add(-48*time.Hour))
	os.WriteFile(filepath.Join(dir, "gone.json"), []byte(`{"pid":1073741824}`), 0o600)

	SweepRun(dir, 24*time.Hour, time.Now())
	if b, err := os.ReadFile(filepath.Join(dir, "wanted.json")); err != nil || string(b) != wanted {
		t.Fatalf("wanted.json swept or changed: %v %s", err, b)
	}
	if exists(filepath.Join(dir, "wanted.json.tmp")) {
		t.Error("an old wanted.json.tmp was not swept")
	}
	if exists(filepath.Join(dir, "gone.json")) {
		t.Error("a state file of a dead pid was not swept")
	}
	// the same file is also kept when the sweep sees it as unreadable garbage
	os.WriteFile(filepath.Join(dir, "wanted.json"), []byte("{garbage"), 0o600)
	os.Chtimes(filepath.Join(dir, "wanted.json"), time.Now().Add(-72*time.Hour), time.Now().Add(-72*time.Hour))
	SweepRun(dir, 24*time.Hour, time.Now())
	if !exists(filepath.Join(dir, "wanted.json")) {
		t.Error("wanted.json swept")
	}
}
