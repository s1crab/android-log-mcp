package service

// run/wanted.json (issue #226, design 3.3 and test plan 7.1 cases 1, 9-16).
// Conditions are awaited with waitUntil (own budget per stage, diagnostics on
// timeout); file failures are injected through WantedTiming.Rename/Remove,
// never by really locking files, so every test runs the same on all OSes. A
// service in these tests runs inside the test process, so the writer pid of a
// file that a successor must accept is a helper process that has exited.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/s1crab/android-log-mcp/server/internal/device"
	"github.com/s1crab/android-log-mcp/server/internal/procattr"
)

type sleeper struct {
	pid   int
	start int64
	cmd   *exec.Cmd
	done  chan struct{}
}

// startSleeper runs this test binary as a process that stays alive until it
// is killed (ALM_TEST_SLEEP in TestMain).
func startSleeper(t *testing.T) *sleeper {
	t.Helper()
	cmd := procattr.Hide(exec.Command(os.Args[0]))
	cmd.Env = append(os.Environ(), "ALM_TEST_SLEEP=1")
	pr, pw, _ := os.Pipe()
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	s := &sleeper{pid: cmd.Process.Pid, cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); close(s.done) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-s.done })
	ready := make(chan struct{})
	go func() { bufio.NewReader(pr).ReadString('\n'); close(ready) }()
	waitDone(t, "the helper process to start", 30*time.Second, ready)
	s.start, _, _ = inspectProc(s.pid)
	return s
}

func (s *sleeper) kill(t *testing.T) {
	t.Helper()
	s.cmd.Process.Kill()
	waitDone(t, "the helper process to exit", 30*time.Second, s.done)
}

// deadWriter is the identity of a service process that no longer exists.
func deadWriter(t *testing.T) wantedWriterID {
	t.Helper()
	s := startSleeper(t)
	id := wantedWriterID{PID: s.pid, StartTime: s.start}
	s.kill(t)
	return id
}

