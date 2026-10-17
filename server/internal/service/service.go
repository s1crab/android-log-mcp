package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/s1crab/android-log-mcp/server/internal/device"
	"github.com/s1crab/android-log-mcp/server/internal/ipc"
	"github.com/s1crab/android-log-mcp/server/internal/mcpserver"
	"github.com/s1crab/android-log-mcp/server/internal/store"
)

// ErrRunning: another service holds run/service.lock.
var ErrRunning = errors.New("an android-log-mcp service is already running for this user")

// Options configures a service.
type Options struct {
	Control Control
	Version string
	// Root is the data root; "" = the configured root (config.json) or
	// the control directory.
	Root string
	// Embedded is non-empty when the service runs inside an MCP client
	// process because a detached start failed; it holds the reason.
	Embedded string
	// Cfg is the store configuration (retention etc., from the env).
	Cfg store.Config
	// Log, if set, is the service log (moved along with set-root).
	Log *LogFile
	// Drain bounds how long in-flight calls may finish at shutdown
	// (default 5 s).
	Drain time.Duration
	// SocketWatch is how often the service checks that its socket file
	// still exists and recreates it if not (default 30 s).
	SocketWatch time.Duration
	// OnRootChange is called when set-root moves the data root (e.g. to
	// move the crash log).
	OnRootChange func(root string)
	// CatchUpWait overrides the log-tool catch-up wait; 0 = the default
	// (tests shorten it per service, #26).
	CatchUpWait time.Duration
	// ExitGrace and ExitTick override the package defaults ExitGrace and
	// exitTick when > 0 (tests, #26).
	ExitGrace, ExitTick time.Duration
	// Getenv reads the service's environment settings (ANDROID_LOG_MCP_LINGER);
	// nil = os.Getenv. Tests pass their own instead of t.Setenv (#26).
	Getenv func(string) string
	// Wanted holds the durations of the run/wanted.json carry (#226); zero
	// values are the defaults.
	Wanted WantedTiming
	// ADBPath is the adb binary for the device manager; "" = device.FindADB.
	ADBPath string
}

// Service is a running service.
type Service struct {
	opt     Options
	lock    *os.File
	srv     *ipc.Server
	started time.Time
	exe     string

	opMu  sync.RWMutex // calls/reports: R; set-root: W
	mu    sync.Mutex
	root  string
	m     *device.Manager
	mstop context.CancelFunc
	tools *mcpserver.Tools

	clients     int
	liveClients int           // clients that keep the service alive (keepsAlive)
	exitGrace   time.Duration // ExitGrace at Start
	exitTick    time.Duration
	idleSince   time.Time // no clients, no pin, nothing recorded since (lifeLoop)
	carry       device.WantState

	wanted      *wantedWriter // nil: embedded service
	wantedTm    WantedTiming
	clearWanted atomic.Bool // the stop is explicit or idle: remove wanted.json
	restoreWG   sync.WaitGroup

	pinMu    sync.Mutex
	pins     *PinFile // nil: record.json is unusable (pinsErr)
	pinsErr  error
	pinNotes []string // crash accounting at start

	stopOnce sync.Once
	stopping chan struct{}
	done     chan struct{}
	reason   string
}

