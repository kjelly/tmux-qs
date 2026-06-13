package main

import (
	"context"
	"os/exec"
	"strings"
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

// runWithTimeout is the shared core: context with deadline, run, and
// kill-on-timeout. Returns the same (output, err) pair as
// exec.Cmd.Output / CombinedOutput so the call sites can ignore the
// timeout machinery.
func runWithTimeout(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
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