func writeWantedFile(t *testing.T, ctrl Control, f wantedFile) {
	t.Helper()
	if err := os.MkdirAll(ctrl.Run(), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	if err := os.WriteFile(ctrl.WantedPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func wantedAt(w wantedWriterID, at time.Time, devs map[string]wantedDevice) wantedFile {
	return wantedFile{Writer: w, WrittenAt: at.UnixMilli(), Devices: devs}
}

func callStatus(t *testing.T, ctrl Control, version, dev string) string {
	t.Helper()
	c, err := Dial(context.Background(), ctrl, version, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var r CallReply
	args := []byte(`{"device":"` + dev + `"}`)
	if err := c.Do(context.Background(), "call", CallArgs{Name: "logcat_status", Args: args}, &r); err != nil || r.IsError {
		t.Fatalf("logcat_status %s: %v %s", dev, err, r.Text)
	}
	return r.Text
}

func readWantedT(t *testing.T, ctrl Control) *wantedFile {
	t.Helper()
	f, err := readWanted(ctrl.WantedPath())
	if err != nil {
		t.Fatalf("wanted.json: %v", err)
	}
	return f
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

const hour = time.Hour

// ---- pure plan (clock jumps, TTL, linger) ----

func TestPlanRestore(t *testing.T) {
	now := time.Now()
	f := func(writtenAgo, idle, linger time.Duration) *wantedFile {
		x := wantedAt(wantedWriterID{PID: 1}, now.Add(-writtenAgo), map[string]wantedDevice{"D": {IdleMS: idle.Milliseconds(), LingerMS: linger.Milliseconds()}})
		return &x
	}
	forever := &wantedFile{WrittenAt: now.Add(-time.Minute).UnixMilli(), Devices: map[string]wantedDevice{"D": {IdleMS: 1000, LingerMS: -1}}}
	cases := []struct {
		name string
		f    *wantedFile
		want bool
		left time.Duration // approximate remaining
	}{
		{"fresh", f(time.Minute, 2*time.Minute, 30*time.Minute), true, 27 * time.Minute},
		{"linger over while down", f(10*time.Minute, 25*time.Minute, 30*time.Minute), false, 0},
		{"linger zero", f(0, 0, 0), false, 0},
		{"past the TTL", f(LingerMax+time.Minute, 0, 24*hour), false, 0},
		{"forward clock jump (a day)", f(24*hour, 0, 30*time.Minute), false, 0},
		{"backward clock jump (written a day ahead)", f(-24*hour, 5*time.Minute, 30*time.Minute), true, 25 * time.Minute},
		{"forever", forever, true, -1},
	}
	for _, c := range cases {
		items := planRestore(c.f, now, LingerMax)
		if (len(items) == 1) != c.want {
			t.Fatalf("%s: items %+v, want restored=%v", c.name, items, c.want)
		}
		if !c.want {
			continue
		}
		it := items[0]
		if it.Last.After(now) {
			t.Fatalf("%s: last in the future: %v", c.name, it.Last)
		}
		if c.left >= 0 && (it.Left > c.left+time.Second || it.Left < c.left-time.Second) {
			t.Fatalf("%s: left %s, want about %s (never more than the linger written)", c.name, it.Left, c.left)
		}
		if c.left < 0 && it.Left >= 0 {
			t.Fatalf("%s: forever expected, left %s", c.name, it.Left)
		}
	}
}

// ---- writer ----

func TestWantedFileWrittenForCalledDevice(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	startTest(t, ctrl, "0.4.0", withADB, func(o *Options) { o.Wanted.Debounce = 20 * time.Millisecond })
	callStatus(t, ctrl, "0.4.0", "FAKEA")
	var f *wantedFile
	waitUntil(t, "wanted.json with FAKEA", 30*time.Second, func() bool {
		f, _ = readWanted(ctrl.WantedPath())
		return f != nil && len(f.Devices) == 1
	})
	if f.Writer.PID != os.Getpid() || f.WrittenAt <= 0 {
		t.Fatalf("writer %+v written_at %d", f.Writer, f.WrittenAt)
	}
	if st, _, _ := inspectProc(os.Getpid()); f.Writer.StartTime != st {
		t.Fatalf("writer start_time %d, want %d", f.Writer.StartTime, st)
	}
	d := f.Devices["FAKEA"]
	if d.IdleMS < 0 || d.LingerMS <= 0 {
		t.Fatalf("device entry %+v", d)
	}
	// no top-level pid key (layout.SweepRun would read the file as an endpoint)
	b, _ := os.ReadFile(ctrl.WantedPath())
	var raw map[string]json.RawMessage
	json.Unmarshal(b, &raw)
	if _, bad := raw["pid"]; bad {
		t.Fatalf("top-level pid in %s", b)
	}
	if fileExists(ctrl.WantedPath() + ".tmp") {
		t.Fatal("temp file left behind")
	}
}

// A pin-only device (no log tool call) is not written: pins are re-applied
// by startPinned.
func TestWantedFileSkipsPinOnlyDevice(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "PINONLY", "CALLED")
	m := startTest(t, testControl(t), "0.4.0", withADB).Manager()
	if err := m.Want("PINONLY", false); err != nil {
		t.Fatal(err)
	}
	if got := m.WantInfos(time.Now()); len(got) != 0 {
		t.Fatalf("pin-only device listed: %+v", got)
	}
	if err := m.Want("CALLED", true); err != nil {
		t.Fatal(err)
	}
	if got := m.WantInfos(time.Now()); len(got) != 1 || got["CALLED"].Linger == 0 {
		t.Fatalf("infos: %+v", got)
	}
}

func TestWantedFileRapidChangesWriteBounded(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA", "FAKEB")
	ctrl := testControl(t)
	var renames atomic.Int32
	s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) {
		o.Wanted.Debounce = 150 * time.Millisecond
		o.Wanted.Rename = func(a, b string) error { renames.Add(1); return os.Rename(a, b) }
	})
	m := s.Manager()
	for i := 0; i < 20; i++ { // a burst of set changes and call refreshes
		m.Want("FAKEA", true)
		m.Want("FAKEB", true)
		m.Unwant("FAKEB", "test")
		end := m.Begin("FAKEA")
		end()
	}
	waitUntil(t, "the burst to be written", 30*time.Second, func() bool {
		f, _ := readWanted(ctrl.WantedPath())
		return f != nil && len(f.Devices) == 1 && f.Devices["FAKEA"].LingerMS > 0
	})
	if n := renames.Load(); n < 1 || n > 3 {
		t.Fatalf("a burst of 80 changes caused %d writes (debounce 150 ms)", n)
	}
}

// A call that only refreshes "last" rewrites the file at most once per Refresh.
func TestWantedFileRefreshIsRateLimited(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	var renames atomic.Int32
	s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) {
		o.Wanted.Debounce = 10 * time.Millisecond
		o.Wanted.Refresh = time.Hour
		o.Wanted.Rename = func(a, b string) error { renames.Add(1); return os.Rename(a, b) }
	})
	m := s.Manager()
	m.Want("FAKEA", true)
	waitUntil(t, "the first write", 30*time.Second, func() bool { return renames.Load() == 1 })
	for i := 0; i < 50; i++ {
		m.Begin("FAKEA")()
	}
	// the negative observation: a write would be armed Debounce after the
	// calls; a set change (the barrier) then proves the timer machinery works
	m.Want("FAKEB", true) // unknown device: FAKEB is not online, still a set change
	waitUntil(t, "the set change to be written", 30*time.Second, func() bool { return renames.Load() == 2 })
	if n := renames.Load(); n != 2 {
		t.Fatalf("refresh calls were written: %d writes", n)
	}
}

