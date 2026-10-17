package device

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/s1crab/android-log-mcp/server/internal/layout"
	"github.com/s1crab/android-log-mcp/server/internal/store"
)

// Manager tracks adb devices and their stores. Devices are keyed by their
// stable ID (ResolveID); the adb serial is only the connection address.
type Manager struct {
	// Base is the data root: layout.DefaultDir()
	// (os.UserCacheDir()/android-log-mcp; fixed since #10), or the
	// --offline dataset.
	Base string
	// Root is the data root (Dir = Base). Only the service records, so a
	// device is recorded by one process per user (the service lock).
	Root       layout.Root
	Version    string
	Cfg        store.Config
	MaxDevices int
	// ADBPath is the adb binary to use; empty: FindADB
	// (ANDROID_LOG_MCP_ADB, PATH, the SDK). Tests point it at the fake adb
	// instead of changing the process environment (#26).
	ADBPath string
	// CatchUpWait is the longest a log-tool call waits for catch-up
	// (DefaultCatchUpWait; tests shorten it per Manager, #26).
	CatchUpWait time.Duration
	// Timing holds the time constants of recording (DefaultTiming; tests
	// shorten them per Manager, #26).
	Timing *Timing
	// NoADB (--offline <dir>): browse a dataset read-only; adb is not used
	// and nothing is recorded, pruned or created.
	NoADB bool
	// MaxTotalBytes caps everything the tool manages under Base (all
	// devices and format versions, tmp/, run/); 0 disables. Default 2 GiB.
	MaxTotalBytes int64
	// Disk pauses recording while free space on the data disk is low.
	Disk *store.DiskGuard
	// KeepBin is the bin/ directory of the running service binary (never
	// removed by the sweep).
	KeepBin string

	// Lazy (issue #11): no adb until a device is wanted (Want); only
	// wanted devices are recorded, each until it is idle (CheckIdle).
	// Without Lazy every online device is recorded (tests, older mode).
	Lazy bool
	// Linger is a device's idle time after its last call (<0: never
	// idle); nil = never idle.
	Linger func(d *Device) time.Duration
	// PinnedUntil returns a device's pin deadline (zero: no pin).
	PinnedUntil func(id string) time.Time
	// OnActivity is called after devices start or stop.
	OnActivity func()
	// OnWantChange is called (without locks, must not block) when the wanted
	// set changed (setChanged) or only a device's last log-tool call did.
	OnWantChange func(setChanged bool)
	// IdleTick is how often idle devices are checked (default 1 s).
	IdleTick time.Duration

	adb       *ADB
	adbErr    error
	ctx       context.Context
	mu        sync.Mutex
	devices   map[string]*Device // by device ID
	states    map[string]string  // live adb states by adb serial (incl. unauthorized/offline)
	addrs     map[string]string  // adb serial -> device ID
	resolving map[string]bool
	wantAddrs map[string]bool // adb serials named by a call while their identity is pending (issue #109)
	attachMu  sync.Mutex      // serializes attach / migration / sweep
	watchErr  error
	closed    bool // Close was called: never attach again
	sess      *trackSession
	bg        workers             // device tracking and identity probes (issue #24)
	kids      children            // their adb processes
	wanted    map[string]*want    // Lazy: devices to record, by ID
	idents    map[string]Identity // resolved online devices, by ID
	trackedAt time.Time           // last Track call (TrackGrace)
	wantMu    sync.Mutex          // serializes Want / Unwant / tracking start and stop
}

// trackSession is one period of adb device tracking.
type trackSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	ready  chan struct{}
	once   sync.Once
}

func (t *trackSession) markReady() { t.once.Do(func() { close(t.ready) }) }

