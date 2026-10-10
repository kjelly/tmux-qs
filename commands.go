package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func executeCommand(cmd string) error {
	switch cmd {
	case "Tmux: Detach Client":
		return tmuxRun("detach-client")
	case "Tmux: Detach Other Clients":
		return tmuxRun("detach-client", "-a")
	case "Tmux: Kill Server (Danger)":
		return tmuxRun("kill-server")
	case "Tmux: Reload Tmux Config":
		return tmuxRun("source-file", expandPath("~/.tmux.conf"))
	case "Tmux: Create Eink Session for Current":
		return createEinkSessionForCurrent()
	case "Tmux: Toggle Eink Session":
		return toggleEinkSession(currentSessionName())
	case "Tmux: Force Current Client as Eink":
		return setEinkClientForced(true)
	case "Tmux: Clear Current Client Eink Override":
		return setEinkClientForced(false)
	case "Tmux-QS: Open Config File":
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "nvim"
		}
		path := expandPath("~/.config/tmux-qs/config.toml")
		if os.Getenv("TMUX") == "" {
			c := exec.Command(editor, path)
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		}
		return tmuxRun("new-window", "-n", "config", shellCommandWithArg(editor, path))
	case "Resurrect: Save Workspace State":
		return resurrectSave()
	case "Resurrect: Restore Workspace State":
		return resurrectRestore()
	}

	cfg := loadConfig()
	for _, c := range cfg.Commands {
		if c.Name == cmd {
			execCmd := c.Cmd
			currSess := currentSessionName()
			if currSess == "" {
				currSess = "qs"
			}
			if strings.Contains(execCmd, "{session}") {
				execCmd = strings.ReplaceAll(execCmd, "{session}", currSess)
			}
			if strings.Contains(execCmd, "{path}") {
				execCmd = strings.ReplaceAll(execCmd, "{path}", currentSessionPath())
			}

			if os.Getenv("TMUX") == "" {
				shCmd := exec.Command("sh", "-c", execCmd)
				shCmd.Stdin = os.Stdin
				shCmd.Stdout = os.Stdout
				shCmd.Stderr = os.Stderr
				err := shCmd.Run()
				if strings.Contains(execCmd, "tmux") {
					_ = switchOrAttach(currSess)
				}
				return err
			}
			return runAction("sh", "-c", execCmd)
		}
	}
	return nil
}

func currentSessionPath() string {
	path, err := tmuxRunOut("display-message", "-p", "#{session_path}")
	if err != nil {
		return ""
	}
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
	Index       int             `json:"index"`
	Name        string          `json:"name"`
	Layout      string          `json:"layout"` // tmux #{window_layout} string
	Panes       []ResurrectPane `json:"panes"`
	layoutError error
}

type ResurrectPane struct {
	Path     string `json:"path"`
	Command  string `json:"command"` // foreground program at save time
	ID       string `json:"id,omitempty"`
	Index    *int   `json:"index,omitempty"`
	Floating bool   `json:"floating,omitzero"`
	Width    int    `json:"width,omitzero"`
	Height   int    `json:"height,omitzero"`
	X        int    `json:"x,omitzero"`
	Y        int    `json:"y,omitzero"`
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
		"#{pane_floating_flag}", "#{@tmux_qs}",
		"#{pane_id}", "#{pane_width}", "#{pane_height}", "#{pane_left}", "#{pane_top}",
	}, "\t")
	lines, err := tmuxRunLines("list-panes", "-a", "-F", format)
	if err != nil {
		return ResurrectState{}, err
	}
	sessions := buildResurrectSessions(lines)
	for _, s := range sessions {
		for _, w := range s.Windows {
			if w.layoutError != nil {
				return ResurrectState{}, fmt.Errorf("snapshot %s:%d: %w", s.Name, w.Index, w.layoutError)
			}
		}
	}
	return ResurrectState{Sessions: sessions}, nil
}

// captureResurrectSession snapshots a single live session by name, for the
// kill-undo buffer. Returns false when the session has no panes (e.g. it
// has already gone away).
func captureResurrectSession(name string) (ResurrectSession, bool) {
	return captureResurrectSessionOn(name, getTmuxServer())
}