// ---- restore ----

func TestWantedRestoredAfterHandoffAndCrash(t *testing.T) {
	t.Parallel()
	e, withADB := installFake(t, "FAKEA", "FAKEB")
	_ = e
	ctrl := testControl(t)
	old := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) { o.Wanted.Debounce = 10 * time.Millisecond })
	callStatus(t, ctrl, "0.4.0", "FAKEA")
	callStatus(t, ctrl, "0.4.0", "FAKEB")
	waitUntil(t, "both devices in wanted.json", 30*time.Second, func() bool {
		f, _ := readWanted(ctrl.WantedPath())
		return f != nil && len(f.Devices) == 2
	})
	// a handoff (and a signal, a crash up to its last write) leaves the file
	old.Stop("replaced by a newer MCP or CLI process (test)")
	<-old.Done()
	f := readWantedT(t, ctrl)
	if f == nil || len(f.Devices) != 2 {
		t.Fatalf("handoff removed or emptied the file: %+v", f)
	}
	// the writer here is this test process: a real successor runs in another
	// one, so the replaced holder's identity is a process that is gone
	f.Writer = deadWriter(t)
	writeWantedFile(t, ctrl, *f)
	s := startTest(t, ctrl, "0.4.1", withADB, func(o *Options) { o.CatchUpWait = 300 * time.Millisecond })
	waitUntil(t, "both devices resumed", 30*time.Second, func() bool { return s.Manager().Wanted("FAKEA") && s.Manager().Wanted("FAKEB") })
	// the "device idle but within linger, a client for another device starts"
	// case (design 7.1 1c-bis): a call for a third device changes nothing for these
	txt := callStatus(t, ctrl, "0.4.1", "FAKEA")
	if strings.Contains(txt, "recording: idle") {
		t.Fatalf("first status after the restart:\n%s", txt)
	}
	// the restored last is never newer than now and keeps the idle time written
	if last := s.Manager().LastCall("FAKEB"); last.IsZero() || last.After(time.Now()) {
		t.Fatalf("restored last %v", last)
	}
}

func TestWantedColdStartRestoresNothing(t *testing.T) {
	t.Parallel()
	e, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	s := startTest(t, ctrl, "0.4.0", withADB)
	if !s.Manager().Idle() {
		t.Fatal("cold start is not idle")
	}
	if c := e.Calls(); len(c) != 0 {
		t.Fatalf("an idle service ran adb: %v", c)
	}
}