func NewManager(base string, cfg store.Config) *Manager {
	m := &Manager{Base: base, Root: layout.Root{Dir: base}, Cfg: cfg, MaxDevices: 4,
		devices: map[string]*Device{}, states: map[string]string{}, addrs: map[string]string{}, resolving: map[string]bool{}, wantAddrs: map[string]bool{},
		wanted: map[string]*want{}, idents: map[string]Identity{}, MaxTotalBytes: DefaultMaxTotalBytes, CatchUpWait: DefaultCatchUpWait, Timing: DefaultTiming(), Disk: &store.DiskGuard{MinFree: DefaultMinFreeBytes}}
	if v, err := strconv.Atoi(os.Getenv("ANDROID_LOG_MCP_MAX_DEVICES")); err == nil && v > 0 {
		m.MaxDevices = v
	}
	if v, ok := store.EnvInt64("ANDROID_LOG_MCP_MAX_TOTAL_MB"); ok {
		m.MaxTotalBytes = v << 20 // 0 disables
	}
	if v, ok := store.EnvInt64("ANDROID_LOG_MCP_MIN_FREE_MB"); ok {
		m.Disk.MinFree = uint64(v) << 20 // 0 disables
	}
	return m
}

// Disk protection defaults.
const (
	DefaultMaxTotalBytes = 2 << 30
	DefaultMinFreeBytes  = 512 << 20 // resume at 2× this
)

func (m *Manager) newDevice(st *store.Store, serial string) *Device {
	d := newDevice(m.adb, st, serial)
	d.tm = m.Timing
	d.disk = m.Disk
	return d
}

func (m *Manager) maxAge() time.Duration {
	if m.Cfg.MaxAge > 0 {
		return m.Cfg.MaxAge
	}
	return store.DefaultMaxAge
}

// maintain runs the disk protection: the free-space guard (every 10 s), the
// sweep of the data root (retention + size cap, every minute) and aux-file
// compaction of the devices this process records (shortly after start, then
// every 10 min). Every tick it also retries devices whose identity is not
// resolved yet.
func (m *Manager) maintain(ctx context.Context) {
	m.Disk.Check(m.Base)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		n++
		if paused, changed := m.Disk.Check(m.Base); changed {
			if paused {
				log.Print(m.Disk.Reason())
			} else {
				log.Print("free disk space recovered; recording resumes")
			}
		}
		m.recheck()
		if n%6 == 0 {
			m.MaintainOnce(false)
		}
		if n == 3 || n%60 == 0 {
			m.MaintainOnce(true)
		}
	}
}

// MaintainOnce applies retention (per device, then a sweep of everything
// under the data root: other devices, other format versions, tmp/, run/,
// the total size cap) and, with aux, compacts the auxiliary files of the
// devices this process writes.
func (m *Manager) MaintainOnce(aux bool) {
	if m.NoADB {
		return // --offline: never prune or delete anything
	}
	m.attachMu.Lock()
	defer m.attachMu.Unlock()
	if m.closed {
		return
	}
	owned := map[string]*store.Store{}
	for _, d := range m.Devices() {
		if d.Owned() && d.Store.IsWriter() {
			owned[d.Store.Dir] = d.Store
			d.Store.Prune() // age / size retention, also while no segment rotates
			if aux {
				CompactAux(d.Store, time.Now())
			}
		}
	}
	m.sweepLocked(owned)
	m.dropVanished()
}

func (m *Manager) sweepLocked(owned map[string]*store.Store) layout.SweepResult {
	res := layout.Sweep(layout.SweepOptions{Base: m.Base, MaxTotal: m.MaxTotalBytes, MaxAge: m.maxAge(), Owned: owned, KeepBin: m.KeepBin})
	if res.CapTriggered && res.Freed > 0 {
		log.Printf("data directory over %d MiB: removed %d MiB of the oldest logs (ANDROID_LOG_MCP_MAX_TOTAL_MB)", m.MaxTotalBytes>>20, res.Freed>>20)
	} else if res.Freed > 0 {
		log.Printf("retention: removed %d expired files (%d KiB) under %s", res.Files, res.Freed>>10, m.Base)
	}
	for _, d := range res.RemovedDirs {
		log.Printf("retention: removed expired directory %s", d)
	}
	return res
}

