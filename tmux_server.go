package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// tmuxServerSpec holds the -L / -S flags to prepend to every tmux
// invocation. Populated from CLI flags (--server / --socket) and
// inherited from the TMUX env var when running inside a tmux
// session that's already talking to a specific server.
//
// Empty by default, meaning "use the default tmux server" (which is
// the behavior the program has always had).
type tmuxServerSpec struct {
	// flag is "-L" or "-S"; empty means no server override.
	flag string
	// value is the socket name or path.
	value string
}

func (s tmuxServerSpec) args() []string {
	if s.flag == "" || s.value == "" {
		return nil
	}
	return []string{s.flag, s.value}
}

// tmuxServer is the active server spec. Set by main() from CLI flags
// before the TUI starts; the TUI and connect paths read it via
// tmuxArgs().
//
// tmuxServerMu protects tmuxServer from concurrent access between
// test functions (which write via clearTmuxServerForTest /
// withTestTmuxServer) and teatest program goroutines (which read
// via tmuxArgs / tmuxRun*). Without this mutex, -race detects a
// data race when a teatest program's async command goroutine
// (e.g. loadPreviewCmd) is still running when the next test
// modifies tmuxServer.
var (
	tmuxServerMu sync.RWMutex
	tmuxServer   tmuxServerSpec
)

// getTmuxServer returns a copy of the current server spec under the
// read lock.
func getTmuxServer() tmuxServerSpec {
	tmuxServerMu.RLock()
	defer tmuxServerMu.RUnlock()
	return tmuxServer
}

// setTmuxServer sets the server spec under the write lock.
func setTmuxServer(spec tmuxServerSpec) {
	tmuxServerMu.Lock()
	defer tmuxServerMu.Unlock()
	tmuxServer = spec
}

// allServersMode, when true, makes the picker scan all running tmux
// servers (via scanRunningTmuxServers) and show their sessions
// together, prefixed with the server name. Set by --all-servers.
var allServersMode bool

type tmuxServerEndpoint struct {
	label    string
	spec     tmuxServerSpec
	sessions []serverSession
}

var allServerEndpoints = struct {
	sync.RWMutex
	byLabel map[string]tmuxServerSpec
}{byLabel: make(map[string]tmuxServerSpec)}

func setAllServerEndpoints(endpoints []tmuxServerEndpoint) {
	allServerEndpoints.Lock()
	defer allServerEndpoints.Unlock()
	clear(allServerEndpoints.byLabel)
	for _, endpoint := range endpoints {
		allServerEndpoints.byLabel[endpoint.label] = endpoint.spec
	}
}

func allServerEndpoint(label string) (tmuxServerSpec, bool) {
	allServerEndpoints.RLock()
	defer allServerEndpoints.RUnlock()
	spec, ok := allServerEndpoints.byLabel[label]
	return spec, ok
}

// tmuxArgs returns the -L/-S flag pair that should be prepended to
// every tmux invocation, or nil if no server override is active.
// Use this as the prefix for tmux command runs.
func tmuxArgs() []string {
	return getTmuxServer().args()
}

// tmuxRun is shorthand for run("tmux", tmuxArgs()..., args...).
func tmuxRun(args ...string) error {
	full := append([]string{"tmux"}, tmuxArgs()...)
	full = append(full, args...)
	return run(full[0], full[1:]...)
}

// tmuxRunOut is shorthand for runOut("tmux", tmuxArgs()..., args...).
func tmuxRunOut(args ...string) (string, error) {
	full := append([]string{"tmux"}, tmuxArgs()...)
	full = append(full, args...)
	return runOut(full[0], full[1:]...)
}

// tmuxRunLines is shorthand for runLines("tmux", tmuxArgs()..., args...).
func tmuxRunLines(args ...string) ([]string, error) {
	full := append([]string{"tmux"}, tmuxArgs()...)
	full = append(full, args...)
	return runLines(full[0], full[1:]...)
}

// setTmuxServerFromEnv infers the server spec from the TMUX env var
// when we're inside a tmux session, or from the TMUX_QS_SERVER env
// var when the parent tmux-qs process explicitly propagated a server
// spec to the popup child. tmux sets TMUX to
// /path/to/socket,server-pid,index. Keep the complete socket path and
// use -S: reducing it to a -L name breaks custom `tmux -S` sockets and
// sockets outside tmux's default per-user directory.
func setTmuxServerFromEnv() {
	// TMUX_QS_SERVER takes precedence (set by the popup host so the
	// child inherits the server spec across the exec).
	if v := os.Getenv("TMUX_QS_SERVER"); v != "" {
		// Format: "FLAG=VALUE" (e.g. "-L=foo" or "-S=/path").
		for i := 0; i < len(v); i++ {
			if v[i] == '=' {
				flag := v[:i]
				value := v[i+1:]
				if flag == "-L" || flag == "-S" {
					setTmuxServer(tmuxServerSpec{flag: flag, value: value})
				}
				return
			}
		}
		return
	}
	v := os.Getenv("TMUX")
	if v == "" {
		return
	}
	// Format: /path/to/socket,<server-pid>,<session-index>.
	socket := v
	if i := strings.Index(socket, ","); i >= 0 {
		socket = socket[:i]
	}
	if socket != "" {
		setTmuxServer(tmuxServerSpec{flag: "-S", value: socket})
	}
}

