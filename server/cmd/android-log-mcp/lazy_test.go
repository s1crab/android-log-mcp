package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/s1crab/android-log-mcp/server/internal/fakeadb"
)

// lazyEnv is an isoEnv with the scriptable fake adb (internal/fakeadb) and
// one online device FAKEA whose buffer already holds a line.
func lazyEnv(t *testing.T, extra ...string) ([]string, fakeadb.Env) {
	t.Helper()
	dir := t.TempDir()
	adb, err := fakeadb.Install(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := fakeadb.Env{Dir: dir}
	e.AddDevice("FAKEA", "boot-a")
	e.Log("FAKEA", fakeadb.Line(time.Now().Add(-time.Minute), 100, "PRE", "before the first call"))
	e.SetDevices(map[string]string{"FAKEA": "device"})
	// runs before TempDir's removal: orphaned fake logcats exit once their
	// device directory is gone
	diagFor(t, func() string {
		return fmt.Sprintf("fake adb calls (tail):\n%s\nany child running: %v\n", e.CallsTail(60), e.AnyChildRunning())
	})
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(dir, "dev"))
		// wait for the orphans to notice instead of sleeping; bounded, and
		// not a failure (TempDir removal reports a real problem)
		deadline := time.Now().Add(scaled(30 * time.Second))
		for e.AnyChildRunning() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	})
	env := isoEnv(t, append([]string{"ANDROID_LOG_MCP_ADB=" + adb, fakeadb.EnvDir + "=" + dir}, extra...)...)
	return env, e
}

func (s *session) call(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v\nstderr:\n%s", name, err, s.err.String())
	}
	return res.Content[0].(*mcp.TextContent).Text
}

// eventually is waitUntil with a floor of 30 s per stage: the callers' short
// budgets were tuned for fast Linux and flaked on starved runners.
func eventually(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	waitUntil(t, what, max(d, 30*time.Second), ok)
}

// An MCP client connects: the service runs, but runs no adb until the
// first log tool call, which backfills what the buffer held. With a short
// linger the device's logcat stops again while the client stays connected,
// and the service stays.
func TestLazyRecording(t *testing.T) {
	t.Parallel()
	env, e := lazyEnv(t, "ANDROID_LOG_MCP_LINGER=2s")
	s := startMCP(t, env)
	pid := statePID(t, env)
	if pid == 0 {
		t.Fatal("no service")
	}
	// no sleep: the service is up (statePID) and the checks below follow
	// synchronous round trips to it, so a late adb start would show here
	if c := e.Calls(); len(c) != 0 {
		t.Fatalf("adb run before the first log tool call: %q", c)
	}
	if out, _, _ := cliEnv(t, env, "service", "status"); !strings.HasPrefix(out, "ok service running=1 pid="+strconv.Itoa(pid)+" ") || strings.Count(out, "\n") != 2 || !strings.Contains(strings.SplitN(out, "\n", 2)[0], " recording=") {
		t.Fatalf("service status (default):\n%s", out)
	}
	if out, _, _ := cliEnv(t, env, "service", "status", "--verbose"); !strings.Contains(out, "recording: idle") {
		t.Fatalf("service status:\n%s", out)
	}
	if c := e.Calls(); len(c) != 0 {
		t.Fatalf("service status ran adb: %q", c)
	}
	out := s.call(t, "logcat_search", map[string]any{"filter": "before the first call"})
	if !strings.Contains(out, "before the first call") {
		t.Fatalf("first search did not backfill:\n%s", out)
	}
	if !e.LogcatRunning("FAKEA") {
		t.Fatal("logcat not running after the first call")
	}
	eventually(t, "logcat stops after the 2s linger", 20*time.Second, func() bool { return !e.LogcatRunning("FAKEA") })
	if !processAlive(pid) {
		t.Fatal("service exited while a client was connected")
	}
	e.Log("FAKEA", fakeadb.Line(time.Now(), 101, "IDLE", "while idle"))
	out = s.call(t, "logcat_search", map[string]any{"filter": "while idle"}) // starts it again
	if !strings.Contains(out, "while idle") || strings.Count(s.call(t, "logcat_search", map[string]any{"filter": "before the first call"}), "before the first call") != 1 {
		t.Fatalf("resume:\n%s", out)
	}
	var resumed bool
	for _, c := range e.CallsMatching("logcat") {
		resumed = resumed || (strings.Contains(c, " -T ") && !strings.Contains(c, " -d"))
	}
	if !resumed {
		t.Fatalf("not resumed with -T: %q", e.CallsMatching("logcat"))
	}
}