// dropVanished forgets devices whose (unowned) directory was removed.
func (m *Manager) dropVanished() {
	m.mu.Lock()
	var gone []*Device
	for k, d := range m.devices {
		if d.Owned() {
			continue
		}
		if _, err := os.Stat(filepath.Join(d.Store.Dir, "meta.json")); os.IsNotExist(err) {
			delete(m.devices, k)
			gone = append(gone, d)
		}
	}
	m.mu.Unlock()
	for _, d := range gone {
		d.stop()
		d.Store.Close()
	}
}

// Start begins device tracking (non-blocking).
func (m *Manager) Start(ctx context.Context) {
	m.ctx = ctx
	if !m.NoADB {
		// startup sweep (--offline only browses a dataset, e.g. an unzipped
		// report: never write, prune or delete anything there)
		if err := m.Root.Ensure(); err != nil {
			log.Printf("data root %s: %v", m.Root.Dir, err)
		}
		m.attachMu.Lock()
		m.sweepLocked(nil)
		m.attachMu.Unlock()
	}
	m.loadDirs()
	if !m.NoADB {
		go m.maintain(ctx)
	}
	if m.NoADB {
		m.adbErr = fmt.Errorf("adb disabled")
		return
	}
	p, err := m.ADBPath, error(nil)
	if p == "" {
		p, err = FindADB() // a file lookup: runs no adb
	}
	if err != nil {
		m.adbErr = err
		return
	}
	m.adb = &ADB{Path: p, Timing: m.Timing}
	m.mu.Lock()
	for _, d := range m.devices {
		d.adb = m.adb
	}
	m.mu.Unlock()
	if m.Lazy {
		go m.idleLoop(ctx)
		return
	}
	m.startTracking()
}

// startTracking starts adb device tracking (idempotent): the adb server
// is started outside our process group first (it is shared with other
// tools and must outlive us), then `adb track-devices`.
func (m *Manager) startTracking() *trackSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess != nil {
		return m.sess
	}
	ctx, cancel := context.WithCancel(withChildren(m.ctx, &m.kids))
	ts := &trackSession{ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	m.sess = ts
	if m.adb == nil {
		ts.markReady()
		return ts
	}
	m.bg.spawn("device tracking", func() {
		sctx, scancel := context.WithTimeout(ctx, 20*time.Second)
		if err := m.adb.StartServer(sctx); err != nil && ctx.Err() == nil {
			log.Printf("adb start-server: %v", err)
		}
		scancel()
		m.adb.Watch(ctx, func(l []DevState, err error) { m.onDevices(ts, l, err) })
	})
	return ts // callers wait for ready with their own bound (WaitReady)
}

// session is the current tracking session (nil: not tracking).
func (m *Manager) session() *trackSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sess
}

// runCtx is the context recorders run in: the tracking session's.
func (m *Manager) runCtx() context.Context {
	if ts := m.session(); ts != nil {
		return ts.ctx
	}
	return m.ctx
}

// Tracking reports whether adb device tracking runs.
func (m *Manager) Tracking() bool { return m.session() != nil }

// loadDirs opens every recorded device directory under Base read-only:
// previously recorded devices are queryable even when offline (a device
// becomes recorded when it connects and is not owned elsewhere).
func (m *Manager) loadDirs() {
	for _, dir := range store.ListDeviceDirs(m.Base) {
		st, err := store.OpenReadOnly(dir, m.Cfg)
		if err != nil || st.Serial == "" {
			continue
		}
		m.mu.Lock()
		key := st.Serial
		if _, ok := m.devices[key]; ok {
			rel, _ := filepath.Rel(m.Base, dir)
			key += "@" + filepath.ToSlash(rel)
		}
		m.devices[key] = m.newDevice(st, key)
		m.mu.Unlock()
	}
}

// WaitReady waits (bounded) for the first device list of the current
// tracking session (returns at once when not tracking).
func (m *Manager) WaitReady(timeout time.Duration) {
	ts := m.session()
	if ts == nil {
		return
	}
	select {
	case <-ts.ready:
	case <-time.After(timeout):
	}
}

func (m *Manager) markReady() {
	if ts := m.session(); ts != nil {
		ts.markReady()
	}
}

