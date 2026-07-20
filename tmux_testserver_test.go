package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// withTestTmuxServer routes every tmux call made during the test at a
// private, throwaway tmux server (its own -L socket) instead of the
// user's real server. This guarantees:
//
//   - a test can never detach the user — commands like "Detach Client"
//     or kill-session only ever reach the private server, which has no
//     attached client and none of the user's sessions;
//   - every session a test creates lives on the private server, and the
//     kill-server in cleanup wipes the whole server, so nothing leaks
//     onto the user's real server even if the test fails before its own
//     defer runs.
//
// The test is skipped when tmux isn't installed. The previous server
// spec is restored on cleanup. Tests in this package don't run in
// parallel, so mutating the package-global tmuxServer here is safe.
func withTestTmuxServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("tmux-qs-test-%d", os.Getpid())
	prev := getTmuxServer()
	setTmuxServer(tmuxServerSpec{flag: "-L", value: socket})
	// Start from a clean slate: drop any leftover server on this socket
	// (e.g. from a previously crashed run), then create a root session so
	// display-message / list-sessions probes have a current session.
	_ = run("tmux", "-L", socket, "kill-server")
	if err := run("tmux", "-L", socket, "new-session", "-d", "-s", "qs-test-root"); err != nil {
		setTmuxServer(prev)
		t.Skipf("cannot start private tmux server: %v", err)
	}
	t.Cleanup(func() {
		_ = run("tmux", "-L", socket, "kill-server")
		// kill-server usually unlinks the socket, but not always; remove
		// it explicitly so no stale socket file lingers in /tmp.
		for _, dir := range tmuxSocketCandidateDirs() {
			_ = os.Remove(filepath.Join(dir, socket))
		}
		setTmuxServer(prev)
	})
}

// tmuxSocketCandidateDirs returns the directories tmux may place its
// per-user sockets in: $TMPDIR/tmux-<uid>/ (when set) and /tmp/tmux-<uid>/.
func tmuxSocketCandidateDirs() []string {
	uid := os.Getuid()
	dirs := []string{fmt.Sprintf("/tmp/tmux-%d", uid)}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		dirs = append(dirs, filepath.Join(tmp, fmt.Sprintf("tmux-%d", uid)))
	}
	return dirs
}
