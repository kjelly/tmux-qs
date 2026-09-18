package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// execTimeout caps the wall time for any external command. Without
// this, a hung `tmux`/`zoxide`/`git` invocation (NFS stall, ssh-agent
// blocking prompt) can freeze the TUI for the full OS-level exec
// timeout (often 2 minutes). 5s is generous for the commands tmux-qs
// actually runs and well under any "feels slow" perception threshold.
const execTimeout = 5 * time.Second

// runOut runs a command with a 5s timeout and returns its trimmed stdout.
func runOut(name string, args ...string) (string, error) {
	out, err := runWithTimeout(name, args...)
	return strings.TrimSpace(string(out)), err
}

// runLines runs a command with a 5s timeout and returns its stdout split
// into non-empty lines.
func runLines(name string, args ...string) ([]string, error) {
	out, err := runWithTimeout(name, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// run runs a command with a 5s timeout, discarding output, returning
// combined output on error.
func run(name string, args ...string) error {
	out, err := runWithTimeout(name, args...)
	if err != nil {
		return &cmdError{cmd: name + " " + strings.Join(args, " "), out: strings.TrimSpace(string(out)), err: err}
	}
	return nil
}

// runAction executes an explicit user action without imposing the short query
// timeout. Configured commands may legitimately be interactive or long-lived.
func runAction(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	err := cmd.Run()
	if err != nil {
		return &cmdError{cmd: name + " " + strings.Join(args, " "), err: err}
	}
	return nil
}

// runWithTimeout is the shared core: context with deadline, run, and
// kill-on-timeout. Returns the same (output, err) pair as
// exec.Cmd.Output / CombinedOutput so the call sites can ignore the
// timeout machinery.
func runWithTimeout(name string, args ...string) ([]byte, error) {
	return runWithDeadline(execTimeout, name, args...)
}

func runWithDeadline(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return output.Bytes(), err
	case <-ctx.Done():
		// Kill the process group so a timed-out shell cannot leave children
		// behind. Ignore ESRCH: the process may have exited at the boundary.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			_ = cmd.Process.Kill()
		}
		<-done
		return output.Bytes(), ctx.Err()
	}
}

type cmdError struct {
	cmd string
	out string
	err error
}

func (e *cmdError) Error() string {
	if e.out != "" {
		return e.out
	}
	return e.cmd + ": " + e.err.Error()
}

func (e *cmdError) Unwrap() error {
	return e.err
}

var lookPath = exec.LookPath

func isCommandInstalled(name string) bool {
	_, err := lookPath(name)
	return err == nil
}
