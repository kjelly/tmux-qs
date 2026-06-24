package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func executeCommand(cmd string) error {
	switch cmd {
	case "Tmux: Detach Client":
		return tmuxRun("detach-client")
	case "Tmux: Kill Server (Danger)":
		return tmuxRun("kill-server")
	case "Tmux: Reload Tmux Config":
		return tmuxRun("source-file", expandPath("~/.tmux.conf"))
	case "Tmux-QS: Open Config File":
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "nvim"
		}
		path := expandPath("~/.config/tmux-qs/config.toml")
		return tmuxRun("new-window", "-n", "config", editor+" "+path)
	case "Resurrect: Save Workspace State":
		return resurrectSave()
	case "Resurrect: Restore Workspace State":
		return resurrectRestore()
	}

	cfg := loadConfig()
	for _, c := range cfg.Commands {
		if c.Name == cmd {
			execCmd := c.Cmd
			if strings.Contains(execCmd, "{session}") {
				execCmd = strings.ReplaceAll(execCmd, "{session}", currentSessionName())
			}
			if strings.Contains(execCmd, "{path}") {
				execCmd = strings.ReplaceAll(execCmd, "{path}", currentSessionPath())
			}
			return run("sh", "-c", execCmd)
		}
	}
	return nil
}

func currentSessionPath() string {
	path, _ := tmuxRunOut("display-message", "-p", "#{session_path}")
	return path
}

// ResurrectState is the on-disk workspace snapshot. The schema captures
// each session's windows and panes (name, layout, cwd, running program)
// so a restore reconstructs the full layout — not just bare sessions.
type ResurrectState struct {
	Sessions []ResurrectSession `json:"sessions"`
}

type ResurrectSession struct {
	Name    string            `json:"name"`
	Path    string            `json:"path"`
	Windows []ResurrectWindow `json:"windows"`
}

type ResurrectWindow struct {
	Index  int             `json:"index"`
	Name   string          `json:"name"`
	Layout string          `json:"layout"` // tmux #{window_layout} string
	Panes  []ResurrectPane `json:"panes"`
}

type ResurrectPane struct {
	Path    string `json:"path"`
	Command string `json:"command"` // foreground program at save time
}

func stateFilePath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "state", "tmux-qs")
	os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "resurrect.json")
}

// captureResurrectState builds a ResurrectState from the live tmux server
// by walking every pane (one list-panes fork) and grouping by session and
// window, preserving order.
func captureResurrectState() (ResurrectState, error) {
	format := strings.Join([]string{
		"#{session_name}", "#{session_path}",
		"#{window_index}", "#{window_name}", "#{window_layout}",
		"#{pane_index}", "#{pane_current_path}", "#{pane_current_command}",
	}, "\t")
	lines, err := tmuxRunLines("list-panes", "-a", "-F", format)
	if err != nil {
		return ResurrectState{}, err
	}
	return ResurrectState{Sessions: buildResurrectSessions(lines)}, nil
}

// captureResurrectSession snapshots a single live session by name, for the
// kill-undo buffer. Returns false when the session has no panes (e.g. it
// has already gone away).
func captureResurrectSession(name string) (ResurrectSession, bool) {
	format := strings.Join([]string{
		"#{session_name}", "#{session_path}",
		"#{window_index}", "#{window_name}", "#{window_layout}",
		"#{pane_index}", "#{pane_current_path}", "#{pane_current_command}",
	}, "\t")
	lines, err := tmuxRunLines("list-panes", "-s", "-t", name, "-F", format)
	if err != nil {
		return ResurrectSession{}, false
	}
	sessions := buildResurrectSessions(lines)
	if len(sessions) == 0 {
		return ResurrectSession{}, false
	}
	return sessions[0], true
}

// buildResurrectSessions groups tab-separated pane lines (session, path,
// window index/name/layout, pane index/path/command) into ordered
// ResurrectSession records.
func buildResurrectSessions(lines []string) []ResurrectSession {
	type winKey struct {
		session string
		index   int
	}
	var sessionOrder []string
	sessions := map[string]*ResurrectSession{}
	winOrder := map[string][]int{}
	windows := map[winKey]*ResurrectWindow{}

	for _, l := range lines {
		p := strings.Split(l, "\t")
		if len(p) < 8 {
			continue
		}
		sName, sPath := p[0], p[1]
		wIdx, _ := strconv.Atoi(p[2])
		wName, wLayout := p[3], p[4]
		paneCmd := p[7]
		panePath := p[6]

		s, ok := sessions[sName]
		if !ok {
			s = &ResurrectSession{Name: sName, Path: sPath}
			sessions[sName] = s
			sessionOrder = append(sessionOrder, sName)
		}
		wk := winKey{sName, wIdx}
		w, ok := windows[wk]
		if !ok {
			w = &ResurrectWindow{Index: wIdx, Name: wName, Layout: wLayout}
			windows[wk] = w
			winOrder[sName] = append(winOrder[sName], wIdx)
		}
		w.Panes = append(w.Panes, ResurrectPane{Path: panePath, Command: paneCmd})
	}

	var out []ResurrectSession
	for _, sName := range sessionOrder {
		s := sessions[sName]
		for _, wIdx := range winOrder[sName] {
			s.Windows = append(s.Windows, *windows[winKey{sName, wIdx}])
		}
		out = append(out, *s)
	}
	return out
}

