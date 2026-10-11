package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// Tests target tmux 3.8: a missing or older tmux fails the test instead
// of silently skipping the native coverage. The previous server spec is
// restored on cleanup. Tests in this package don't run in parallel, so
// mutating the package-global tmuxServer here is safe.
func withTestTmuxServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("tests require tmux %d.%d: %v", testTmuxMajor, testTmuxMinor, err)
	}
	socket := fmt.Sprintf("tmux-qs-test-%d", os.Getpid())
	prev := getTmuxServer()
	setTmuxServer(tmuxServerSpec{flag: "-L", value: socket})
	// Subcommands reload the server from the environment, and subprocesses
	// inherit it. Pin both paths to the private server instead of the caller.
	t.Setenv("TMUX_QS_SERVER", "-L="+socket)
	t.Setenv("TMUX_QS_CLIENT", "")
	t.Setenv("LC_IS_EINK", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMUX_QS_CONFIG", "")
	// Start from a clean slate: drop any leftover server on this socket
	// (e.g. from a previously crashed run), then create a root session so
	// display-message / list-sessions probes have a current session.
	_ = run("tmux", "-L", socket, "kill-server")
	if err := run("tmux", "-L", socket, "-f", os.DevNull, "new-session", "-d", "-s", "qs-test-root"); err != nil {
		setTmuxServer(prev)
		t.Fatalf("cannot start private tmux server: %v", err)
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
	callerEnv, err := tmuxRunOut("display-message", "-p", "#{socket_path},#{pid},0")
	if err != nil {
		t.Fatalf("cannot determine private tmux environment: %v", err)
	}
	t.Setenv("TMUX", callerEnv)
	raw, err := tmuxRunOut("display-message", "-p", "#{version}")
	if err != nil {
		t.Fatalf("cannot determine private tmux version: %v", err)
	}
	if !tmuxVersionAtLeast(parseTmuxVersion(strings.TrimPrefix(raw, "next-")), testTmuxMajor, testTmuxMinor) {
		t.Fatalf("tests require tmux %d.%d or newer; found %q (put a tmux %d.%d build first on PATH)", testTmuxMajor, testTmuxMinor, raw, testTmuxMajor, testTmuxMinor)
	}
}

// Real-server tests run against tmux 3.8, matching CI.
const testTmuxMajor, testTmuxMinor = 3, 8

func tmuxVersionAtLeast(v TmuxVersion, major, minor int) bool {
	return v.Major > major || (v.Major == major && v.Minor >= minor)
}

// requireTmux38Capability fails when the private 3.8 server does not
// report a capability that the release is known to provide.
func requireTmux38Capability(t *testing.T, supported bool, name string) {
	t.Helper()
	if !supported {
		t.Fatalf("tmux %d.%d capability %s was not detected", testTmuxMajor, testTmuxMinor, name)
	}
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

func TestPrivateTmuxServerSurvivesEnvironmentReload(t *testing.T) {
	// Simulate running tests inside a caller's tmux without contacting it.
	t.Setenv("TMUX", filepath.Join(t.TempDir(), "caller.sock")+",123,0")
	t.Setenv("TMUX_QS_SERVER", "")
	withTestTmuxServer(t)
	private := getTmuxServer()

	setTmuxServerFromEnv()
	if got := getTmuxServer(); got != private {
		t.Fatalf("environment reload escaped private server: got %+v, want %+v", got, private)
	}

	if handled, err := runThemeSubcommand([]string{"eink", "set", "165,167"}); !handled || err != nil {
		t.Fatalf("private eink subcommand: handled=%v, err=%v", handled, err)
	}
	// A child process using tmux without explicit flags must also be private.
	got, err := runOut("tmux", "show-options", "-gv", "@eink-widths")
	if err != nil || got != "165,167" {
		t.Fatalf("child process escaped private server: widths=%q, err=%v", got, err)
	}
}
