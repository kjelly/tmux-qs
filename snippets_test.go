package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMatchingSnippetsFiltersByForegroundCommand(t *testing.T) {
	snippets := []SnippetConfig{
		{Name: "Claude", Commands: []string{"claude", "codex"}, Text: "/continue", Favorite: true},
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

func TestMatchingSnippetsPutsFavoritesFirst(t *testing.T) {
	snippets := []SnippetConfig{
		{Name: "Normal", Text: "normal"},
		{Name: "Favorite", Text: "favorite", Favorite: true},
		{Name: "Favorite 2", Text: "favorite 2", Favorite: true},
	}
	got := matchingSnippets(snippets, "claude")
	if got[0].Name != "Favorite" || got[1].Name != "Favorite 2" || got[2].Name != "Normal" {
		t.Fatalf("favorites should be stable and first, got %#v", got)
	}
}

func TestSnippetConfigParsesFromTOML(t *testing.T) {
	cfg, err := parseConfigBytes([]byte(`
[[snippet]]
name = "Continue"
commands = ["claude"]
text = "/continue"
submit = true
favorite = true
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Snippets) != 1 || cfg.Snippets[0].Name != "Continue" || !cfg.Snippets[0].Submit || !cfg.Snippets[0].Favorite {
		t.Fatalf("parsed snippets = %#v", cfg.Snippets)
	}
}

func TestDefaultSnippetsIncludeGamepadAgentActions(t *testing.T) {
	got := matchingSnippets(defaultSnippets(), "claude")
	want := map[string]bool{"Continue": false, "Review changes": false, "Run tests": false}
	for _, snippet := range got {
		if _, ok := want[snippet.Name]; ok {
			want[snippet.Name] = snippet.Submit && snippet.Favorite
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("default snippets missing gamepad action %q or it is not favorite+submit", name)
		}
	}
}

func TestDefaultSnippetsIncludeBlocks(t *testing.T) {
	snippets := defaultSnippets()
	hasBlock := false
	for _, s := range snippets {
		if strings.HasPrefix(s.Name, "Block: ") {
			hasBlock = true
			break
		}
	}
	if !hasBlock {
		t.Fatalf("expected defaultSnippets to contain Block: items")
	}
}

func TestRecentSnippetChoicesUseNewestUniquePrompts(t *testing.T) {
	got := recentSnippetChoices([]string{"old", "repeat", "new", "repeat"}, 2)
	if len(got) != 2 || got[0].Name != "Recent: repeat" || got[1].Name != "Recent: new" {
		t.Fatalf("recent snippets = %#v, want newest unique prompts", got)
	}
	if got[0].Text != "repeat" || !got[0].Submit || got[0].Favorite {
		t.Fatalf("recent snippet metadata = %#v", got[0])
	}
}

func TestRankSnippetsByFavoriteThenUsage(t *testing.T) {
	snippets := []SnippetConfig{
		{Name: "Rare", Text: "rare"},
		{Name: "Often", Text: "often"},
		{Name: "Fav", Text: "fav", Favorite: true},
	}
	got := rankSnippets(snippets, []string{"often", "fav", "often", "often", "rare"})
	if got[0].Name != "Fav" || got[1].Name != "Often" || got[2].Name != "Rare" {
		t.Fatalf("ranked snippets = %#v, want favorite then usage", got)
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

func TestExtractContextSnippets(t *testing.T) {
	preview := `
Building package...
Do you want to proceed? [y/N]
FAIL: TestRun
`
	clipboard := "git checkout main"
	snippets := extractContextSnippets(preview, clipboard, "claude", nil)

	var names []string
	for _, s := range snippets {
		names = append(names, s.Name)
	}

	foundY := false
	foundFix := false
	foundClip := false
	for _, n := range names {
		if n == "Quick Response: y" {
			foundY = true
		}
		if n == "Fix: TestRun" {
			foundFix = true
		}
		if len(n) >= 15 && n[:15] == "Paste Clipboard" {
			foundClip = true
		}
	}

	if !foundY || !foundFix || !foundClip {
		t.Fatalf("expected y, fix, and clipboard snippets, got: %v", names)
	}
}

func TestExtractContextSnippetsWithCustomRules(t *testing.T) {
	preview := `
Line 1: 發生系統錯誤
Line 2: FAIL: TestAuth
`
	rules := []DynamicSnippetConfig{
		{
			Name:    "檢視: $1",
			Matches: []string{"錯誤", "error"},
			Text:    "inspect error $1",
			Submit:  true,
		},
		{
			Name:    "Fix Test: $1",
			Matches: []string{`FAIL:\s+(\w+)`},
			Text:    "fix $1",
			Submit:  true,
		},
	}

	snippets := extractContextSnippets(preview, "", "zsh", rules)
	foundError := false
	foundFix := false
	for _, s := range snippets {
		if strings.HasPrefix(s.Name, "檢視:") {
			foundError = true
		}
		if s.Name == "Fix Test: TestAuth" && s.Text == "fix TestAuth" {
			foundFix = true
		}
	}
	if !foundError || !foundFix {
		t.Fatalf("expected custom error and fix snippets, got: %#v", snippets)
	}
}

func TestFilterSnippetsByCategory(t *testing.T) {
	snippets := []SnippetConfig{
		{Name: "Quick Response: y", Text: "y"},
		{Name: "Block: Prefix Fix", Text: "Fix "},
		{Name: "Continue", Commands: []string{"claude"}},
	}
	quick := filterSnippetsByCategory(snippets, "Quick")
	if len(quick) != 1 || quick[0].Name != "Quick Response: y" {
		t.Fatalf("expected 1 Quick snippet, got: %v", quick)
	}

	blocks := filterSnippetsByCategory(snippets, "Blocks")
	if len(blocks) != 1 || blocks[0].Name != "Block: Prefix Fix" {
		t.Fatalf("expected 1 Block snippet, got: %v", blocks)
	}
}