// record --for with no service spawns it detached; recording runs without
// clients until the pin ends, then the service exits.
func TestRecordForWithoutService(t *testing.T) {
	t.Parallel()
	env, e := lazyEnv(t, "ALM_TEST_EXIT_GRACE=1s", "ANDROID_LOG_MCP_LINGER=1s")
	t0 := time.Now()
	// long enough for slow fake-adb starts on starved runners (-race: ~1 s per
	// adb call); the pin is observed, never slept through (#226)
	pin := 20 * time.Second
	if raceEnabled {
		pin = 40 * time.Second
	}
	out, errOut, code := cliEnv(t, env, "record", "--for", pin.String())
	if code != 0 || !strings.HasPrefix(out, "ok record action=pinned ") || !strings.Contains(out, "device id=FAKEA state=recording ") {
		t.Fatalf("record --for: %d\n%s\n%s", code, out, errOut)
	}
	pid := statePID(t, env)
	if pid == 0 {
		t.Fatal("no service pid")
	}
	// "recording" is reported once logcat was started; the fake registers
	// its pid a moment later (slow process starts on macOS runners)
	eventually(t, "logcat running", 5*time.Second, func() bool { return e.LogcatRunning("FAKEA") })
	out, _, _ = cliEnv(t, env, "record")
	if !strings.Contains(out, "device id=FAKEA ") || strings.Contains(out, "pinned_until=- ") || !strings.Contains(out, "pin_by=\"android-log-mcp record") {
		t.Fatalf("record state:\n%s", out)
	}
	if !e.LogcatRunning("FAKEA") {
		t.Fatalf("stopped before the pin ended (%s after the start)\n%s", time.Since(t0), dumpDiags(t))
	}
	waitGone(t, pid)
	// The service waits only a bounded time for a killed adb child (Windows:
	// TerminateProcess is asynchronous; killWait is 500 ms), so on a loaded
	// runner the child can still be exiting when the service pid is gone.
	// What matters is that the child ends, not that it is gone at the very
	// instant the service is.
	eventually(t, "logcat outlived the service", 20*time.Second, func() bool { return !e.LogcatRunning("FAKEA") })
	out, _, _ = cliEnv(t, env, "record")
	if !strings.HasPrefix(out, "empty record action=state service=not_running") || strings.Contains(out, "FAKEA") {
		t.Fatalf("state after the pin:\n%s", out)
	}
}

// record --stop stops recording and removes the pin at once.
func TestRecordStop(t *testing.T) {
	t.Parallel()
	env, e := lazyEnv(t, "ALM_TEST_EXIT_GRACE=1s")
	if out, errOut, code := cliEnv(t, env, "record", "--for", "1h"); code != 0 {
		t.Fatalf("record --for: %d\n%s\n%s", code, out, errOut)
	}
	pid := statePID(t, env)
	out, errOut, code := cliEnv(t, env, "record", "--stop")
	if code != 0 || !strings.Contains(out, "FAKEA") {
		t.Fatalf("record --stop: %d\n%s\n%s", code, out, errOut)
	}
	eventually(t, "logcat stops", 10*time.Second, func() bool { return !e.LogcatRunning("FAKEA") })
	waitGone(t, pid) // nothing keeps it: exits after the grace
	if b, _ := os.ReadFile(filepath.Join(controlDirOf(env), "run", "record.json")); strings.Contains(string(b), "FAKEA") {
		t.Fatalf("pin kept:\n%s", b)
	}
}

// A pinned service that is killed is restarted by a connected client at
// once (no tool call needed) and resumes recording the pinned device; the
// crash is counted in record.json.
func TestClientRestartsPinnedService(t *testing.T) {
	t.Parallel()
	env, e := lazyEnv(t)
	s := startMCP(t, env)
	old := statePID(t, env)
	if out, errOut, code := cliEnv(t, env, "record", "--for", "1h"); code != 0 {
		t.Fatalf("record --for: %d\n%s\n%s", code, out, errOut)
	}
	p, _ := os.FindProcess(old)
	p.Kill()
	waitGone(t, old)
	var pid int
	eventually(t, "a new service", 30*time.Second, func() bool {
		pid = statePID(t, env)
		return pid > 0 && pid != old
	})
	eventually(t, "recording resumes", 20*time.Second, func() bool { return e.LogcatRunning("FAKEA") })
	b, _ := os.ReadFile(filepath.Join(controlDirOf(env), "run", "record.json"))
	if !strings.Contains(string(b), `"count": 1`) || !strings.Contains(string(b), "ended without shutting down") {
		t.Fatalf("record.json:\n%s", b)
	}
	_ = s
}