func captureResurrectSessionOn(name string, server tmuxServerSpec) (ResurrectSession, bool) {
	format := strings.Join([]string{
		"#{session_name}", "#{session_path}",
		"#{window_index}", "#{window_name}", "#{window_layout}",
		"#{pane_index}", "#{pane_current_path}", "#{pane_current_command}",
		"#{pane_floating_flag}", "#{@tmux_qs}",
		"#{pane_id}", "#{pane_width}", "#{pane_height}", "#{pane_left}", "#{pane_top}",
	}, "\t")
	lines, err := tmuxRunLinesOn(server, "list-panes", "-s", "-t", name, "-F", format)
	if err != nil {
		return ResurrectSession{}, false
	}
	sessions := buildResurrectSessions(lines)
	if len(sessions) == 0 {
		return ResurrectSession{}, false
	}
	for _, w := range sessions[0].Windows {
		if w.layoutError != nil {
			return ResurrectSession{}, false
		}
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
		if len(p) >= 10 && isInternalTmuxQSPane(p[9]) {
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
		pane := ResurrectPane{Path: panePath, Command: paneCmd}
		if len(p) >= 10 {
			pane.Floating = p[8] == "1"
		}
		if len(p) >= 15 {
			idx, err := strconv.Atoi(p[5])
			if err == nil {
				pane.Index = &idx
			}
			pane.ID = p[10]
			pane.Width, _ = strconv.Atoi(p[11])
			pane.Height, _ = strconv.Atoi(p[12])
			pane.X, _ = strconv.Atoi(p[13])
			pane.Y, _ = strconv.Atoi(p[14])
		}
		w.Panes = append(w.Panes, pane)
	}

	var out []ResurrectSession
	for _, sName := range sessionOrder {
		s := sessions[sName]
		for _, wIdx := range winOrder[sName] {
			w := windows[winKey{sName, wIdx}]
			slices.SortStableFunc(w.Panes, func(a, b ResurrectPane) int {
				if a.Index == nil || b.Index == nil {
					return 0
				}
				return cmp.Compare(*a.Index, *b.Index)
			})
			// New layouts include temporary floating pickers. Remove their
			// cells as well as their metadata so select-layout stays valid.
			if layout, err := filterResurrectLayout(w.Layout, w.Panes); err == nil {
				w.Layout = layout
			} else {
				w.layoutError = err
			}
			s.Windows = append(s.Windows, *w)
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
		if err := restoreSessionDetailedOn(s, allowed, getTmuxServer()); err != nil {
			return fmt.Errorf("restore %s: %w", s.Name, err)
		}
		count++
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
	return restoreSessionOn(s, allowed, getTmuxServer())
}

func restoreSessionOn(s ResurrectSession, allowed map[string]bool, server tmuxServerSpec) bool {
	return restoreSessionDetailedOn(s, allowed, server) == nil
}

// Detailed restore fails before creating a session when the target cannot
// represent a snapshot, and rolls back only the session it created on error.
func restoreSessionDetailedOn(s ResurrectSession, allowed map[string]bool, server tmuxServerSpec) error {
	return restoreSessionWithRunner(s, allowed, server, realCapabilityRunner{})
}

func restoreSessionWithRunner(s ResurrectSession, allowed map[string]bool, server tmuxServerSpec, runner capabilityRunner) (err error) {
	created := false
	defer func() {
		if err != nil && created {
			if _, cleanupErr := runner.runOut(server, "kill-session", "-t", "="+s.Name); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback session: %w", cleanupErr))
			}
		}
	}()
	if len(s.Windows) == 0 {
		_, err = runner.runOut(server, "new-session", "-d", "-s", s.Name, "-c", firstNonEmpty(s.Path, "~"))
		return err
	}
	for i, w := range s.Windows {
		path := firstNonEmpty(windowFirstPath(w), s.Path, "~")
		var args []string
		if i == 0 {
			args = []string{"new-session", "-d", "-s", s.Name, "-n", w.Name, "-c", path}
		} else {
			args = []string{"new-window", "-d", "-t", "=" + s.Name + ":" + strconv.Itoa(w.Index), "-n", w.Name, "-c", path}
		}
		args = append(args, "-P", "-F", "#{window_id}\t#{pane_id}\t#{window_index}")
		out, createErr := runner.runOut(server, args...)
		if createErr != nil {
			return fmt.Errorf("create window %s: %w", w.Name, createErr)
		}
		if i == 0 {
			created = true
		}
		fields := strings.Split(strings.TrimSpace(out), "\t")
		if len(fields) != 3 || fields[0] == "" || fields[1] == "" {
			return errors.New("create window returned no window/pane ID")
		}
		if i == 0 {
			caps := getTmuxCapabilities(server)
			// Formats on the newly created window reflect the running server,
			// even if a socket was restarted with a different tmux version.
			if layout, probeErr := runner.runOut(server, "display-message", "-p", "-t", fields[0], "#{window_layout}"); probeErr != nil {
				return fmt.Errorf("probe target layout: %w", probeErr)
			} else if layout != "" {
				var parsed resurrectLayout
				caps.JSONLayout = json.Unmarshal([]byte(layout), &parsed) == nil && parsed.Version == 2
			}
			for _, w := range s.Windows {
				if strings.HasPrefix(strings.TrimSpace(w.Layout), "{") && !caps.JSONLayout {
					return errors.New("JSON layout requires tmux 3.8; restore on a compatible server")
				}
				for _, p := range w.Panes {
					if p.Floating && !caps.FloatingPane {
						return errors.New("snapshot contains floating panes unsupported by target server")
					}
				}
			}
		}
		if i == 0 && fields[2] != strconv.Itoa(w.Index) {
			if _, moveErr := runner.runOut(server, "move-window", "-s", fields[0], "-t", "="+s.Name+":"+strconv.Itoa(w.Index)); moveErr != nil {
				return fmt.Errorf("restore window index: %w", moveErr)
			}
		}
		if err := restoreWindowWithRunner(fields[0], fields[1], w, allowed, server, runner); err != nil {
			return fmt.Errorf("window %s: %w", w.Name, err)
		}
	}
	return nil
}

func restoreWindowWithRunner(target, firstID string, w ResurrectWindow, allowed map[string]bool, server tmuxServerSpec, runner capabilityRunner) error {
	if len(w.Panes) == 0 {
		return nil
	}
	if w.Panes[0].Floating {
		return errors.New("snapshot has no initial tiled pane")
	}
	ids := []string{firstID}
	lastTiled := firstID
	for _, p := range w.Panes[1:] {
		args := []string{"split-window", "-d", "-t", lastTiled, "-c", firstNonEmpty(p.Path, "~")}
		if p.Floating {
			if p.Width <= 0 || p.Height <= 0 {
				return errors.New("floating pane has no saved geometry")
			}
			border, err := runner.runOut(server, "show-options", "-wv", "-t", target, "pane-border-lines")
			if err != nil {
				return fmt.Errorf("read floating border geometry: %w", err)
			}
			pad := 1
			if strings.TrimSpace(border) == "none" {
				pad = 0
			}
			args = []string{"new-pane", "-d", "-t", lastTiled, "-c", firstNonEmpty(p.Path, "~"), "-x", strconv.Itoa(p.Width + 2*pad), "-y", strconv.Itoa(p.Height + 2*pad), "-X", strconv.Itoa(p.X - pad), "-Y", strconv.Itoa(p.Y - pad)}
		}
		args = append(args, "-P", "-F", "#{pane_id}")
		out, err := runner.runOut(server, args...)
		if err != nil {
			return fmt.Errorf("create pane: %w", err)
		}
		id := strings.TrimSpace(out)
		if !paneIDRegex.MatchString(id) {
			return errors.New("create pane returned invalid ID")
		}
		ids = append(ids, id)
		if !p.Floating {
			lastTiled = id
		}
		// Keep enough space for large snapshots before applying their layout.
		if !p.Floating {
			if _, err := runner.runOut(server, "select-layout", "-t", target, "tiled"); err != nil {
				return err
			}
		}
	}
	if w.Layout != "" {
		if out, err := runner.runOut(server, "select-layout", "-t", target, w.Layout); err != nil {
			return fmt.Errorf("apply saved layout: %s: %w", out, err)
		}
	}
	for i, p := range w.Panes {
		if p.Command != "" && allowed[p.Command] {
			if _, err := runner.runOut(server, "send-keys", "-t", ids[i], "-l", p.Command); err != nil {
				return err
			}
			if _, err := runner.runOut(server, "send-keys", "-t", ids[i], "Enter"); err != nil {
				return err
			}
		}
	}
	return nil
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

func createEinkSessionForCurrent() error {
	currSession := currentSessionName()
	if currSession == "" {
		return fmt.Errorf("no active tmux session found")
	}
	return createEinkSession(currSession)
}

func createEinkSession(targetSession string) error {
	if targetSession == "" {
		return fmt.Errorf("invalid target session")
	}

	baseSession := targetSession
	if strings.HasSuffix(baseSession, "-eink") {
		baseSession = strings.TrimSuffix(baseSession, "-eink")
	}

	einkSessionName := baseSession + "-eink"

	// Check if session already exists
	sessions, err := tmuxRunLines("list-sessions", "-F", "#{session_name}")
	exists := false
	if err == nil {
		for _, s := range sessions {
			if strings.TrimSpace(s) == einkSessionName {
				exists = true
				break
			}
		}
	}

	// Create grouped session sharing windows with baseSession if it doesn't exist
	if !exists {
		if err := tmuxRun("new-session", "-d", "-t", baseSession, "-s", einkSessionName); err != nil {
			return fmt.Errorf("failed to create eink session: %w", err)
		}
	}

	return switchOrAttachExact(einkSessionName)
}

// toggleEinkSession switches between a normal workspace and its grouped
// <workspace>-eink session. The grouped session is created on demand.
func toggleEinkSession(targetSession string) error {
	targetSession = strings.TrimSpace(targetSession)
	if targetSession == "" {
		return fmt.Errorf("no active tmux session found")
	}

	base := targetSession
	if strings.HasSuffix(base, "-eink") {
		base = strings.TrimSuffix(base, "-eink")
		return switchOrAttachExact(base)
	}
	eink := base + "-eink"
	sessions, err := tmuxRunLines("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if strings.TrimSpace(session) == eink {
			return switchOrAttachExact(eink)
		}
	}
	return createEinkSession(base)
}
