// Package service is the single long-lived android-log-mcp process per user
// (issue #3): it records every device, owns all writes (retention, sweep,
// set-root) and answers the tool calls of thin clients over a local socket
// (package ipc). MCP processes, `call`, `report` and `set-root` connect to
// it, starting it detached when it is not running.
package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/s1crab/android-log-mcp/server/internal/ipc"
	"github.com/s1crab/android-log-mcp/server/internal/layout"
)

// Control is the fixed per-user control directory,
// os.UserCacheDir()/android-log-mcp (also the default data root). It holds
// what clients need to find the service and never moves with set-root:
//
//	run/service.lock   held by the running service (single instance)
//	run/service.json   the service's pid, version, protocol, socket, root
//	run/service.sock   the socket (long control dir paths: /tmp/alm-<uid>/<hash>.sock, ipc.SocketPath)
//	run/wanted.json    the devices being recorded, for the successor of a replaced or crashed service (wanted.go)
//	run/spawn.lock     serializes clients starting the service
//	run/config.lock    serializes set-root
//	config.json        {"root": …} written by set-root only
type Control struct{ Dir string }

// DefaultControl is the control directory of the current user.
func DefaultControl() Control { return Control{Dir: layout.DefaultDir()} }

func (c Control) Run() string            { return filepath.Join(c.Dir, "run") }
func (c Control) Socket() string         { return ipc.SocketPath(c.Run()) }
func (c Control) LockPath() string       { return filepath.Join(c.Run(), "service.lock") }
func (c Control) StatePath() string      { return filepath.Join(c.Run(), "service.json") }
func (c Control) WantedPath() string     { return filepath.Join(c.Run(), "wanted.json") }
func (c Control) SpawnLockPath() string  { return filepath.Join(c.Run(), "spawn.lock") }
func (c Control) ConfigPath() string     { return filepath.Join(c.Dir, "config.json") }
func (c Control) ConfigLockPath() string { return filepath.Join(c.Run(), "config.lock") }

// State is run/service.json (also read by layout.SweepRun: pid + socket).
type State struct {
	PID     int    `json:"pid"`
	Socket  string `json:"socket"`
	Version string `json:"version"`
	Proto   int    `json:"proto"`
	Root    string `json:"root"`
	Exe     string `json:"exe,omitempty"`
	// StartTime is the OS-reported start time of the service process
	// (an opaque value, only compared with a later reading of the same pid
	// to tell the service from a process that reused its pid).
	StartTime int64     `json:"start_time,omitempty"`
	Embedded  bool      `json:"embedded,omitempty"`
	Started   time.Time `json:"started"`
}

// ReadState reads run/service.json (nil if missing or unreadable).
func (c Control) ReadState() *State {
	b, err := os.ReadFile(c.StatePath())
	if err != nil {
		return nil
	}
	var s State
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

func writeFileAtomic(p string, b []byte, perm os.FileMode) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