// A version handoff keeps the pins (the new service resumes recording) and,
// through run/wanted.json (#226), the devices started by tool calls.
func TestHandoffKeepsPins(t *testing.T) {
	t.Parallel()
	env, e := lazyEnv(t)
	// Generous bounds (#230): on a starved CI runner (Windows, other jobs in
	// parallel) each fake adb call is a process spawn and the handoff plus
	// the old service's stop can take far longer than on a dev box.
	const wait = 60 * time.Second
	t.Cleanup(func() {
		if t.Failed() {
			rj, _ := os.ReadFile(filepath.Join(controlDirOf(env), "run", "record.json"))
			t.Logf("fake adb calls (tail):\n%s\nrecord.json:\n%s", e.CallsTail(60), rj)
		}
	})
	e.AddDevice("FAKEB", "boot-b")
	e.SetDevices(map[string]string{"FAKEA": "device", "FAKEB": "device"})
	if out, errOut, code := cliEnv(t, append(env, "ALM_TEST_VERSION=0.0.1"), "record", "--device", "FAKEA", "--for", "1h"); code != 0 {
		t.Fatalf("record --for: %d\n%s\n%s", code, out, errOut)
	}
	if out, errOut, code := cliEnv(t, append(env, "ALM_TEST_VERSION=0.0.1"), "call", "logcat_status", `{"device":"FAKEB"}`); code != 0 {
		t.Fatalf("call FAKEB: %d\n%s\n%s", code, out, errOut)
	}
	eventually(t, "FAKEB recorded", wait, func() bool { return e.LogcatRunning("FAKEB") })
	old := statePID(t, env)
	s := startMCP(t, append(env, "ALM_TEST_VERSION=99.0.0"))
	st := s.status(t)
	pid, v := servicePID(t, st)
	if pid == old || v != "99.0.0" {
		t.Fatalf("no handoff:\n%s", st)
	}
	if !strings.Contains(st, "pinned") {
		t.Fatalf("pin lost:\n%s", st)
	}
	eventually(t, "recording resumes", wait, func() bool { return e.LogcatRunning("FAKEA") })
	eventually(t, "the call-started device is carried over (wanted.json)", wait, func() bool { return e.LogcatRunning("FAKEB") })
	b, _ := os.ReadFile(filepath.Join(controlDirOf(env), "run", "record.json"))
	if strings.Contains(string(b), "ended without shutting down") {
		t.Fatalf("handoff counted as a crash:\n%s", b)
	}
}

// #226 (design 7.1 1c-bis): a device that a client used and left, still within
// its linger, is resumed by the successor of a killed service, even when the
// client that starts it is for another device; an explicit `service stop`
// resumes nothing.
func TestKilledServiceResumesWantedDevice(t *testing.T) {
	t.Parallel()
	env, e := lazyEnv(t)
	e.AddDevice("FAKEB", "boot-b")
	e.SetDevices(map[string]string{"FAKEA": "device", "FAKEB": "device"})
	dir := filepath.Join(controlDirOf(env), "run")
	if out, errOut, code := cliEnv(t, env, "call", "logcat_status", `{"device":"FAKEA"}`); code != 0 {
		t.Fatalf("call FAKEA: %d\n%s\n%s", code, out, errOut)
	}
	eventually(t, "FAKEA recorded", 60*time.Second, func() bool { return e.LogcatRunning("FAKEA") })
	// the file is written within the debounce (1 s) of the call
	eventually(t, "wanted.json names FAKEA", 60*time.Second, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "wanted.json"))
		return strings.Contains(string(b), "FAKEA")
	})
	old := statePID(t, env)
	p, _ := os.FindProcess(old)
	p.Kill()
	waitGone(t, old)
	if out, errOut, code := cliEnv(t, env, "call", "logcat_status", `{"device":"FAKEB"}`); code != 0 {
		t.Fatalf("call FAKEB: %d\n%s\n%s", code, out, errOut)
	}
	if pid := statePID(t, env); pid == 0 || pid == old {
		t.Fatalf("no new service (old %d, now %d)", old, pid)
	}
	eventually(t, "FAKEA resumed by the successor", 60*time.Second, func() bool { return e.LogcatRunning("FAKEA") })
	eventually(t, "FAKEB recorded", 60*time.Second, func() bool { return e.LogcatRunning("FAKEB") })

	// an explicit stop clears the file: the next start resumes nothing
	if out, errOut, code := cliEnv(t, env, "service", "stop"); code != 0 {
		t.Fatalf("service stop: %d\n%s\n%s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "wanted.json")); err == nil {
		t.Fatal("service stop left wanted.json")
	}
}