func TestWantedRestoreFilters(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA", "FAKEB", "FAKEC")
	ctrl := testControl(t)
	w := deadWriter(t)
	now := time.Now()
	writeWantedFile(t, ctrl, wantedAt(w, now.Add(-time.Minute), map[string]wantedDevice{
		"FAKEA": {IdleMS: 1000, LingerMS: (30 * time.Minute).Milliseconds()},                              // within its linger
		"FAKEB": {IdleMS: (25 * time.Minute).Milliseconds(), LingerMS: (10 * time.Minute).Milliseconds()}, // past it
		"FAKEC": {IdleMS: 1000, LingerMS: -1},                                                             // forever
	}))
	s := startTest(t, ctrl, "0.4.0", withADB)
	waitUntil(t, "FAKEA and FAKEC resumed", 30*time.Second, func() bool { return s.Manager().Wanted("FAKEA") && s.Manager().Wanted("FAKEC") })
	if s.Manager().Wanted("FAKEB") {
		t.Fatal("a device past its linger was resumed")
	}
	// the idle time written (1 s) plus the downtime (1 min) is charged to the
	// service's own linger (default 10 min here), never more than the file's
	if got := s.Manager().IdleIn("FAKEA", time.Now()); got > 9*time.Minute || got < 8*time.Minute {
		t.Fatalf("remaining linger of FAKEA: %s", got)
	}
}

func TestWantedExpiredFileRestoresNothing(t *testing.T) {
	t.Parallel()
	e, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	writeWantedFile(t, ctrl, wantedAt(deadWriter(t), time.Now().Add(-2*hour), map[string]wantedDevice{"FAKEA": {IdleMS: 0, LingerMS: -1}}))
	s := startTest(t, ctrl, "0.4.0", withADB)
	// a forward jump past the TTL: the restore goroutine ends without a device
	s.restoreWG.Wait()
	if !s.Manager().Idle() || len(e.Calls()) != 0 {
		t.Fatalf("expired file resumed a device: idle=%v calls=%v", s.Manager().Idle(), e.Calls())
	}
}

func TestWantedLingerZeroDisablesRestore(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	writeWantedFile(t, ctrl, wantedAt(deadWriter(t), time.Now(), map[string]wantedDevice{"FAKEA": {IdleMS: 0, LingerMS: -1}}))
	s := startTest(t, ctrl, "0.4.0", withADB, lingerEnv("0"))
	s.restoreWG.Wait()
	if s.Manager().Wanted("FAKEA") {
		t.Fatal("ANDROID_LOG_MCP_LINGER=0 resumed a device")
	}
}

func TestWantedBackwardClockJump(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	linger := 30 * time.Minute
	writeWantedFile(t, ctrl, wantedAt(deadWriter(t), time.Now().Add(24*hour), map[string]wantedDevice{"FAKEA": {IdleMS: (5 * time.Minute).Milliseconds(), LingerMS: linger.Milliseconds()}}))
	s := startTest(t, ctrl, "0.4.0", withADB)
	waitUntil(t, "FAKEA resumed", 30*time.Second, func() bool { return s.Manager().Wanted("FAKEA") })
	if got := s.Manager().IdleIn("FAKEA", time.Now()); got > linger {
		t.Fatalf("remaining %s exceeds the linger written (%s)", got, linger)
	}
}

