package main

import (
	"fmt"
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
		if strings.TrimSpace(snippet.Name) == "" || snippet.Text == "" || !snippetMatchesCommand(snippet, command) {
			continue
		}
		out = append(out, snippet)
	}
	return out
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

type snippetSendMsg struct{ err error }

func snippetSendCmd(target paneTarget, snippet SnippetConfig) tea.Cmd {
	return func() tea.Msg {
		return snippetSendMsg{err: sendTextToPane(target.paneID, snippet.Text, snippet.Submit)}
	}
}

func (m *model) startSnippetPicker() error {
	selected, ok := m.selected()
	if !ok {
		return fmt.Errorf("select a session or pane first")
	}
	target, err := resolvePaneTarget(selected)
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

func (m *model) leaveSnippetPicker() {
	m.items = m.savedItems
	m.mode = modeList
	m.snippetTarget = paneTarget{}
	m.snippetChoices = nil
	m.selectedSnippet = SnippetConfig{}
	m.input.SetValue("")
	m.refilter()
}
