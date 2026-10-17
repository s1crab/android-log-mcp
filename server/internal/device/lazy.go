package device

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync/atomic"
	"time"

	"github.com/s1crab/android-log-mcp/server/internal/store"
)

// Lazy recording (issue #11): a device is recorded only after a log-tool
// call (or a pin) wants it, and stops when its own idle time has passed
// since its last call and no pin is active. With nothing wanted, adb
// device tracking stops too: an idle manager runs no adb at all.

type want struct {
	last  time.Time // last log-tool call for the device (zero: pin only)
	busy  int       // calls in flight (a wait_ms long poll is activity)
	since time.Time // when it was started
}

// ErrMaxDevices is returned by Want over ANDROID_LOG_MCP_MAX_DEVICES.
type ErrMaxDevices struct {
	Max     int
	Current []string
}

func (e *ErrMaxDevices) Error() string {
	return fmt.Sprintf("already recording %d devices (%v), the limit set by ANDROID_LOG_MCP_MAX_DEVICES=%d; stop one with `android-log-mcp record --stop --device <id>` or raise the limit", len(e.Current), e.Current, e.Max)
}

// Track starts adb device tracking (no recording) and waits (bounded) for
// the first device list: the first log-tool call needs it to resolve its
// device.
func (m *Manager) Track(timeout time.Duration) {
	if m.NoADB || m.adb == nil {
		return
	}
	m.startTracking()
	m.mu.Lock()
	m.trackedAt = time.Now()
	m.mu.Unlock()
	m.WaitReady(timeout)
}

// TrackGrace: device tracking started by a call that recorded nothing (no
// device, several devices, an error) stops this long after that call.
var TrackGrace = 30 * time.Second