// Ownership during handoff (design 7.1 case 12).
func TestWantedOwnership(t *testing.T) {
	dev := map[string]wantedDevice{"FAKEA": {IdleMS: 0, LingerMS: -1}}
	t.Run("writer pid reused", func(t *testing.T) {
		t.Parallel()
		sl := startSleeper(t)
		if sl.start == 0 {
			t.Skip("no process start time on this OS: only the TTL applies")
		}
		_, withADB := installFake(t, "FAKEA")
		ctrl := testControl(t)
		writeWantedFile(t, ctrl, wantedAt(wantedWriterID{PID: sl.pid, StartTime: sl.start + 12345}, time.Now(), dev))
		s := startTest(t, ctrl, "0.4.0", withADB)
		waitUntil(t, "FAKEA resumed (a reused pid is a dead writer)", 30*time.Second, func() bool { return s.Manager().Wanted("FAKEA") })
	})
	t.Run("writer exits within WriterExitWait", func(t *testing.T) {
		t.Parallel()
		sl := startSleeper(t)
		if sl.start == 0 {
			t.Skip("no process start time on this OS")
		}
		_, withADB := installFake(t, "FAKEA")
		ctrl := testControl(t)
		writeWantedFile(t, ctrl, wantedAt(wantedWriterID{PID: sl.pid, StartTime: sl.start}, time.Now(), dev))
		s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) { o.Wanted.WriterExitWait = 60 * time.Second })
		// Start returned while the writer is alive: not resumed yet, the
		// service answers (Start is not delayed)
		if s.Manager().Wanted("FAKEA") {
			t.Fatal("resumed while the writer still ran")
		}
		c, err := Dial(context.Background(), ctrl, "0.4.0", "cli") // answers hello
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		sl.kill(t)
		waitUntil(t, "FAKEA resumed after the writer exited", 30*time.Second, func() bool { return s.Manager().Wanted("FAKEA") })
	})
	t.Run("writer alive and unrelated", func(t *testing.T) {
		t.Parallel()
		sl := startSleeper(t)
		if sl.start == 0 {
			t.Skip("no process start time on this OS")
		}
		_, withADB := installFake(t, "FAKEA")
		ctrl := testControl(t)
		writeWantedFile(t, ctrl, wantedAt(wantedWriterID{PID: sl.pid, StartTime: sl.start}, time.Now(), dev))
		before, _ := os.ReadFile(ctrl.WantedPath())
		s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) { o.Wanted.WriterExitWait = 100 * time.Millisecond })
		s.restoreWG.Wait()
		if s.Manager().Wanted("FAKEA") {
			t.Fatal("the file of a live unrelated process was used")
		}
		if after, _ := os.ReadFile(ctrl.WantedPath()); string(after) != string(before) {
			t.Fatalf("the file was touched:\n%s\n%s", before, after)
		}
	})
	t.Run("writer is this process", func(t *testing.T) {
		t.Parallel()
		_, withADB := installFake(t, "FAKEA")
		ctrl := testControl(t)
		st, _, _ := inspectProc(os.Getpid())
		writeWantedFile(t, ctrl, wantedAt(wantedWriterID{PID: os.Getpid(), StartTime: st}, time.Now(), dev))
		s := startTest(t, ctrl, "0.4.0", withADB)
		s.restoreWG.Wait()
		if s.Manager().Wanted("FAKEA") {
			t.Fatal("a file written by this process was used")
		}
	})
}

// Garbage and unknown keys (design 7.1 case 9).
func TestWantedGarbageAndUnknownKeys(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	os.MkdirAll(ctrl.Run(), 0o700)
	for _, junk := range []string{"{not json", `{"writer":{"pid":0},"written_at":0}`, ""} {
		os.WriteFile(ctrl.WantedPath(), []byte(junk), 0o600)
		o := Options{Control: ctrl, Version: "0.4.0", Drain: time.Second}
		withADB(&o)
		s, err := Start(o)
		if err != nil {
			t.Fatal(err)
		}
		s.restoreWG.Wait()
		if !s.Manager().Idle() {
			t.Fatalf("garbage %q resumed something", junk)
		}
		s.Stop("test")
	}
	w := deadWriter(t)
	raw := `{"writer":{"pid":` + strconv.Itoa(w.PID) + `,"start_time":` + strconv.FormatInt(w.StartTime, 10) + `,"future":1},"written_at":` + strconv.FormatInt(time.Now().UnixMilli(), 10) +
		`,"extra":{"a":1},"devices":{"FAKEA":{"idle_ms":0,"linger_ms":-1,"new":true}}}`
	os.WriteFile(ctrl.WantedPath(), []byte(raw), 0o600)
	s := startTest(t, ctrl, "0.4.0", withADB)
	waitUntil(t, "FAKEA resumed from a file with unknown keys", 30*time.Second, func() bool { return s.Manager().Wanted("FAKEA") })
}

