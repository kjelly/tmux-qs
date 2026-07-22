package main

import "testing"

import tea "github.com/charmbracelet/bubbletea"

func TestMatchingSnippetsFiltersByForegroundCommand(t *testing.T) {
	snippets := []SnippetConfig{
		{Name: "Claude", Commands: []string{"claude", "codex"}, Text: "/continue"},
		{Name: "Shell", Commands: []string{"zsh"}, Text: "git status"},
		{Name: "Universal", Text: "help"},
		{Name: "Interrupt", Commands: []string{"claude"}, Keys: []string{"C-c"}},
		{Name: "Missing text", Commands: []string{"claude"}},
	}
	got := matchingSnippets(snippets, "Claude")
	if len(got) != 3 || got[0].Name != "Claude" || got[1].Name != "Universal" || got[2].Name != "Interrupt" {
		t.Fatalf("matchingSnippets() = %#v, want Claude + Universal + Interrupt", got)
	}
}

func TestSnippetConfigParsesFromTOML(t *testing.T) {
	cfg, err := parseConfigBytes([]byte(`
[[snippet]]
name = "Continue"
commands = ["claude"]
text = "/continue"
submit = true
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Snippets) != 1 || cfg.Snippets[0].Name != "Continue" || !cfg.Snippets[0].Submit {
		t.Fatalf("parsed snippets = %#v", cfg.Snippets)
	}
}

func TestDefaultSnippetsCoverFzfSendKeysPrograms(t *testing.T) {
	for _, command := range []string{"nvim", "opencode", "ollama", "codex", "claude", "crush"} {
		if got := matchingSnippets(defaultSnippets(), command); len(got) == 0 {
			t.Errorf("no defaults for %s", command)
		}
	}
}

func TestHasPaneEnvelope(t *testing.T) {
	if !hasPaneEnvelope("work:1.0 [claude]\twork\t%42") {
		t.Fatal("pane row should be recognized as an exact pane target")
	}
	if hasPaneEnvelope("work") {
		t.Fatal("session row must not be treated as an exact pane target")
	}
}

func TestPaneLookupTargetPrefersEmbeddedPaneID(t *testing.T) {
	row := "work:1.0 [claude] ~/repo\twork\t%42"
	if got := paneLookupTarget(row); got != "%42" {
		t.Fatalf("paneLookupTarget(%q) = %q, want %%42", row, got)
	}
	if got := paneLookupTarget("work"); got != "work" {
		t.Fatalf("paneLookupTarget(session) = %q, want work", got)
	}
}

func TestSnippetTargetUsesCurrentSessionUnlessPaneIsExplicit(t *testing.T) {
	m := model{currentSession: "current"}
	if got := m.snippetTargetEntry("previous"); got != "current" {
		t.Fatalf("session-list snippet target = %q, want current", got)
	}
	pane := "previous:1.0 [claude]\tprevious\t%42"
	if got := m.snippetTargetEntry(pane); got != pane {
		t.Fatalf("pane-list snippet target = %q, want explicit pane row", got)
	}
}

func TestSnippetEnterSendsAndClosesAfterSuccess(t *testing.T) {
	m := newModel()
	m.mode = modeSnippetSelect
	m.items = []string{"Continue"}
	m.snippetChoices = []SnippetConfig{{Name: "Continue", Text: "/continue", Submit: true}}
	m.refilter()

	next, sendCmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(model)
	if !got.loading || got.mode != modeSnippetSelect || sendCmd == nil {
		t.Fatalf("Enter should start a closing send, got loading=%v mode=%v cmd=%v", got.loading, got.mode, sendCmd)
	}
	if got.selectedSnippet.Name != "Continue" {
		t.Fatalf("selected snippet = %#v", got.selectedSnippet)
	}
	updated, quitCmd := got.Update(snippetSendMsg{closeAfter: true})
	got = updated.(model)
	if quitCmd == nil || got.mode != modeSnippetSelect {
		t.Fatalf("successful Enter send should quit from snippet list, mode=%v cmd=%v", got.mode, quitCmd)
	}
}

func TestSnippetSpaceSendsAndKeepsListOpen(t *testing.T) {
	m := newModel()
	m.mode = modeSnippetSelect
	m.items = []string{"Continue"}
	m.snippetChoices = []SnippetConfig{{Name: "Continue", Text: "/continue"}}
	m.refilter()

	next, sendCmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	got := next.(model)
	if !got.loading || sendCmd == nil {
		t.Fatal("Space should start a non-closing send")
	}
	updated, quitCmd := got.Update(snippetSendMsg{})
	got = updated.(model)
	if quitCmd == nil || got.mode != modeSnippetSelect || got.sendConfirm == "" {
		t.Fatalf("successful Space send should retain list and show confirmation, mode=%v status=%q cmd=%v", got.mode, got.sendConfirm, quitCmd)
	}
}

func TestSpaceOpensSnippetPickerForPane(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-space-snippet"
	if err := tmuxRun("new-session", "-d", "-s", session); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	m := newModel()
	m.items = []string{session}
	m.sessionPaths = map[string]string{session: "/tmp"}
	m.refilter()
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	got := updated.(model)
	if got.mode != modeSnippetSelect {
		t.Fatalf("mode = %v, want modeSnippetSelect; err=%q", got.mode, got.errText)
	}
}

func TestStartCurrentSnippetPickerPrefersPopupCallerPane(t *testing.T) {
	withTestTmuxServer(t)
	paneID, err := tmuxRunOut("display-message", "-p", "-t", "qs-test-root", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(popupPaneEnv, paneID)

	m := model{
		currentSession: "a-different-session",
		resolvedCfg: Config{Snippets: []SnippetConfig{
			{Name: "generic", Text: "hello"},
		}},
	}
	if err := m.startCurrentSnippetPicker(); err != nil {
		t.Fatal(err)
	}
	if m.snippetTarget.paneID != paneID {
		t.Fatalf("snippet pane = %q, want popup caller %q", m.snippetTarget.paneID, paneID)
	}
}
