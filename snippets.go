package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// paneTarget is the exact pane a snippet will be sent to. Keeping the pane ID
// rather than a session/window target avoids tmux's active-pane fallback
// changing between selection and confirmation.
type paneTarget struct {
	session string
	window  string
	index   string
	paneID  string
	command string
}

func (p paneTarget) label() string {
	if p.session == "" {
		return p.paneID
	}
	if p.window == "" {
		return p.session
	}
	return fmt.Sprintf("%s:%s.%s", p.session, p.window, p.index)
}

// resolvePaneTarget turns a list row into the current concrete target pane.
// Pane/window/tmux rows carry a tab envelope whose third field is already a
// pane ID; session rows intentionally resolve to that session's active pane.
func resolvePaneTarget(selected string) (paneTarget, error) {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return paneTarget{}, fmt.Errorf("no pane selected")
	}
	tmuxTarget := paneLookupTarget(selected)
	format := "#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}"
	out, err := tmuxRunOut("display-message", "-p", "-t", tmuxTarget, format)
	if err != nil {
		return paneTarget{}, err
	}
	fields := strings.Split(out, "\t")
	if len(fields) < 5 || strings.TrimSpace(fields[3]) == "" {
		return paneTarget{}, fmt.Errorf("cannot resolve target pane")
	}
	return paneTarget{
		session: fields[0], window: fields[1], index: fields[2],
		paneID: fields[3], command: fields[4],
	}, nil
}

// paneLookupTarget extracts the exact pane ID embedded by pane/window/tmux
// sources. This is the critical distinction from the old send path, which
// accidentally passed the human-readable display text to tmux.
func paneLookupTarget(selected string) string {
	parts := strings.Split(selected, "\t")
	if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
		return strings.TrimSpace(parts[2])
	}
	return strings.TrimSpace(selected)
}

func hasPaneEnvelope(selected string) bool {
	parts := strings.Split(selected, "\t")
	return len(parts) >= 3 && strings.TrimSpace(parts[2]) != ""
}

func snippetMatchesCommand(s SnippetConfig, command string) bool {
	if len(s.Commands) == 0 {
		return true
	}
	for _, candidate := range s.Commands {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(command)) {
			return true
		}
	}
	return false
}

func matchingSnippets(snippets []SnippetConfig, command string) []SnippetConfig {
	out := make([]SnippetConfig, 0, len(snippets))
	for _, snippet := range snippets {
		if strings.TrimSpace(snippet.Name) == "" || (snippet.Text == "" && len(snippet.Keys) == 0) || !snippetMatchesCommand(snippet, command) {
			continue
		}
		out = append(out, snippet)
	}
	return out
}

// defaultSnippets mirrors the useful pane inputs from ~/bin/fzf-send-keys.nu:
// shared terminal controls plus foreground-program-specific commands. The
// script's assist.nu entry deliberately is not included: it runs a local
// program rather than sending an input to the selected pane.
func defaultSnippets() []SnippetConfig {
	key := func(name, value string, commands ...string) SnippetConfig {
		return SnippetConfig{Name: name, Commands: commands, Keys: []string{value}}
	}
	text := func(name, value string, commands ...string) SnippetConfig {
		return SnippetConfig{Name: name, Commands: commands, Text: value}
	}
	all := []SnippetConfig{
		key("Ctrl-A", "C-a"), key("Ctrl-C", "C-c"), key("Ctrl-D", "C-d"),
		key("Ctrl-N", "C-n"), key("Ctrl-P", "C-p"), key("Ctrl-Q", "C-q"),
		key("Ctrl-Z", "C-z"), key("Alt-Z", "M-z"),
	}
	addText := func(command string, values ...string) {
		for _, value := range values {
			all = append(all, text(command+": "+value, value, command))
		}
	}
	addKey := func(command string, values ...string) {
		for _, value := range values {
			all = append(all, key(command+": "+value, value, command))
		}
	}
	addKey("nvim", "ZZ", "M-h", "M-j", "M-k", "M-l", "M-;")
	addText("opencode", "do it")
	addKey("opencode", "C-p")
	addText("ollama", "do it")
	addKey("ollama", "C-p")
	for _, command := range []string{"codex", "claude", "crush"} {
		values := []string{"/help", "/model", "/compact", "/clear", "/status"}
		if command == "codex" {
			values = []string{"/help", "/model", "/review", "/status", "/new", "/compact", "/diff", "/side"}
		}
		if command == "claude" {
			values = append(values, "/config", "/memory")
		}
		if command == "crush" {
			values = []string{"/help", "/model", "/provider", "/new", "/clear", "/compact", "/status"}
		}
		addText(command, values...)
		addKey(command, "C-c", "C-l")
	}
	return all
}