// Hello is the first request of a client.
type Hello struct {
	Proto   int    `json:"proto"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Role    string `json:"role"` // mcp, call, report, set-root, cli

	// status remembers what logcat_status last showed on this connection
	status *mcpserver.StatusMemo
}

// HelloReply describes the service to the client.
type HelloReply struct {
	Proto        int                 `json:"proto"`
	MinProto     int                 `json:"min_proto"`
	Version      string              `json:"version"`
	PID          int                 `json:"pid"`
	Root         string              `json:"root"`
	Embedded     bool                `json:"embedded,omitempty"`
	Tools        []mcpserver.ToolDef `json:"tools"`
	Instructions string              `json:"instructions"`
	// Recording summarizes what is recorded ("idle (starts on the first
	// log tool call)" …); protocol 2.
	Recording string `json:"recording,omitempty"`
}

// CallArgs / CallReply are the "call" op.
type CallArgs struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}
type CallReply struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
}

// Info is the "status" op's reply.
type Info struct {
	PID      int       `json:"pid"`
	Version  string    `json:"version"`
	Proto    int       `json:"proto"`
	MinProto int       `json:"min_proto"`
	Root     string    `json:"root"`
	Control  string    `json:"control"`
	Socket   string    `json:"socket"`
	Exe      string    `json:"exe,omitempty"`
	Log      string    `json:"log,omitempty"`
	Started  time.Time `json:"started"`
	Clients  int       `json:"clients"`
	Embedded string    `json:"embedded,omitempty"`
	Linger   string    `json:"linger,omitempty"`
	// protocol 2 (issue #11)
	Recording string   `json:"recording,omitempty"`
	Devices   []string `json:"devices,omitempty"` // per recorded device
	Pins      []string `json:"pins,omitempty"`    // per pin (with suspension, last error)
	Lifetime  string   `json:"lifetime,omitempty"`
	// numeric twins of the sentences above (issue #85): seconds
	UptimeS    int `json:"uptime_s"`
	LingerMinS int `json:"linger_min_s"`
	LingerMaxS int `json:"linger_max_s"`
	ExitGraceS int `json:"exit_grace_s"`
}

// Text renders the info for `service status` (and, through Lines, the
// start of logcat_status).
func (i Info) Text() string {
	var b strings.Builder
	for _, l := range i.Lines() {
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// Lines is Text line by line. The lines that do not change from call to call
// (root, linger, lifetime, service log) are marked Static: logcat_status shows
// them on the first call of a connection and again when they change (#23).
func (i Info) Lines() []mcpserver.StatusLine {
	mode := "service process"
	if i.Embedded != "" {
		mode = "in-process service (" + i.Embedded + ")"
	}
	out := []mcpserver.StatusLine{
		{Text: fmt.Sprintf("service: %s pid %d, version %s, protocol %d, up %s, connected %d", mode, i.PID, i.Version, i.Proto, time.Since(i.Started).Round(time.Second), i.Clients)},
		{Text: "root: " + i.Root, Label: "root", Static: true},
	}
	if i.Recording != "" {
		out = append(out, mcpserver.StatusLine{Text: "recording: " + i.Recording})
	}
	for _, d := range i.Devices {
		out = append(out, mcpserver.StatusLine{Text: "  " + d})
	}
	for _, p := range i.Pins {
		out = append(out, mcpserver.StatusLine{Text: "  " + p})
	}
	if i.Linger != "" {
		out = append(out, mcpserver.StatusLine{Text: "linger: " + i.Linger, Label: "linger", Static: true})
	}
	if i.Lifetime != "" {
		// the rule is static; "staying: …" / "exiting in …" is the moment's state
		rule := i.Lifetime
		for _, sep := range []string{"; staying: ", "; exiting in "} {
			rule, _, _ = strings.Cut(rule, sep)
		}
		out = append(out, mcpserver.StatusLine{Text: "lifetime: " + i.Lifetime, Label: "lifetime", Static: true, Key: "lifetime: " + rule})
	}
	if i.Log != "" {
		out = append(out, mcpserver.StatusLine{Text: "service log: " + i.Log, Label: "service log", Static: true})
	}
	return out
}

// Start acquires the service lock, starts recording into the data root and
// serves the socket. It fails with ErrRunning when a service runs.
func Start(opt Options) (*Service, error) {
	if opt.Control.Dir == "" {
		opt.Control = DefaultControl()
	}
	if opt.Drain <= 0 {
		opt.Drain = 5 * time.Second
	}
	if opt.SocketWatch <= 0 {
		opt.SocketWatch = 30 * time.Second
	}
	if err := ipc.PrepareDir(opt.Control.Run()); err != nil {
		return nil, fmt.Errorf("run directory: %w", err)
	}
	lock, ok := store.TryLock(opt.Control.LockPath())
	if !ok {
		return nil, ErrRunning
	}
	s := &Service{opt: opt, lock: lock, started: time.Now(), stopping: make(chan struct{}), done: make(chan struct{}), exitGrace: ExitGrace, exitTick: exitTick}
	if opt.ExitGrace > 0 {
		s.exitGrace = opt.ExitGrace
	}
	if opt.ExitTick > 0 {
		s.exitTick = opt.ExitTick
	}
	s.exe, _ = os.Executable()
	root := opt.Root
	if root == "" {
		r, err := ConfiguredRoot(opt.Control)
		if err != nil {
			store.ReleaseLock(lock)
			return nil, err
		}
		root = r
	}
	if opt.Log != nil && opt.Log.Path() == "" {
		opt.Log.SetRoot(root)
	}
	// before the manager's startup sweep removes a crashed service's
	// stale service.json: a pinned recording counts that crash
	s.loadPins(opt.Control.ReadState())
	// run/wanted.json of the replaced or crashed service: read once into
	// memory before the manager starts (nothing may rewrite it first)
	var carry *wantedFile
	s.wantedTm = opt.Wanted.withDefaults()
	if opt.Embedded == "" {
		var err error
		if carry, err = readWanted(opt.Control.WantedPath()); err != nil {
			log.Printf("service: ignoring %s: %v", opt.Control.WantedPath(), err)
			carry = nil
		}
		s.wanted = newWantedWriter(opt.Control.WantedPath(), s.wantedTm, func(now time.Time) map[string]device.WantInfo {
			if m := s.Manager(); m != nil {
				return m.WantInfos(now)
			}
			return nil
		})
	}
	s.startManager(root)
	go retryCleanup(opt.Control, root)
	l, err := ipc.Listen(opt.Control.Socket())
	if err != nil {
		s.stopManager()
		store.ReleaseLock(lock)
		return nil, err
	}
	s.srv = &ipc.Server{Handler: s.handle, OnConn: s.onConn}
	go s.srv.Serve(l)
	go s.socketWatch()
	go s.lifeLoop() // a service nobody uses does not stay forever
	if err := s.writeState(); err != nil {
		log.Printf("service: writing %s: %v", opt.Control.StatePath(), err)
	}
	if carry != nil {
		s.restoreWG.Add(1)
		go s.restoreWanted(carry)
	}
	log.Printf("service started: pid %d, version %s, protocol %d, root %s, socket %s", os.Getpid(), opt.Version, Proto, root, opt.Control.Socket())
	return s, nil
}

func (s *Service) startManager(root string) {
	ctx, cancel := context.WithCancel(context.Background())
	m := device.NewManager(root, s.opt.Cfg)
	m.Version = s.opt.Version
	if s.exe != "" {
		m.KeepBin = filepath.Dir(s.exe) // never sweep the running binary
	}
	// lazy recording (issue #11): no adb until a log tool call or a pin
	m.Lazy = true
	m.Linger = s.deviceLinger
	if s.opt.ADBPath != "" {
		m.ADBPath = s.opt.ADBPath
	}
	m.PinnedUntil = s.pinUntil
	if s.wanted != nil {
		m.OnWantChange = s.wanted.Changed
	}
	if s.opt.CatchUpWait > 0 {
		m.CatchUpWait = s.opt.CatchUpWait
	}
	m.Start(ctx)
	t := &mcpserver.Tools{M: m, Pins: pinner{s}, ExportRoot: m.Root}
	t.Service = func() []mcpserver.StatusLine { return s.Info().Lines() }
	s.mu.Lock()
	s.root, s.m, s.mstop, s.tools = root, m, cancel, t
	carry := s.carry
	s.carry = nil
	s.mu.Unlock()
	if carry != nil {
		m.Restore(carry) // set-root: started devices keep their timers
	}
	s.startPinned(m)
}

// stopManager stops recording (flushing every store) and waits for it.
func (s *Service) stopManager() {
	s.mu.Lock()
	m, cancel := s.m, s.mstop
	s.mu.Unlock()
	if m == nil {
		return
	}
	snap := m.Snapshot()
	cancel()
	m.Close()
	s.mu.Lock()
	s.carry = snap // set-root restarts them in the new root
	s.mu.Unlock()
}

func (s *Service) writeState() error {
	st := State{PID: os.Getpid(), Socket: s.opt.Control.Socket(), Version: s.opt.Version, Proto: Proto, Root: s.Root(), Exe: s.exe, Embedded: s.opt.Embedded != "", Started: s.started.UTC()}
	st.StartTime, _, _ = inspectProc(os.Getpid())
	b, _ := json.MarshalIndent(st, "", "  ")
	return writeFileAtomic(s.opt.Control.StatePath(), append(b, '\n'), 0o600)
}

// socketWatch recreates the socket when its file is gone (a tmp cleaner, a
// user): the service keeps running and holding the lock, but nobody could
// connect any more (#83).
func (s *Service) socketWatch() {
	t := time.NewTicker(s.opt.SocketWatch)
	defer t.Stop()
	for {
		select {
		case <-s.stopping:
			return
		case <-t.C:
		}
		path := s.opt.Control.Socket()
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			continue
		}
		select {
		case <-s.stopping:
			return
		default:
		}
		if err := s.srv.Relisten(path); err != nil {
			log.Printf("error: the socket %s was removed and could not be recreated: %v", path, err)
			continue
		}
		log.Printf("warning: the socket %s was removed while the service was running; recreated it", path)
	}
}

// Root is the current data root.
func (s *Service) Root() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root
}

// Manager is the current device manager (tests).
func (s *Service) Manager() *device.Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m
}

// Info describes the service.
func (s *Service) Info() Info {
	s.mu.Lock()
	n := s.clients
	s.mu.Unlock()
	i := Info{PID: os.Getpid(), Version: s.opt.Version, Proto: Proto, MinProto: MinProto, Root: s.Root(), Control: s.opt.Control.Dir,
		Socket: s.opt.Control.Socket(), Exe: s.exe, Started: s.started, Clients: n, Embedded: s.opt.Embedded}
	if s.opt.Log != nil {
		i.Log = s.opt.Log.Path()
	}
	i.Linger = s.lingerText()
	i.UptimeS, i.LingerMinS, i.LingerMaxS, i.ExitGraceS = int(time.Since(s.started).Seconds()), int(LingerMin.Seconds()), int(LingerMax.Seconds()), int(s.exitGrace.Seconds())
	i.Lifetime = s.lifeText()
	i.Recording, i.Devices = s.recordingText()
	i.Pins = s.pinTexts()
	return i
}

// keepsAlive: connections of these clients keep the service running
// (§6); short status queries (`service status`, `root`, `record` state,
// role "cli") do not, so polling them cannot keep an unused service alive.
func keepsAlive(h *Hello) bool { return h.Role != "cli" }

func (s *Service) onConn(c *ipc.Conn, open bool) {
	if open {
		return
	}
	if h, ok := c.Value.(*Hello); ok {
		s.mu.Lock()
		s.clients--
		if keepsAlive(h) {
			s.liveClients--
		}
		s.mu.Unlock()
	}
}

func (s *Service) handle(ctx context.Context, c *ipc.Conn, op string, body json.RawMessage) (any, error) {
	switch op {
	case "hello":
		var h Hello
		if err := json.Unmarshal(body, &h); err != nil {
			return nil, err
		}
		if _, ok := c.Value.(*Hello); !ok {
			h.status = &mcpserver.StatusMemo{}
			c.Value = &h
			s.mu.Lock()
			s.clients++
			if keepsAlive(&h) {
				s.liveClients++
				s.idleSince = time.Time{}
			}
			s.mu.Unlock()
		}
		rec, _ := s.recordingText()
		return HelloReply{Proto: Proto, MinProto: MinProto, Version: s.opt.Version, PID: os.Getpid(), Root: s.Root(),
			Embedded: s.opt.Embedded != "", Tools: mcpserver.ToolDefs(), Instructions: mcpserver.Instructions(), Recording: rec}, nil
	case "status":
		return s.Info(), nil
	}
	h, _ := c.Value.(*Hello)
	if h == nil {
		return nil, errors.New("hello required first")
	}
	switch op {
	case "call":
		var a CallArgs
		if err := json.Unmarshal(body, &a); err != nil {
			return nil, err
		}
		s.opMu.RLock()
		defer s.opMu.RUnlock()
		s.mu.Lock()
		t := s.tools
		s.mu.Unlock()
		text, isErr := t.Call(mcpserver.WithStatusMemo(ctx, h.status), a.Name, a.Args)
		return CallReply{Text: text, IsError: isErr}, nil
	case "stop":
		s.clearWanted.Store(true) // an explicit stop resumes nothing
		go s.Stop(fmt.Sprintf("stop requested (pid %d)", h.PID))
		return "stopping", nil
	case "handoff":
		if CompareVersions(h.Version, s.opt.Version) <= 0 {
			return nil, fmt.Errorf("handoff refused: the MCP or CLI process (version %s) is not newer than this service (version %s)", h.Version, s.opt.Version)
		}
		go s.Stop(fmt.Sprintf("replaced by a newer MCP or CLI process (pid %d, version %s)", h.PID, h.Version))
		return "stopping", nil
	}
	if f := extraOps[op]; f != nil {
		return f(ctx, s, h, body)
	}
	return nil, fmt.Errorf("unknown op %q (service version %s, protocol %d)", op, s.opt.Version, Proto)
}

// extraOps are ops implemented in other files (report, set-root).
var extraOps = map[string]func(ctx context.Context, s *Service, h *Hello, body json.RawMessage) (any, error){}

// Stop shuts the service down (idempotent; returns when it has stopped):
// no new connections, in-flight calls get up to Options.Drain, connected
// clients are disconnected (they reconnect / restart the service), the
// recorders flush and stop, service.json is removed and the lock released.
func (s *Service) Stop(reason string) {
	first := false
	s.stopOnce.Do(func() {
		first = true
		s.mu.Lock()
		s.reason = reason
		s.mu.Unlock()
		close(s.stopping)
	})
	if !first {
		<-s.done
		return
	}
	log.Printf("service stopping: %s", reason)
	s.srv.CloseListener()
	deadline := time.Now().Add(s.opt.Drain)
	for s.srv.InFlight() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	s.srv.Close()
	s.restoreWG.Wait() // nothing is resumed after the manager closed
	if s.wanted != nil {
		// before the manager stops (a long flush must not be behind it) and
		// before the lock is released; synchronous, ordered after any
		// pending debounced write
		s.wanted.Close(s.clearWanted.Load())
	}
	s.stopManager()
	if st := s.opt.Control.ReadState(); st != nil && st.PID == os.Getpid() {
		os.Remove(s.opt.Control.StatePath())
	}
	store.ReleaseLock(s.lock)
	log.Printf("service stopped")
	close(s.done)
}

// Done is closed once the service has stopped.
func (s *Service) Done() <-chan struct{} { return s.done }

// Stopping is closed when shutdown begins.
func (s *Service) Stopping() <-chan struct{} { return s.stopping }

// Reason is why the service stopped.
func (s *Service) Reason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}