func (m *Manager) onDevices(ts *trackSession, list []DevState, err error) {
	m.mu.Lock()
	if m.sess != ts {
		m.mu.Unlock()
		return // a stopped session
	}
	m.watchErr = err
	if err != nil {
		m.mu.Unlock()
		return
	}
	m.states = map[string]string{}
	for _, ds := range list {
		m.states[ds.Serial] = ds.State
	}
	for a := range m.wantAddrs {
		if m.states[a] != "device" {
			delete(m.wantAddrs, a) // gone before its identity was read
		}
	}
	for a := range m.addrs {
		if _, ok := m.states[a]; !ok {
			delete(m.addrs, a) // gone: the address may be reused by another device (emulator ports)
		}
	}
	pending := m.startResolvesLocked()
	m.mu.Unlock()
	m.applyStates()
	if pending == 0 {
		ts.markReady()
	}
}

// startResolvesLocked probes the identity of online adb serials not mapped
// to a device ID yet. Returns how many are pending.
func (m *Manager) startResolvesLocked() int {
	n := 0
	for a, st := range m.states {
		if st != "device" {
			continue
		}
		if _, ok := m.addrs[a]; ok {
			continue
		}
		n++
		if !m.resolving[a] {
			m.resolving[a] = true
			m.bg.spawn("identity probe "+a, func() { m.resolve(a) })
		}
	}
	return n
}

func (m *Manager) resolve(addr string) {
	ctx := m.runCtx()
	id, err := ProbeIdentity(ctx, m.adb, addr)
	if err != nil {
		m.mu.Lock()
		delete(m.resolving, addr)
		m.mu.Unlock()
		log.Printf("%s: identity probe failed (retrying): %v", addr, err)
		return
	}
	m.mu.Lock()
	named := m.wantAddrs[addr]
	delete(m.wantAddrs, addr)
	m.mu.Unlock()
	if named {
		if err := m.Want(id.ID, true); err != nil {
			log.Printf("%s: starting recording: %v", id.ID, err)
		}
	}
	final := m.attach(id)
	m.mu.Lock()
	delete(m.resolving, addr)
	if ctx.Err() != nil {
		m.mu.Unlock()
		return // tracking stopped meanwhile
	}
	if m.states[addr] != "" && final != "" {
		m.addrs[addr] = final
	}
	pending := m.startResolvesLocked()
	m.mu.Unlock()
	m.applyStates()
	if pending == 0 {
		m.markReady()
	}
}