// sendTextToPane uses -l so a snippet that happens to resemble a tmux key
// name (for example "Enter") is inserted as text. Submit is a separate,
// explicit keypress so text-only snippets never execute accidentally.
func sendTextToPane(target, text string, submit bool) error {
	if err := tmuxRun("send-keys", "-l", "-t", target, text); err != nil {
		return err
	}
	if submit {
		return tmuxRun("send-keys", "-t", target, "Enter")
	}
	return nil
}

func sendSnippetToPane(target string, snippet SnippetConfig) error {
	if len(snippet.Keys) > 0 {
		args := append([]string{"send-keys", "-t", target}, snippet.Keys...)
		if err := tmuxRun(args...); err != nil {
			return err
		}
	} else if err := sendTextToPane(target, snippet.Text, false); err != nil {
		return err
	}
	if snippet.Submit {
		return tmuxRun("send-keys", "-t", target, "Enter")
	}
	return nil
}

type snippetSendMsg struct {
	err        error
	closeAfter bool
}

func snippetSendCmd(target paneTarget, snippet SnippetConfig, closeAfter bool) tea.Cmd {
	return func() tea.Msg {
		return snippetSendMsg{err: sendSnippetToPane(target.paneID, snippet), closeAfter: closeAfter}
	}
}

// sendSelectedSnippet starts delivery from the snippet list. Enter closes the
// picker after a successful send; Space keeps it open for repeated sends.
func (m model) sendSelectedSnippet(idx int, closeAfter bool) (tea.Model, tea.Cmd) {
	if m.loading || idx < 0 || idx >= len(m.snippetChoices) {
		return m, nil
	}
	m.selectedSnippet = m.snippetChoices[idx]
	m.loading = true
	m.errText = ""
	return m, snippetSendCmd(m.snippetTarget, m.selectedSnippet, closeAfter)
}

// startSnippetPicker opens snippets for either the current workspace (Space)
// or the cursor's explicit destination (Ctrl-s).
func (m *model) startSnippetPicker(useSelectedTarget bool) error {
	selected, ok := m.selected()
	if !ok {
		return fmt.Errorf("select a session or pane first")
	}
	targetEntry := selected
	if !useSelectedTarget {
		targetEntry = m.snippetTargetEntry(selected)
	}
	return m.startSnippetPickerForTarget(targetEntry)
}

// startCurrentSnippetPicker opens snippets for the current session's active
// pane, regardless of a restored list cursor or selected pane row.
func (m *model) startCurrentSnippetPicker() error {
	if callerPane := strings.TrimSpace(os.Getenv(popupPaneEnv)); callerPane != "" {
		return m.startSnippetPickerForTarget(callerPane)
	}
	if m.currentSession == "" {
		return fmt.Errorf("cannot determine current tmux session")
	}
	return m.startSnippetPickerForTarget(m.currentSession)
}

func (m *model) startSnippetPickerForTarget(targetEntry string) error {
	target, err := resolvePaneTarget(targetEntry)
	if err != nil {
		return fmt.Errorf("cannot inspect target pane: %w", err)
	}
	choices := matchingSnippets(m.cfg().Snippets, target.command)
	if len(choices) == 0 {
		return fmt.Errorf("no snippets for %s", target.command)
	}
	m.savedItems = m.items
	m.snippetTarget = target
	m.snippetChoices = choices
	// Capture once when the picker opens. This is intentionally a snapshot:
	// it makes the target visible without adding a polling fork while the user
	// browses snippets, and the pane ID remains pinned through confirmation.
	if preview, err := tmuxRunOut("capture-pane", "-p", "-t", target.paneID, "-S", "-20"); err == nil {
		m.snippetPreview = sanitizePanePreview(preview)
	} else {
		m.snippetPreview = "(pane preview unavailable)"
	}
	m.items = make([]string, len(choices))
	for i, snippet := range choices {
		m.items[i] = snippet.Name
	}
	m.mode = modeSnippetSelect
	m.input.SetValue("")
	m.errText = ""
	m.refilter()
	return nil
}

// snippetTargetEntry separates navigation from delivery. In ordinary session
// lists the cursor is a prospective switch destination, while snippets should
// continue to reach the workspace the user is currently working in. Pane rows
// are explicit delivery targets and therefore always win.
func (m model) snippetTargetEntry(selected string) string {
	if hasPaneEnvelope(selected) {
		return selected
	}
	if m.currentSession != "" {
		return m.currentSession
	}
	return selected
}

func (m *model) leaveSnippetPicker() {
	m.items = m.savedItems
	m.mode = modeList
	m.snippetTarget = paneTarget{}
	m.snippetChoices = nil
	m.selectedSnippet = SnippetConfig{}
	m.snippetPreview = ""
	m.input.SetValue("")
	m.refilter()
}
