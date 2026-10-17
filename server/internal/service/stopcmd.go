package service

import (
	"context"
	"fmt"
	"io"
	"time"
)

// RunStop is `service stop [--force]`: it returns the exit code.
func RunStop(ctrl Control, version string, force bool, stdout, stderr io.Writer) int {
	dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
	c, err := Dial(dctx, ctrl, version, "cli")
	dcancel()
	if err != nil {
		d := Diagnose(ctrl)
		if !d.Held {
			fmt.Fprintln(stdout, "service: not running")
			return 0
		}
		// the lock is held but the service does not answer: not "not running"
		if !force {
			fmt.Fprintf(stderr, "android-log-mcp service stop: %s but does not answer on %s; use `android-log-mcp service stop --force`\n", d.Text(ctrl), ctrl.Socket())
			return 1
		}
		return forceStop(ctrl, d, stdout, stderr)
	}
	pid := c.Hello.PID
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = c.Do(sctx, "stop", nil, nil)
	cancel()
	c.Close()
	if err != nil {
		if force {
			return forceStop(ctrl, Diagnose(ctrl), stdout, stderr)
		}
		fmt.Fprintln(stderr, "android-log-mcp service stop:", err)
		return 1
	}
	if !WaitStopped(ctrl, 20*time.Second) {
		if force {
			return forceStop(ctrl, Diagnose(ctrl), stdout, stderr)
		}
		fmt.Fprintf(stderr, "android-log-mcp service stop: pid %d is still running after 20 s; use `android-log-mcp service stop --force`\n", pid)
		return 1
	}
	fmt.Fprintf(stdout, "service: stopped (pid %d)\n", pid)
	return 0
}

func forceStop(ctrl Control, d Diagnosis, stdout, stderr io.Writer) int {
	res, err := StopHolder(ctrl, d, StopOptions{Killing: func(pid int) {
		fmt.Fprintf(stderr, "android-log-mcp service stop: pid %d did not stop gracefully; killing it (lines it had not flushed yet may be lost)\n", pid)
	}})
	if err != nil {
		fmt.Fprintf(stderr, "android-log-mcp service stop --force: %v (%s)\n", err, d.Text(ctrl))
		return 1
	}
	ClearWanted(ctrl) // an explicit stop resumes nothing (the service cannot tell it from a replacement)
	how := "stopped"
	if res.Killed {
		how = "killed"
	}
	fmt.Fprintf(stdout, "service: %s (pid %d)\n", how, d.PID)
	return 0
}