// ErrMaxDevices during a restore is returned and logged, never fatal, and the
// restore does not override a fresher call (design 7.1 1j, 15: max merge).
func TestWantedMaxDevicesAndMerge(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA", "FAKEB")
	s := startTest(t, testControl(t), "0.4.0", withADB)
	m := s.Manager()
	m.MaxDevices = 1
	m.Want("FAKEA", true)
	var max *device.ErrMaxDevices
	if err := m.RestoreWant("FAKEB", time.Now(), time.Time{}); !errors.As(err, &max) {
		t.Fatalf("RestoreWant over the limit: %v", err)
	}
	if m.Wanted("FAKEB") {
		t.Fatal("FAKEB started over the limit")
	}
	fresh := m.LastCall("FAKEA")
	m.RestoreWant("FAKEA", fresh.Add(-time.Hour), time.Time{})
	if got := m.LastCall("FAKEA"); !got.Equal(fresh) {
		t.Fatalf("an older restore moved last: %v -> %v", fresh, got)
	}
	m.RestoreWant("FAKEA", fresh.Add(time.Minute), time.Time{})
	if got := m.LastCall("FAKEA"); !got.Equal(fresh.Add(time.Minute)) {
		t.Fatalf("a newer restore was ignored: %v", got)
	}
}

// ---- stop kinds (design 7.1 case 1g, 13) ----

func wantedWritten(t *testing.T, ctrl Control) {
	t.Helper()
	waitUntil(t, "wanted.json with a device", 30*time.Second, func() bool {
		f, _ := readWanted(ctrl.WantedPath())
		return f != nil && len(f.Devices) > 0
	})
}

func TestWantedClearedByStopOp(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) { o.Wanted.Debounce = 10 * time.Millisecond })
	callStatus(t, ctrl, "0.4.0", "FAKEA")
	wantedWritten(t, ctrl)
	c, err := Dial(context.Background(), ctrl, "0.4.0", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), "stop", nil, nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	waitDone(t, "the service to stop", 30*time.Second, s.Done())
	if fileExists(ctrl.WantedPath()) {
		t.Fatal("service stop left wanted.json")
	}
}

func TestWantedForceStopClearsAndSignalKeeps(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) { o.Wanted.Debounce = 10 * time.Millisecond })
	callStatus(t, ctrl, "0.4.0", "FAKEA")
	wantedWritten(t, ctrl)
	s.Stop("signal terminated")
	if !fileExists(ctrl.WantedPath()) {
		t.Fatal("a SIGTERM stop removed wanted.json")
	}
	ClearWanted(ctrl) // what `service stop --force` does after StopHolder
	if fileExists(ctrl.WantedPath()) {
		t.Fatal("ClearWanted left the file")
	}
	ClearWanted(ctrl) // and is fine when there is none
}

func TestWantedRemovedByIdleExit(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	// linger 0: the device stops right after the call; the service then exits idle
	s := startTest(t, ctrl, "0.4.0", withADB, lingerEnv("0"), exitAfter(300*time.Millisecond), func(o *Options) { o.Wanted.Debounce = 10 * time.Millisecond })
	callStatus(t, ctrl, "0.4.0", "FAKEA")
	waitDone(t, "the idle exit", 60*time.Second, s.Done())
	if fileExists(ctrl.WantedPath()) {
		t.Fatalf("idle exit left wanted.json:\n%s", ctrlDiag(ctrl))
	}
}

// A debounced write is in flight (blocked in its rename) when the idle exit
// happens: the final removal is ordered after it and nothing recreates the file.
func TestWantedIdleExitOrderedAfterPendingWrite(t *testing.T) {
	t.Parallel()
	for i := 0; i < 5; i++ {
		_, withADB := installFake(t, "FAKEA")
		ctrl := testControl(t)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) {
			o.Wanted.Debounce = 5 * time.Millisecond
			o.Wanted.Rename = func(a, b string) error {
				once.Do(func() { close(entered) })
				<-release
				return os.Rename(a, b)
			}
		})
		s.Manager().Want("FAKEA", true)
		waitDone(t, "the write to be in flight", 30*time.Second, entered)
		s.clearWanted.Store(true)
		go s.Stop("idle exit (test)")
		close(release)
		waitDone(t, "Stop", 30*time.Second, s.Done())
		if fileExists(ctrl.WantedPath()) || fileExists(ctrl.WantedPath()+".tmp") {
			t.Fatalf("round %d: the pending write recreated wanted.json\n%s", i, ctrlDiag(ctrl))
		}
	}
}