func resurrectSave() error { return doResurrectSave(true) }

// doResurrectSave snapshots the workspace to disk. When notify is true it
// flashes a tmux status message; the periodic auto-save passes false so it
// stays silent.
func doResurrectSave(notify bool) error {
	state, err := captureResurrectState()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(stateFilePath(), data, 0644); err != nil {
		return err
	}
	if notify {
		return tmuxRun("display-message", fmt.Sprintf("tmux-qs: Saved %d sessions", len(state.Sessions)))
	}
	return nil
}

// loadResurrectState reads the saved state, tolerating the legacy
// map[name]path schema written by older versions (best-effort migration).
func loadResurrectState() (ResurrectState, error) {
	data, err := os.ReadFile(stateFilePath())
	if err != nil {
		return ResurrectState{}, err
	}
	var state ResurrectState
	if err := json.Unmarshal(data, &state); err == nil && state.Sessions != nil {
		return state, nil
	}
	// Legacy fallback: {"sessions": {"name": "path"}}.
	var legacy struct {
		Sessions map[string]string `json:"sessions"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return ResurrectState{}, err
	}
	for name, path := range legacy.Sessions {
		state.Sessions = append(state.Sessions, ResurrectSession{Name: name, Path: path})
	}
	return state, nil
}

func resurrectRestore() error {
	state, err := loadResurrectState()
	if err != nil {
		return err
	}

	// Skip sessions that already exist so restore is idempotent.
	currentLines, _ := tmuxRunLines("list-sessions", "-F", "#{session_name}")
	current := make(map[string]bool)
	for _, l := range currentLines {
		current[l] = true
	}

	allowed := restoreProgramSet()
	count := 0
	for _, s := range state.Sessions {
		if current[s.Name] {
			continue
		}
		if restoreSession(s, allowed) {
			count++
		}
	}
	return tmuxRun("display-message", fmt.Sprintf("tmux-qs: Restored %d sessions", count))
}

// restoreProgramSet returns the set of foreground programs that may be
// re-launched on restore, from config.
func restoreProgramSet() map[string]bool {
	set := map[string]bool{}
	for _, p := range loadConfig().Resurrect.RestorePrograms {
		if p != "" {
			set[p] = true
		}
	}
	return set
}

// restoreSession recreates one saved session: its windows (name + layout),
// panes (cwd), and any allowed running programs. Returns true if the
// session was created.
func restoreSession(s ResurrectSession, allowed map[string]bool) bool {
	if len(s.Windows) == 0 {
		// No detailed layout (e.g. legacy state): just recreate the
		// bare session at its saved path.
		return tmuxRun("new-session", "-d", "-s", s.Name, "-c", firstNonEmpty(s.Path, "~")) == nil
	}
	for i, w := range s.Windows {
		paneZeroPath := firstNonEmpty(windowFirstPath(w), s.Path, "~")
		if i == 0 {
			if err := tmuxRun("new-session", "-d", "-s", s.Name, "-n", w.Name, "-c", paneZeroPath); err != nil {
				return false
			}
		} else {
			_ = tmuxRun("new-window", "-t", s.Name+":", "-n", w.Name, "-c", paneZeroPath)
		}
		restoreWindowPanes(s.Name, w, allowed)
	}
	return true
}

// restoreWindowPanes splits the (already-created, currently-active) window
// to recreate its panes, applies the saved layout, and re-runs any allowed
// programs. Targets the session's active window via "<name>:" — restore
// builds one window to completion before moving to the next.
func restoreWindowPanes(session string, w ResurrectWindow, allowed map[string]bool) {
	target := session + ":"
	for i := 1; i < len(w.Panes); i++ {
		_ = tmuxRun("split-window", "-t", target, "-c", firstNonEmpty(w.Panes[i].Path, "~"))
	}
	if w.Layout != "" {
		_ = tmuxRun("select-layout", "-t", target, w.Layout)
	}
	// Map creation-order pane ids so we can address each pane reliably
	// regardless of pane-base-index.
	ids, _ := tmuxRunLines("list-panes", "-t", target, "-F", "#{pane_id}")
	for i, p := range w.Panes {
		if i >= len(ids) {
			break
		}
		if p.Command != "" && allowed[p.Command] {
			_ = tmuxRun("send-keys", "-t", ids[i], p.Command, "Enter")
		}
	}
}

func windowFirstPath(w ResurrectWindow) string {
	if len(w.Panes) > 0 {
		return w.Panes[0].Path
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