// OnlineIDs returns the device IDs of the online devices whose identity
// is resolved, with their adb serial, sorted by ID.
func (m *Manager) OnlineIDs() []Identity {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	var out []Identity
	addrs := make([]string, 0, len(m.addrs))
	for a := range m.addrs {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	for _, a := range addrs {
		id := m.addrs[a]
		if m.states[a] != "device" || seen[id] {
			continue
		}
		seen[id] = true
		ident, ok := m.idents[id]
		if !ok {
			ident = Identity{ID: id}
		}
		ident.Addr = a
		out = append(out, ident)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Identifying returns the adb serials that are online but whose device
// identity is not resolved yet (a slow device right after tracking began).
func (m *Manager) Identifying() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for a, st := range m.states {
		if _, ok := m.addrs[a]; st == "device" && !ok {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// Settling reports whether the current adb tracking session has not yet
// delivered its first device list or still reads the identity of an online
// device: a device that is not listed may merely not be known yet.
func (m *Manager) Settling() bool {
	ts := m.session()
	if ts == nil {
		return false
	}
	select {
	case <-ts.ready:
		return false
	default:
		return true
	}
}

// WantAddr starts the device behind the adb serial addr, which adb lists
// but whose identity (and so device ID) is not read yet: it is started as
// soon as the identity is known. addr must be an identifying serial
// (Identifying); anything else is ignored.
func (m *Manager) WantAddr(addr string) {
	if !m.Lazy || m.NoADB {
		return
	}
	m.mu.Lock()
	pending := false
	if _, ok := m.addrs[addr]; !ok && m.states[addr] == "device" {
		m.wantAddrs[addr] = true
		pending = true
	}
	m.mu.Unlock()
	if pending {
		m.activity()
	}
}

// Wanted reports whether the device is started (recorded or waiting to
// come online).
func (m *Manager) Wanted(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.wanted[id]
	return ok
}

// WantedIDs lists the started devices.
func (m *Manager) WantedIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.wanted))
	for id := range m.wanted {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// LastCall is the device's last log-tool call (zero if none).
func (m *Manager) LastCall(id string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.wanted[id]; w != nil {
		return w.last
	}
	return time.Time{}
}

// Want starts recording device id (a log-tool call when call is true,
// which also refreshes its idle timer; false for a pin restored by a new
// service). It starts adb tracking if needed; the device is attached as
// soon as it is online. Safe to repeat and to call concurrently.
func (m *Manager) Want(id string, call bool) error {
	if !m.Lazy {
		return nil
	}
	if m.NoADB {
		return fmt.Errorf("adb disabled")
	}
	if m.adb == nil {
		return m.adbErr
	}
	m.wantMu.Lock()
	defer m.wantMu.Unlock()
	m.mu.Lock()
	w := m.wanted[id]
	created := false
	if w == nil {
		if len(m.wanted) >= m.MaxDevices {
			cur := make([]string, 0, len(m.wanted))
			for k := range m.wanted {
				cur = append(cur, k)
			}
			sort.Strings(cur)
			m.mu.Unlock()
			return &ErrMaxDevices{Max: m.MaxDevices, Current: cur}
		}
		w = &want{since: time.Now()}
		m.wanted[id] = w
		created = true
		log.Printf("%s: recording starts", id)
	}
	if call {
		w.last = time.Now()
	}
	ident, known := m.idents[id]
	online := ""
	for a, i := range m.addrs {
		if i == id && m.states[a] == "device" {
			online = a
			break
		}
	}
	m.mu.Unlock()
	m.startTracking()
	if known && online != "" {
		ident.Addr = online
		m.attach(ident)
		m.applyStates()
	}
	m.activity()
	m.wantChanged(created)
	return nil
}

// wantChanged tells OnWantChange that the wanted set (setChanged) or only a
// device's last call changed. It is called without m.mu held.
func (m *Manager) wantChanged(setChanged bool) {
	if f := m.OnWantChange; f != nil {
		f(setChanged)
	}
}

// Begin marks a log-tool call on the device as in flight (it is never
// idle meanwhile); the returned func ends it and refreshes the timer.
func (m *Manager) Begin(id string) func() {
	m.mu.Lock()
	if w := m.wanted[id]; w != nil {
		w.busy++
		w.last = time.Now()
	}
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		if w := m.wanted[id]; w != nil {
			if w.busy > 0 {
				w.busy--
			}
			w.last = time.Now()
		}
		m.mu.Unlock()
		m.wantChanged(false)
	}
}

// Unwant stops recording the device now: its logcat, ps and package
// refreshes stop, its store is flushed and reopened read-only (no
// writer.lock). With nothing wanted any more, adb tracking stops too.
func (m *Manager) Unwant(id, reason string) {
	if !m.Lazy {
		return
	}
	m.wantMu.Lock()
	defer m.wantMu.Unlock()
	m.unwantLocked(id, reason)
	m.mu.Lock()
	n := len(m.wanted)
	m.mu.Unlock()
	if n == 0 {
		m.stopTracking()
	}
	m.activity()
	m.wantChanged(true)
}

// unwantIdle is Unwant for an idle stop: under the lock it re-checks that
// the device is still idle (no call in flight or since CheckIdle decided,
// no pin) and keeps it recording otherwise (issue #15).
func (m *Manager) unwantIdle(id, reason string) {
	m.wantMu.Lock()
	defer m.wantMu.Unlock()
	if m.PinnedUntil != nil && m.PinnedUntil(id).After(time.Now()) {
		return
	}
	if m.IdleIn(id, time.Now()) != 0 {
		return // used meanwhile
	}
	m.unwantLocked(id, reason)
	m.mu.Lock()
	n := len(m.wanted)
	m.mu.Unlock()
	if n == 0 {
		m.stopTracking()
	}
	m.activity()
	m.wantChanged(true)
}

// beforeIdleStop (tests) runs between CheckIdle's decision to stop a device
// and the stop.
var beforeIdleStop atomic.Value // func(id string)

func (m *Manager) unwantLocked(id, reason string) {
	m.mu.Lock()
	_, ok := m.wanted[id]
	delete(m.wanted, id)
	d := m.devices[id]
	m.mu.Unlock()
	if ok {
		log.Printf("%s: recording stopped (%s)", id, reason)
	}
	if d == nil || !d.Owned() {
		return
	}
	m.release(id, d)
}

// release stops an owned device and replaces it by a read-only one.
func (m *Manager) release(id string, d *Device) {
	d.stop()
	done := make(chan struct{})
	go func() { d.runWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(m.Timing.RecorderStopWait.Get()):
		log.Printf("%s: recorder did not stop within %s", id, m.Timing.RecorderStopWait.Get())
	}
	d.waitBackground()
	if p := m.Timing.ReleasePause.Get(); p > 0 {
		time.Sleep(p)
	}
	m.attachMu.Lock()
	defer m.attachMu.Unlock()
	d.Store.Close() // flushes, releases writer.lock
	ro, err := store.OpenReadOnly(d.Store.Dir, m.Cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.devices[id] != d {
		return
	}
	if err != nil || ro.Serial == "" {
		delete(m.devices, id)
		return
	}
	nd := m.newDevice(ro, id)
	nd.adb = m.adb
	m.devices[id] = nd
}

// stopTracking stops adb device tracking (callers hold wantMu): no adb
// children run afterwards.
func (m *Manager) stopTracking() {
	m.mu.Lock()
	ts := m.sess
	m.sess = nil
	if ts == nil {
		m.mu.Unlock()
		return
	}
	m.states = map[string]string{}
	m.addrs = map[string]string{}
	m.wantAddrs = map[string]bool{}
	m.watchErr = nil
	devs := make([]*Device, 0, len(m.devices))
	for _, d := range m.devices {
		devs = append(devs, d)
	}
	m.mu.Unlock()
	ts.cancel()
	for _, d := range devs {
		d.setState(m.ctx, "")
	}
	log.Print("no device is recorded: adb device tracking stopped (idle)")
}

func (m *Manager) activity() {
	if m.OnActivity != nil {
		m.OnActivity()
	}
}

func (m *Manager) idleLoop(ctx context.Context) {
	tick := m.IdleTick
	if tick <= 0 {
		tick = time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.CheckIdle(time.Now())
	}
}

// IdleIn is how long until the device becomes idle (<0: never; 0: now or
// not started), ignoring its pin.
func (m *Manager) IdleIn(id string, now time.Time) time.Duration {
	m.mu.Lock()
	w := m.wanted[id]
	var last time.Time
	busy := 0
	if w != nil {
		last, busy = w.last, w.busy
	}
	d := m.devices[id]
	m.mu.Unlock()
	if w == nil {
		return 0
	}
	if busy > 0 {
		return -1
	}
	l := time.Duration(-1)
	if m.Linger != nil {
		l = m.Linger(d)
	}
	if l < 0 {
		return -1
	}
	if last.IsZero() {
		return 0
	}
	return max(0, last.Add(l).Sub(now))
}

// CheckIdle stops every started device whose idle time has passed since
// its last call and that has no active pin.
func (m *Manager) CheckIdle(now time.Time) {
	if !m.Lazy {
		return
	}
	var stop []string
	for _, id := range m.WantedIDs() {
		if m.PinnedUntil != nil && m.PinnedUntil(id).After(now) {
			continue
		}
		if m.IdleIn(id, now) == 0 {
			stop = append(stop, id)
		}
	}
	for _, id := range stop {
		reason := "idle: no log tool call within its linger time and no pin"
		if m.LastCall(id).IsZero() {
			reason = "its pin ended"
		}
		if h, _ := beforeIdleStop.Load().(func(string)); h != nil {
			h(id) // tests: a call arriving between the decision and the stop
		}
		m.unwantIdle(id, reason)
	}
	// tracking without any recorded device (e.g. a call that found no
	// device or several) stops after TrackGrace
	m.wantMu.Lock()
	defer m.wantMu.Unlock()
	m.mu.Lock()
	idle := len(m.wanted) == 0 && m.sess != nil && now.Sub(m.trackedAt) >= TrackGrace
	m.mu.Unlock()
	if idle {
		m.stopTracking()
		m.activity()
	}
}

// Idle reports whether nothing is recorded and adb tracking is stopped.
func (m *Manager) Idle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.wanted) == 0 && m.sess == nil
}

// WantState is the started devices' timers (carried over set-root).
type WantState map[string]WantEntry

// WantEntry is one started device's timer.
type WantEntry struct {
	Last  time.Time
	Since time.Time
}

// Snapshot returns the started devices' timers.
func (m *Manager) Snapshot() WantState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := WantState{}
	for id, w := range m.wanted {
		out[id] = WantEntry{Last: w.last, Since: w.since}
	}
	return out
}

// Restore starts the devices of a Snapshot again with their timers (set-root).
// A device that is already wanted keeps the later of the two last calls.
func (m *Manager) Restore(ws WantState) {
	for id, e := range ws {
		m.RestoreWant(id, e.Last, e.Since)
	}
}

// RestoreWant starts device id again as if it was last called at last. It
// merges instead of overwriting: a call that already refreshed the device
// keeps its later last (the restore must never shorten a live timer). since
// (zero: keep) is when the device was started. It returns the Want error
// (for example *ErrMaxDevices).
func (m *Manager) RestoreWant(id string, last, since time.Time) error {
	if err := m.Want(id, false); err != nil {
		log.Printf("%s: restarting recording: %v", id, err)
		return err
	}
	m.mu.Lock()
	if w := m.wanted[id]; w != nil {
		if last.After(w.last) {
			w.last = last
		}
		if !since.IsZero() && since.Before(w.since) {
			w.since = since
		}
	}
	m.mu.Unlock()
	return nil
}

// WantInfo is what a successor needs to resume one started device: how long
// ago its last log-tool call was and the linger in force (-1: forever).
type WantInfo struct {
	Idle   time.Duration
	Linger time.Duration
}

// WantInfos lists the started devices that had a log-tool call (a pin-only
// device, last zero, is re-applied by the pins). Idle comes from the
// monotonic clock; a call in flight counts as idle 0.
func (m *Manager) WantInfos(now time.Time) map[string]WantInfo {
	m.mu.Lock()
	type e struct {
		w *want
		d *Device
	}
	tmp := map[string]e{}
	for id, w := range m.wanted {
		if w.last.IsZero() {
			continue
		}
		tmp[id] = e{w, m.devices[id]}
	}
	idle := map[string]time.Duration{}
	for id, x := range tmp {
		if x.w.busy > 0 {
			idle[id] = 0
		} else {
			idle[id] = max(0, now.Sub(x.w.last))
		}
	}
	m.mu.Unlock()
	out := map[string]WantInfo{}
	for id, x := range tmp {
		l := time.Duration(-1)
		if m.Linger != nil {
			l = m.Linger(x.d)
		}
		out[id] = WantInfo{Idle: idle[id], Linger: l}
	}
	return out
}

// WaitRecording waits up to timeout until the wanted device id is attached
// by this process and its recorder session runs (catch-up then tells
// whether the backfill is done). It returns the device to use and whether
// it records; a device being stopped or re-attached (issue #15) returns
// false after the timeout, never silently the old read-only device as
// if it were current.
func (m *Manager) WaitRecording(ctx context.Context, id string, timeout time.Duration) (*Device, bool) {
	deadline := time.Now().Add(timeout)
	for {
		d := m.Get(id)
		if d != nil && d.started() {
			return d, true
		}
		if !m.Wanted(id) || ctx.Err() != nil || !time.Now().Before(deadline) {
			return d, false
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}
