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
//
// hintPath, when non-empty, is the cwd that the picker recorded for
// this entry. It's used to disambiguate sessions whose names
// collide (e.g. "foo" and "foo-1" rooted in different directories):
// if the resolved session's cwd doesn't match the hint, we
// re-derive a unique name and create a new session instead.
func connect(target string, paneID string, openWithAgent bool, selectedAgent string, hintPath string) error {
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
		if hintPath != "" {
			dir = expandPath(hintPath)
		} else if resolvedPath != "" {
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
		if hintPath != "" || resolvedPath != "" {
			sessionName = target
		} else if looksLikePath(target) {
			sessionName = sanitizeSessionName(filepath.Base(dir))
		}

		if _, exists := sessionsMap[sessionName]; !exists {
			existing := make(map[string]bool, len(sessionsMap))
			for k := range sessionsMap {
				existing[k] = true
			}
			name := deriveSessionName(dir, existing, cfg.Naming.Strategy)
			sessionName = name
			if err := tmuxRun("new-session", "-d", "-s", sessionName, "-c", dir); err == nil {
				applyAutoTemplate(sessionName, dir)
				_ = tmuxRun("send-keys", "-t", sessionName, agentCmd, "Enter")
			}
		} else {
			_ = tmuxRun("new-window", "-t", sessionName, "-c", dir, "-n", agentCmd)
			_ = tmuxRun("send-keys", "-t", sessionName, agentCmd, "Enter")
		}

		return switchOrAttach(sessionName)
	}

	if _, exists := sessionsMap[target]; exists {
		// If the picker carried a hint path, verify the resolved
		// session actually lives there. Two sessions with the same
		// name (e.g. legacy "foo-1" alongside a freshly created
		// "work-foo") shouldn't both be reachable through the same
		// target — when the hint disagrees, we drop down to the
		// directory-creation branch and let naming produce a fresh
		// unique session rooted at hintPath.
		if hintPath == "" || filepath.Clean(expandPath(hintPath)) == filepath.Clean(sessionsMap[target]) {
			if paneID != "" {
				_ = tmuxRun("select-pane", "-t", paneID)
			}
			return switchOrAttach(target)
		}
	}

	if resolvedPath != "" {
		target = resolvedPath
	}

	if paneID != "" {
		_ = tmuxRun("select-pane", "-t", paneID)
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
			if err := tmuxRun("new-session", "-d", "-s", sessionName, target); err == nil {
				return switchOrAttach(sessionName)
			}
		}
	}

	path := expandPath(target)
	if hintPath != "" {
		path = expandPath(hintPath)
	}
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
			existing := make(map[string]bool, len(sessionsMap))
			for k := range sessionsMap {
				existing[k] = true
			}
			sessionName := deriveSessionName(dirPath, existing, cfg.Naming.Strategy)

			if err := tmuxRun("new-session", "-d", "-s", sessionName, "-c", dirPath); err == nil {
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
						_ = tmuxRun("send-keys", "-t", sessionName, scriptCmd, "Enter")
					}
				}
				if !hasScript {
					applyAutoTemplate(sessionName, dirPath)
				}
				if isFile {
					_ = tmuxRun("send-keys", "-t", sessionName, editor+" "+filepath.Base(path), "Enter")
				}
				target = sessionName
			}
		} else {
			if isFile {
				_ = tmuxRun("new-window", "-t", existingSessionName, "-c", dirPath)
				_ = tmuxRun("send-keys", "-t", existingSessionName, editor+" "+filepath.Base(path), "Enter")
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
	if isEinkClient() {
		base := target
		if strings.HasSuffix(base, "-eink") {
			base = strings.TrimSuffix(base, "-eink")
		}
		einkTarget := base + "-eink"

		// Ensure einkTarget session exists
		sessions, err := tmuxRunLines("list-sessions", "-F", "#{session_name}")
		exists := false
		if err == nil {
			for _, s := range sessions {
				if strings.TrimSpace(s) == einkTarget {
					exists = true
					break
				}
			}
		}
		if !exists && base != "" {
			_ = tmuxRun("new-session", "-d", "-t", base, "-s", einkTarget)
		}

		// Apply E-ink optimization options specifically on einkTarget
		_ = tmuxRun("set-option", "-t", einkTarget, "status-style", "fg=#000000,bg=#ffffff")
		_ = tmuxRun("set-option", "-t", einkTarget, "window-status-current-style", "fg=#000000,bg=#ffffff,bold,reverse")
		_ = tmuxRun("set-option", "-t", einkTarget, "pane-border-style", "fg=#888888")
		_ = tmuxRun("set-option", "-t", einkTarget, "pane-active-border-style", "fg=#000000,bold")
		_ = tmuxRun("set-option", "-t", einkTarget, "mode-style", "fg=#ffffff,bg=#000000")
		_ = tmuxRun("set-option", "-t", einkTarget, "message-style", "fg=#000000,bg=#ffffff,bold")

		// Set E-ink environment variables on the session for downstream TUI apps
		_ = tmuxRun("set-environment", "-t", einkTarget, "LC_IS_EINK", "1")
		_ = tmuxRun("set-environment", "-t", einkTarget, "COLORFGBG", "15;0")

		target = einkTarget
	} else if strings.HasSuffix(target, "-eink") {
		// Monitor client selecting an -eink session: redirect to base session
		base := strings.TrimSuffix(target, "-eink")
		if base != "" {
			target = base
		}
	}

	recordLastSession()
	if os.Getenv("TMUX") != "" {
		client := os.Getenv("TMUX_QS_CLIENT")
		args := []string{"switch-client"}
		if client != "" {
			args = append(args, "-c", client)
		}
		args = append(args, "-t", target)
		err := tmuxRun(args...)
		if err == nil {
			recordVisit()
		}
		return err
	}
	err := tmuxRun("attach-session", "-t", target)
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
	lines, err := tmuxRunLines("list-sessions", "-F",
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
	panes, _ := tmuxRunLines("list-panes", "-s", "-F",
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
			if err := tmuxRun("select-window", "-t", parts[0]); err != nil {
				return err
			}
			return tmuxRun("select-pane", "-t", parts[1])
		}
	}
	return tmuxRun("new-window", "-c", path)
}

// selectTemplate returns the first user-defined [[template]] whose
// detect_files all exist under path, or nil if none matches. Used by
// applyAutoTemplate to decide what to do when a new session is
// created. Built-in detection (package.json / Cargo.toml / go.mod)
// was removed; users who want a split or extra commands on new
// sessions must define a [[template]] in their config.
func selectTemplate(cfg Config, path string) *TemplateConfig {
	for i, t := range cfg.Templates {
		if len(t.DetectFiles) == 0 {
			continue
		}
		matched := true
		for _, df := range t.DetectFiles {
			if _, err := os.Stat(filepath.Join(path, df)); err != nil {
				matched = false
				break
			}
		}
		if matched {
			return &cfg.Templates[i]
		}
	}
	return nil
}

// applyAutoTemplate applies the first matching user-defined template
// to a freshly created session. If no template matches, the session
// is left untouched (single pane, no extra commands).
func applyAutoTemplate(sessionName, path string) {
	if t := selectTemplate(loadConfig(), path); t != nil {
		applyTemplate(*t, sessionName, path)
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
					_ = tmuxRun("rename-window", "-t", sessionName+":0", w.Name)
				}
				if cmd != "" {
					_ = tmuxRun("send-keys", "-t", sessionName+":0", cmd, "Enter")
				}
			} else {
				// Subsequent windows: split and create.
				splitFlag := "-h"
				if w.Split == "horizontal" {
					splitFlag = "-v"
				}
				_ = tmuxRun("split-window", splitFlag, "-c", path, "-t", sessionName)
				if w.Name != "" {
					_ = tmuxRun("rename-window", "-t", sessionName+":0."+fmt.Sprintf("%d", i), w.Name)
				}
				if cmd != "" {
					_ = tmuxRun("send-keys", "-t", sessionName+":0."+fmt.Sprintf("%d", i), cmd, "Enter")
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
