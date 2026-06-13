package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
func connect(target string, paneID string, openWithAgent bool, selectedAgent string) error {
	sessionsMap := tmuxSessionPaths()
	
	// Resolve configured session name to path if not currently running
	cfg := loadConfig()
	resolvedPath := ""
	for _, s := range cfg.Sessions {
		if s.Name == target {
			resolvedPath = s.ExpandedPath()
			break
		}
	}

	if openWithAgent {
		agentCmd := selectedAgent
		if agentCmd == "" {
			agentCmd = cfg.Layout.Agent
			if agentCmd == "" {
				agentCmd = "claude"
			}
		}

		dir := expandPath(target)
		if resolvedPath != "" {
			dir = resolvedPath
		}
		if st, err := os.Stat(dir); err == nil && !st.IsDir() {
			dir = filepath.Dir(dir)
		} else if err != nil {
			if p, ok := sessionsMap[target]; ok {
				dir = p
			}
		}

		sessionName := target
		if resolvedPath != "" {
			sessionName = target
		} else if looksLikePath(target) {
			sessionName = sanitizeSessionName(filepath.Base(dir))
		}

		if _, exists := sessionsMap[sessionName]; !exists {
			name := sessionName
			for i := 1; ; i++ {
				if _, exists := sessionsMap[name]; !exists {
					sessionName = name
					break
				}
				name = fmt.Sprintf("%s-%d", sessionName, i)
			}
			if err := run("tmux", "new-session", "-d", "-s", sessionName, "-c", dir); err == nil {
				applyAutoTemplate(sessionName, dir)
				_ = run("tmux", "send-keys", "-t", sessionName, agentCmd, "Enter")
			}
		} else {
			_ = run("tmux", "new-window", "-t", sessionName, "-c", dir, "-n", agentCmd)
			_ = run("tmux", "send-keys", "-t", sessionName, agentCmd, "Enter")
		}

		return switchOrAttach(sessionName)
	}

	if _, exists := sessionsMap[target]; exists {
		if paneID != "" {
			_ = run("tmux", "select-pane", "-t", paneID)
		}
		return switchOrAttach(target)
	}

	if resolvedPath != "" {
		target = resolvedPath
	}

	if paneID != "" {
		_ = run("tmux", "select-pane", "-t", paneID)
	}

	if strings.HasPrefix(target, "ssh ") {
		parts := strings.Fields(target)
		if len(parts) >= 2 {
			host := parts[1]
			sessionName := sanitizeSessionName(host)
			// Deduplicate session name if it already exists
			name := sessionName
			for i := 1; ; i++ {
				if _, exists := sessionsMap[name]; !exists {
					sessionName = name
					break
				}
				name = fmt.Sprintf("%s-%d", sessionName, i)
			}
			// Create a new session running the SSH command
			if err := run("tmux", "new-session", "-d", "-s", sessionName, target); err == nil {
				return switchOrAttach(sessionName)
			}
		}
	}

	path := expandPath(target)
	isDir := false
	isFile := false
	if st, err := os.Stat(path); err == nil {
		if st.IsDir() {
			isDir = true
		} else {
			isFile = true
		}
	}

	dirPath := path
	if isFile {
		dirPath = filepath.Dir(path)
	}

	if isDir || isFile {
		sessionsMap := tmuxSessionPaths()
		sessionExists := false
		existingSessionName := ""
		for name, p := range sessionsMap {
			if filepath.Clean(p) == filepath.Clean(dirPath) {
				sessionExists = true
				existingSessionName = name
				break
			}
		}

		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "nvim"
		}

		if !sessionExists {
			sessionName := sanitizeSessionName(filepath.Base(dirPath))
			name := sessionName
			for i := 1; ; i++ {
				if _, exists := sessionsMap[name]; !exists {
					sessionName = name
					break
				}
				name = fmt.Sprintf("%s-%d", sessionName, i)
			}

			if err := run("tmux", "new-session", "-d", "-s", sessionName, "-c", dirPath); err == nil {
				cfg := loadConfig()
				hasScript := false
				if cfg.EnableLayoutScripts() {
					scriptNames := cfg.Layout.ScriptNames
					if len(scriptNames) == 0 {
						scriptNames = []string{".tmux-qs.sh", ".tmux.sh"}
					}
					var foundScript string
					for _, sn := range scriptNames {
						scriptPath := filepath.Join(dirPath, sn)
						if st, err := os.Stat(scriptPath); err == nil && !st.IsDir() {
							foundScript = scriptPath
							break
						}
					}
					if foundScript != "" {
						hasScript = true
						scriptCmd := "./" + filepath.Base(foundScript)
						if st, err := os.Stat(foundScript); err == nil {
							if st.Mode()&0111 == 0 { // not executable
								scriptCmd = "bash " + filepath.Base(foundScript)
							}
						}
						_ = run("tmux", "send-keys", "-t", sessionName, scriptCmd, "Enter")
					}
				}
				if !hasScript {
					applyAutoTemplate(sessionName, dirPath)
				}
				if isFile {
					_ = run("tmux", "send-keys", "-t", sessionName, editor+" "+filepath.Base(path), "Enter")
				}
				target = sessionName
			}
		} else {
			if isFile {
				_ = run("tmux", "new-window", "-t", existingSessionName, "-c", dirPath)
				_ = run("tmux", "send-keys", "-t", existingSessionName, editor+" "+filepath.Base(path), "Enter")
			}
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
// Before switching, records the current session as "last session"
// and pushes it onto the visit stack so --last / --back / --forward
// can navigate without opening the TUI.
func switchOrAttach(target string) error {
	recordLastSession()
	if os.Getenv("TMUX") != "" {
		err := run("tmux", "switch-client", "-t", target)
		if err == nil {
			recordVisit()
		}
		return err
	}
	err := run("tmux", "attach-session", "-t", target)
	if err == nil {
		recordVisit()
	}
	return err
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

func applyAutoTemplate(sessionName, path string) {
	cfg := loadConfig()
	for _, t := range cfg.Templates {
		matched := false
		for _, df := range t.DetectFiles {
			if _, err := os.Stat(filepath.Join(path, df)); err == nil {
				matched = true
				break
			}
		}
		if matched {
			applyTemplate(t, sessionName, path)
			return
		}
	}

	// Fallback built-in templates
	if _, err := os.Stat(filepath.Join(path, "package.json")); err == nil {
		_ = run("tmux", "split-window", "-h", "-c", path, "-t", sessionName)
		devCmd := "npm run dev"
		if _, err := os.Stat(filepath.Join(path, "pnpm-lock.yaml")); err == nil {
			devCmd = "pnpm dev"
		} else if _, err := os.Stat(filepath.Join(path, "yarn.lock")); err == nil {
			devCmd = "yarn dev"
		}
		_ = run("tmux", "send-keys", "-t", sessionName+":0.1", devCmd, "Enter")
	} else if _, err := os.Stat(filepath.Join(path, "Cargo.toml")); err == nil {
		_ = run("tmux", "split-window", "-h", "-c", path, "-t", sessionName)
		_ = run("tmux", "send-keys", "-t", sessionName+":0.1", "cargo check", "Enter")
	} else if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
		_ = run("tmux", "split-window", "-h", "-c", path, "-t", sessionName)
	}
}

// applyTemplateByName finds a template by name and applies it to the
// given session. Used by Alt-t in the TUI for manual template
// application.
func applyTemplateByName(name, sessionName, path string) {
	cfg := loadConfig()
	for _, t := range cfg.Templates {
		if t.Name == name {
			applyTemplate(t, sessionName, path)
			return
		}
	}
}

// applyTemplate applies a template to a session. If the template has
// Windows defined, it creates the window layout (split + send-keys).
// Commands (shell commands) are always executed after the layout.
func applyTemplate(t TemplateConfig, sessionName, path string) {
	// Build window layout if defined.
	if len(t.Windows) > 0 {
		for i, w := range t.Windows {
			cmd := strings.ReplaceAll(w.Command, "{session}", sessionName)
			cmd = strings.ReplaceAll(cmd, "{path}", path)
			if i == 0 {
				// First window: rename the default window and send command.
				if w.Name != "" {
					_ = run("tmux", "rename-window", "-t", sessionName+":0", w.Name)
				}
				if cmd != "" {
					_ = run("tmux", "send-keys", "-t", sessionName+":0", cmd, "Enter")
				}
			} else {
				// Subsequent windows: split and create.
				splitFlag := "-h"
				if w.Split == "horizontal" {
					splitFlag = "-v"
				}
				_ = run("tmux", "split-window", splitFlag, "-c", path, "-t", sessionName)
				if w.Name != "" {
					_ = run("tmux", "rename-window", "-t", sessionName+":0."+fmt.Sprintf("%d", i), w.Name)
				}
				if cmd != "" {
					_ = run("tmux", "send-keys", "-t", sessionName+":0."+fmt.Sprintf("%d", i), cmd, "Enter")
				}
			}
		}
	}
	// Execute shell commands.
	for _, cmd := range t.Commands {
		cmd = strings.ReplaceAll(cmd, "{session}", sessionName)
		cmd = strings.ReplaceAll(cmd, "{path}", path)
		_ = run("sh", "-c", cmd)
	}
}

func openBrowserCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		url := gitRemoteURL(dir)
		if url == "" {
			return uiErrMsg{err: fmt.Errorf("no git remote found for %s", dir)}
		}
		_ = run("xdg-open", url)
		return nil
	}
}

func gitRemoteURL(dir string) string {
	out, err := runOut("git", "-C", dir, "remote", "get-url", "origin")
	if err != nil || out == "" {
		return ""
	}
	url := out
	if strings.HasPrefix(url, "git@") {
		url = strings.TrimPrefix(url, "git@")
		url = strings.Replace(url, ":", "/", 1)
		url = "https://" + url
	}
	url = strings.TrimSuffix(url, ".git")
	return url
}


