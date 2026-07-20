package main

import "testing"

func TestMatchingSnippetsFiltersByForegroundCommand(t *testing.T) {
	snippets := []SnippetConfig{
		{Name: "Claude", Commands: []string{"claude", "codex"}, Text: "/continue"},
		{Name: "Shell", Commands: []string{"zsh"}, Text: "git status"},
		{Name: "Universal", Text: "help"},
		{Name: "Missing text", Commands: []string{"claude"}},
	}
	got := matchingSnippets(snippets, "Claude")
	if len(got) != 2 || got[0].Name != "Claude" || got[1].Name != "Universal" {
		t.Fatalf("matchingSnippets() = %#v, want Claude + Universal", got)
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

func TestChooseSnippetOpensConfirmation(t *testing.T) {
	m := newModel()
	m.mode = modeSnippetSelect
	m.items = []string{"Continue"}
	m.snippetChoices = []SnippetConfig{{Name: "Continue", Text: "/continue", Submit: true}}
	m.refilter()

	next, _ := m.choose()
	got := next.(model)
	if got.mode != modeSnippetConfirm {
		t.Fatalf("mode = %v, want modeSnippetConfirm", got.mode)
	}
	if got.selectedSnippet.Name != "Continue" {
		t.Fatalf("selected snippet = %#v", got.selectedSnippet)
	}
}