// An idle exit whose removal fails leaves a file that restores nothing: every
// device in it is past its linger (design 7.1 case 13).
func TestWantedIdleExitRemoveFailureIsHarmless(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	var removes atomic.Int32
	s := startTest(t, ctrl, "0.4.0", withADB, lingerEnv("0"), func(o *Options) {
		o.Wanted.Debounce = 10 * time.Millisecond
		o.Wanted.FinalGap = time.Millisecond
		o.Wanted.Remove = func(string) error { removes.Add(1); return errors.New("sharing violation (injected)") }
	})
	callStatus(t, ctrl, "0.4.0", "FAKEA")
	wantedWritten(t, ctrl)
	s.clearWanted.Store(true)
	s.Stop("idle exit (test)")
	if removes.Load() < 1 || !fileExists(ctrl.WantedPath()) {
		t.Fatalf("removes=%d exists=%v", removes.Load(), fileExists(ctrl.WantedPath()))
	}
	f := readWantedT(t, ctrl)
	for _, it := range planRestore(f, time.Now().Add(time.Second), LingerMax) {
		if d := f.Devices[it.ID]; d.LingerMS != 0 {
			t.Fatalf("device %s with linger %d would be resumed from the leftover", it.ID, d.LingerMS)
		}
	}
	// and a successor (new service, same ctrl) with a dead writer resumes nothing
	f.Writer = deadWriter(t)
	writeWantedFile(t, ctrl, *f)
	s2 := startTest(t, ctrl, "0.4.1", withADB, lingerEnv("0"))
	s2.restoreWG.Wait()
	if !s2.Manager().Idle() {
		t.Fatal("a leftover of an idle exit resumed a device")
	}
}

// ---- Windows rename tolerance (design 7.1 case 14), injected ----

func TestWantedRenameRetries(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	var calls atomic.Int32
	const failures = 3
	s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) {
		o.Wanted.Debounce = 5 * time.Millisecond
		o.Wanted.RetryBase, o.Wanted.RetryMax = 5*time.Millisecond, 20*time.Millisecond
		o.Wanted.Rename = func(a, b string) error {
			if calls.Add(1) <= failures {
				if !fileExists(a) {
					return errors.New("temp file missing")
				}
				return errors.New("The process cannot access the file because it is being used by another process. (injected)")
			}
			return os.Rename(a, b)
		}
	})
	s.Manager().Want("FAKEA", true)
	waitUntil(t, "the content to converge after the failed renames", 30*time.Second, func() bool {
		f, _ := readWanted(ctrl.WantedPath())
		return f != nil && len(f.Devices) == 1
	})
	if n := calls.Load(); n < failures+1 {
		t.Fatalf("%d rename attempts", n)
	}
	if fileExists(ctrl.WantedPath() + ".tmp") {
		t.Fatal("a failed rename left its temp file")
	}
}

// Stop gives up after FinalTries x FinalGap without leaving a temp file.
func TestWantedFinalFlushIsBounded(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA")
	ctrl := testControl(t)
	var calls atomic.Int32
	s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) {
		o.Wanted.Debounce = time.Hour // only the final flush writes
		o.Wanted.FinalTries, o.Wanted.FinalGap = 5, time.Millisecond
		o.Wanted.Rename = func(a, b string) error { calls.Add(1); return errors.New("sharing violation (injected)") }
	})
	s.Manager().Want("FAKEA", true)
	s.Stop("signal (test)")
	if n := calls.Load(); n != 5 {
		t.Fatalf("final flush attempts: %d, want 5", n)
	}
	if fileExists(ctrl.WantedPath()+".tmp") || fileExists(ctrl.WantedPath()) {
		t.Fatalf("files left:\n%s", ctrlDiag(ctrl))
	}
}