// tmuxEnvironmentUsable reports whether the inherited TMUX value points at a
// reachable tmux server. SSH clients can forward TMUX from the originating
// host; in that case the socket path is usually meaningless on the remote
// host. Treat that environment as absent so the picker stays usable inline.
func tmuxEnvironmentUsable() bool {
	v := strings.TrimSpace(os.Getenv("TMUX"))
	if v == "" {
		return false
	}
	socket := v
	if i := strings.IndexByte(socket, ','); i >= 0 {
		socket = socket[:i]
	}
	if socket == "" {
		return false
	}
	_, err := runOut("tmux", "-S", socket, "list-sessions", "-F", "#{session_name}")
	return err == nil
}

// clearTmuxServerForTest resets the server spec. Tests use this to
// ensure a clean state.
func clearTmuxServerForTest() {
	setTmuxServer(tmuxServerSpec{})
}

// tmuxServerForTest returns the current server spec. Tests use this
// to verify CLI flag parsing.
func tmuxServerForTest() tmuxServerSpec {
	return getTmuxServer()
}

// scanRunningTmuxServers discovers all running tmux servers on the
// system by scanning the default tmux socket directory
// (/tmp/tmux-UID/ on Linux/macOS) and returning the names of
// servers that have at least one session. Each returned name is
// suitable to pass to `tmux -L NAME`. Returns an empty slice (not
// an error) if no servers are found or the directory doesn't exist.
//
// The "default" server name is also included if a default server is
// running.
func scanRunningTmuxServers() []tmuxServerEndpoint {
	dirs, err := defaultSocketDirs()
	if err != nil {
		return []tmuxServerEndpoint{}
	}
	candidates := []string{}
	seen := make(map[string]bool)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			info, infoErr := e.Info()
			if infoErr != nil || info.Mode()&os.ModeSocket == 0 {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if seen[path] {
				continue
			}
			seen[path] = true
			candidates = append(candidates, path)
		}
	}
	// A stale socket can take the complete command timeout. Probe a bounded
	// number concurrently so one bad server does not delay every other server;
	// retain candidate order for a stable list.
	ok := make([]bool, len(candidates))
	sessions := make([][]serverSession, len(candidates))
	sem := make(chan struct{}, min(4, len(candidates)))
	var wg sync.WaitGroup
	for i, name := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, err := runOut("tmux", "-S", name, "list-sessions", "-F", "#{session_name}\t#{session_path}")
			ok[i] = err == nil && out != ""
			if !ok[i] {
				return
			}
			for _, line := range strings.Split(out, "\n") {
				name, path, _ := strings.Cut(line, "\t")
				if name != "" {
					sessions[i] = append(sessions[i], serverSession{name: name, path: path})
				}
			}
		}()
	}
	wg.Wait()
	servers := make([]tmuxServerEndpoint, 0, len(candidates))
	labels := make(map[string]int)
	for i, name := range candidates {
		if ok[i] {
			label := filepath.Base(name)
			labels[label]++
			if labels[label] > 1 {
				label = filepath.Base(filepath.Dir(name)) + "/" + label
			}
			servers = append(servers, tmuxServerEndpoint{label: label, spec: tmuxServerSpec{flag: "-S", value: name}, sessions: sessions[i]})
		}
	}
	setAllServerEndpoints(servers)
	return servers
}

// buildExcludedSessionPaths returns the set of absolute session
// cwds that should be hidden from sources like zoxide — the user
// already has a tmux session rooted at each, so listing the path
// again in the picker would surface the same workspace twice.
//
// In normal (single-server) mode this is just the active server's
// session paths. In --all-servers mode it is the union of session
// paths across every running server. Always returns a non-nil map
// (possibly empty) so callers can pass the result directly to
// loadZoxide without a nil check.
func buildExcludedSessionPaths() map[string]bool {
	out := make(map[string]bool)
	if allServersMode {
		for _, srv := range scanRunningTmuxServers() {
			for _, session := range srv.sessions {
				if session.path != "" {
					out[filepath.Clean(session.path)] = true
				}
			}
		}
		return out
	}
	for _, p := range tmuxSessionPaths() {
		if p != "" {
			out[filepath.Clean(p)] = true
		}
	}
	return out
}

// defaultSocketDirs returns the directories where tmux keeps its
// server sockets. Usually $TMPDIR/tmux-UID/; on systems without
// $TMPDIR set, /tmp is used. Includes both $TMPDIR (preferred) and
// /tmp (fallback) to catch servers started with different TMPDIR
// settings.
func defaultSocketDirs() ([]string, error) {
	dirs := []string{}
	uid := os.Getuid()
	// Most systems: /tmp/tmux-UID/
	if _, err := os.Stat(fmt.Sprintf("/tmp/tmux-%d", uid)); err == nil {
		dirs = append(dirs, fmt.Sprintf("/tmp/tmux-%d", uid))
	}
	// Also check TMPDIR if set.
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		dirs = append(dirs, fmt.Sprintf("%s/tmux-%d", tmp, uid))
	}
	return dirs, nil
}