// applyStates pushes the live adb state to every device (by ID).
func (m *Manager) applyStates() {
	m.mu.Lock()
	type upd struct {
		d     *Device
		addr  string
		state string
	}
	byID := map[string]upd{}
	addrs := make([]string, 0, len(m.addrs))
	for a := range m.addrs {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	for _, a := range addrs {
		id := m.addrs[a]
		cur, ok := byID[id]
		if ok && cur.state == "device" {
			continue // already reachable through another transport
		}
		byID[id] = upd{addr: a, state: m.states[a]}
	}
	var ups []upd
	for k, d := range m.devices {
		u, ok := byID[k]
		if !ok {
			ups = append(ups, upd{d: d})
			continue
		}
		u.d = d
		ups = append(ups, u)
	}
	m.mu.Unlock()
	ctx := m.runCtx()
	for _, u := range ups {
		if u.addr != "" {
			u.d.setAddr(u.addr)
		}
		u.d.setState(ctx, u.state)
	}
}

// recheck retries unresolved devices and devices that were not recorded
// (e.g. over MaxDevices before).
func (m *Manager) recheck() {
	m.mu.Lock()
	m.startResolvesLocked()
	var retry []string
	for a, id := range m.addrs {
		if m.states[a] != "device" || m.resolving[a] {
			continue
		}
		if _, w := m.wanted[id]; m.Lazy && !w {
			continue // known, not recorded until a call targets it
		}
		if d := m.devices[id]; d == nil || (m.Lazy && !d.Owned()) {
			retry = append(retry, a)
		}
	}
	for _, a := range retry {
		m.resolving[a] = true
		m.bg.spawn("identity probe "+a, func() { m.resolve(a) })
	}
	m.mu.Unlock()
}

// Attach is attach for a device whose identity is already known (tests,
// tools that probed it themselves). Returns the ID the device is filed under.
func (m *Manager) Attach(id Identity) string { return m.attach(id) }

// collision reports whether another device that is connected right now
// already uses id.ID: same ID, different adb serial and a different boot id
// (the same device over two transports, e.g. USB + Wi-Fi, has one boot id).
// Checked against this process's devices (the service records every
// device). Returns the other device's adb serial.
func (m *Manager) collision(id Identity) (string, bool) {
	if id.BootID == "" {
		return "", false
	}
	m.mu.Lock()
	cur := m.devices[id.ID]
	online := func(a string) bool { return a != "" && m.states[a] == "device" }
	m.mu.Unlock()
	if cur != nil && cur.Owned() {
		cur.mu.RLock()
		a, boot := cur.addr, cur.bootID
		cur.mu.RUnlock()
		m.mu.Lock()
		on := online(a)
		m.mu.Unlock()
		if a != id.Addr && on && boot != "" && boot != id.BootID {
			return a, true
		}
	}
	return "", false
}

// attach records device id into <base>/v1/<id> (unless the recording limit
// is reached). Returns the ID the device is filed under ("" if it is not
// tracked).
func (m *Manager) attach(id Identity) string {
	m.attachMu.Lock()
	defer m.attachMu.Unlock()
	if m.NoADB || m.closed || (m.ctx != nil && m.ctx.Err() != nil) {
		return "" // --offline never records or creates anything
	}
	if other, ok := m.collision(id); ok {
		base := id.ID
		id.ID = base + "~" + store.SanitizeSerial(id.Addr)
		id.Source += "+addr"
		id.Collision = fmt.Sprintf("device ID %s is already used by another connected device (adb %s, different boot); this device is filed separately as %s", base, other, id.ID)
		log.Printf("%s: %s", id.Addr, id.Collision)
	}
	m.mu.Lock()
	cur := m.devices[id.ID]
	m.idents[id.ID] = id
	_, wanted := m.wanted[id.ID]
	m.mu.Unlock()
	if m.Lazy && !wanted {
		return id.ID // known, but not recorded until a call targets it
	}
	if cur != nil && cur.Owned() {
		cur.setAddr(id.Addr)
		return id.ID
	}
	owned := 0
	for _, d := range m.Devices() {
		if d.Owned() {
			owned++
		}
	}
	if owned >= m.MaxDevices {
		log.Printf("%s (%s): not recorded, already recording %d devices (ANDROID_LOG_MCP_MAX_DEVICES)", id.ID, id.Addr, owned)
		return id.ID
	}
	dir := layout.DeviceDir(m.Base, id.ID)
	st, err := store.OpenDir(dir, id.ID, m.Cfg, true)
	if err != nil {
		log.Printf("%s: open %s: %v", id.ID, dir, err)
		return ""
	}
	if !st.IsWriter() {
		log.Printf("%s: %s is being written by another process (writer.lock held, pid %s); not recording it until that stops", id.ID, dir, readWriterPID(dir))
	}
	st.UpdateMeta(func(mt *store.Meta) {
		mt.Serial, mt.Addr, mt.IDSource = id.ID, id.Addr, id.Source
	})
	d := m.newDevice(st, id.ID)
	d.addr = id.Addr
	d.owned = true
	d.adb = m.adb
	d.idNote = id.Collision
	if id.BootID != "" {
		d.bootID = id.BootID
	}
	d.preProbe = id.Probe // used once: a later attach probes again
	m.mu.Lock()
	if i, ok := m.idents[id.ID]; ok && i.Probe == id.Probe {
		i.Probe = nil
		m.idents[id.ID] = i
	}
	m.mu.Unlock()
	m.swap(id.ID, d)
	return id.ID
}

func readWriterPID(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "writer.lock"))
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(b))
}

