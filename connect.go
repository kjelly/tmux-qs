package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// connect implements the selection handling previously delegated to
// sesh, with all logic now driven directly by tmux + a thin
// attached-session probe:
//
//	if target is an existing tmux session: switch-client (or attach)
//	elif target is an existing directory:
//	    reuse the tmux session rooted there, or create a new one
//	    rooted at the directory (optionally running the layout
//	    script), then switch
//	elif target is a path (e.g. a zoxide-tracked subdir) and the
//	    currently-attached session is rooted elsewhere:
//	    pick a pane in some session whose cwd matches the target
//	    and whose foreground command is in LayoutConfig.PaneShells;
//	    otherwise open a new window in the attached session
func connect(target string, paneID string) error {
	if paneID != "" {
		_ = run("tmux", "select-pane", "-t", paneID)
	}

	path := expandPath(target)
	isDir := false
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		isDir = true
	}

	if isDir {
		sessionsMap := tmuxSessionPaths()
		sessionExists := false
		existingSessionName := ""
		for name, p := range sessionsMap {
			if filepath.Clean(p) == filepath.Clean(path) {
				sessionExists = true
				existingSessionName = name
				break
			}
		}

		if !sessionExists {
			sessionName := sanitizeSessionName(filepath.Base(path))
			name := sessionName
			for i := 1; ; i++ {
				if _, exists := sessionsMap[name]; !exists {
					sessionName = name
					break
				}
				name = fmt.Sprintf("%s-%d", sessionName, i)
			}

			if err := run("tmux", "new-session", "-d", "-s", sessionName, "-c", path); err == nil {
				cfg := loadConfig()
				if cfg.EnableLayoutScripts() {
					scriptNames := cfg.Layout.ScriptNames
					if len(scriptNames) == 0 {
						scriptNames = []string{".tmux-qs.sh", ".tmux.sh"}
					}
					var foundScript string
					for _, sn := range scriptNames {
						scriptPath := filepath.Join(path, sn)
						if st, err := os.Stat(scriptPath); err == nil && !st.IsDir() {
							foundScript = scriptPath
							break
						}
					}
					if foundScript != "" {
						scriptCmd := "./" + filepath.Base(foundScript)
						if st, err := os.Stat(foundScript); err == nil {
							if st.Mode()&0111 == 0 { // not executable
								scriptCmd = "bash " + filepath.Base(foundScript)
							}
						}
						_ = run("tmux", "send-keys", "-t", sessionName, scriptCmd, "Enter")
					}
				}
				target = sessionName
			}
		} else {
			target = existingSessionName
		}
	}

	// By the time we reach this point, "target" is either:
	//   (a) the name of a tmux session that should already exist
	//       (either an existing one, or one we just created), or
	//   (b) a path entry that the directory branch above did not
	//       recognize (e.g. a zoxide subdir under the attached
	//       session's cwd but not equal to it).
	if _, ok := tmuxSessionPaths()[target]; ok {
		return switchOrAttach(target)
	}

	// Fallback path: target is a directory but no tmux session is
	// rooted there. We re-derive the path (in case the directory
	// branch above rewrote target to a session name) and try to
	// drop into the attached session, picking a matching pane or
	// opening a new window.
	path = expandPath(target)
	if root := attachedSessionPath(); root != "" && expandPath(root) == path {
		// The attached session is already rooted at path — just
		// find a matching pane or open a new window there.
		return pickPaneOrNewWindow(path)
	}

	// No attached session, or attached session rooted elsewhere.
	// Best we can do: open a new window in the attached session
	// (or a fresh detached session) at the requested path.
	return pickPaneOrNewWindow(path)
}

// switchOrAttach switches the current tmux client to the target
// session when called from inside tmux, or attaches a fresh client
// to it when called standalone. Mirrors the behavior of
// `sesh connect <name>` (without the sesh process in the middle).
func switchOrAttach(target string) error {
	if os.Getenv("TMUX") != "" {
		return run("tmux", "switch-client", "-t", target)
	}
	return run("tmux", "attach-session", "-t", target)
}

// attachedSessionPath returns the cwd of the currently-attached tmux
// session, or "" if there isn't one. Used to detect "we're already
// in the right workspace, just need a new pane" vs "we need to
// switch sessions entirely".
func attachedSessionPath() string {
	lines, err := runLines("tmux", "list-sessions", "-F",
		"#{session_attached}\t#{session_path}")
	if err != nil {
		return ""
	}
	for _, l := range lines {
		parts := strings.SplitN(l, "\t", 2)
		if len(parts) == 2 && parts[0] == "1" {
			return parts[1]
		}
	}
	return ""
}

// pickPaneOrNewWindow selects an existing pane in the attached tmux
// session whose cwd matches path and whose foreground command is in
// LayoutConfig.PaneShells. If no match is found, opens a new window
// at path instead. This is the "no new session needed" path: the
// user is already in the right workspace, just at a different
// shell/editor pane.
func pickPaneOrNewWindow(path string) error {
	// Tab-separated format: pane paths may contain spaces, and a
	// substring check (the old implementation) let /foo match /foobar.
	panes, _ := runLines("tmux", "list-panes", "-s", "-F",
		"#{window_id}\t#{pane_id}\t#{pane_current_path}\t#{pane_current_command}")
	shells := loadConfig().Layout.PaneShells
	shellSet := make(map[string]bool, len(shells))
	for _, s := range shells {
		shellSet[s] = true
	}
	want := filepath.Clean(expandPath(path))
	for _, p := range panes {
		parts := strings.Split(p, "\t")
		if len(parts) < 4 {
			continue
		}
		paneDir := filepath.Clean(parts[2])
		// Accept the exact directory or a pane somewhere beneath it
		// (a shell sitting in repo/subpkg still counts as "in this
		// workspace").
		if paneDir != want && !strings.HasPrefix(paneDir, want+"/") {
			continue
		}
		if shellSet[parts[3]] {
			if err := run("tmux", "select-window", "-t", parts[0]); err != nil {
				return err
			}
			return run("tmux", "select-pane", "-t", parts[1])
		}
	}
	return run("tmux", "new-window", "-c", path)
}