// swap installs d under key, retiring the previous device with that key and
// any other entry reading the same directory.
func (m *Manager) swap(key string, d *Device) {
	m.mu.Lock()
	var old []*Device
	for k, e := range m.devices {
		if k == key || e.Store.Dir == d.Store.Dir {
			delete(m.devices, k)
			old = append(old, e)
		}
	}
	m.devices[key] = d
	m.mu.Unlock()
	for _, e := range old {
		if e.Owned() {
			// not reached today (attach keeps an owned device), but never
			// close a store under a running recorder: shut it down like
			// release() does
			e.stop()
			done := make(chan struct{})
			go func() { e.runWG.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(m.Timing.RecorderStopWait.Get()):
				log.Printf("%s: recorder did not stop within %s", e.Serial, m.Timing.RecorderStopWait.Get())
			}
			e.waitBackground()
		} else {
			e.stop()
		}
		if e.Store == d.Store {
			continue
		}
		// A read-only store holds no file open between reads, and a closed
		// one still serves them: tool calls that still hold the old device
		// keep working, so close it now (issue #30).
		e.Store.Close()
		e.Store.ReleaseCaches()
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// ADBError is non-nil when adb could not be found.
func (m *Manager) ADBError() error { return m.adbErr }

// WatchError is the last device-listing error (adb server problems).
func (m *Manager) WatchError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watchErr
}

// ADB returns the adb wrapper (nil if not found).
func (m *Manager) ADB() *ADB { return m.adb }

// LiveStates returns adb's current serial->state (incl. unauthorized).
func (m *Manager) LiveStates() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.states {
		out[k] = v
	}
	return out
}

// Devices returns all devices with stores, sorted by serial.
func (m *Manager) Devices() []*Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Device, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Serial < out[j].Serial })
	return out
}

// Get returns the device named by its ID, its current adb serial, or an
// adb serial it was last reached at (nil if
// unknown).
func (m *Manager) Get(name string) *Device {
	m.mu.Lock()
	if id, ok := m.addrs[name]; ok && m.devices[id] != nil {
		d := m.devices[id]
		m.mu.Unlock()
		return d
	}
	if d := m.devices[name]; d != nil {
		m.mu.Unlock()
		return d
	}
	keys := make([]string, 0, len(m.devices))
	for k := range m.devices {
		keys = append(keys, k)
	}
	devs := m.devices
	sort.Strings(keys)
	cand := make([]*Device, 0, len(keys))
	for _, k := range keys {
		cand = append(cand, devs[k])
	}
	m.mu.Unlock()
	var fallback *Device
	for _, d := range cand {
		if d.Matches(name) {
			if d.Owned() {
				return d
			}
			if fallback == nil {
				fallback = d
			}
		}
	}
	return fallback
}

// ReplaySpan is the longest time span the log buffer of a device recorded
// in this run holds (newest - oldest line of its main buffer, measured
// at every (re)connect); 0 if none was measured.
func (m *Manager) ReplaySpan() time.Duration {
	var max time.Duration
	for _, d := range m.Devices() {
		if v := time.Duration(d.spanNs.Load()); v > max {
			max = v
		}
	}
	return max
}

// AddrOf returns the device ID an adb serial resolved to ("" if unknown).
func (m *Manager) AddrOf(addr string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addrs[addr]
}

// Close stops the recorders, waits (bounded) until each has flushed its
// last lines and closes the stores. Attach does nothing afterwards.
func (m *Manager) Close() {
	m.attachMu.Lock()
	m.closed = true
	m.attachMu.Unlock()
	devs := m.Devices()
	for _, d := range devs {
		d.stop()
	}
	done := make(chan struct{})
	go func() {
		for _, d := range devs {
			d.runWG.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Print("recorders did not stop within 10 s; closing the stores anyway")
	}
	for _, d := range devs {
		d.waitBackground()
	}
	// device tracking and identity probes (issue #24)
	m.mu.Lock()
	ts := m.sess
	m.mu.Unlock()
	if ts != nil {
		ts.cancel()
	}
	stopBackground("device tracking", m.Timing.BgStopWait.Get(), &m.bg, &m.kids)
	m.attachMu.Lock()
	defer m.attachMu.Unlock()
	for _, d := range m.Devices() {
		d.Store.Close()
	}
}
