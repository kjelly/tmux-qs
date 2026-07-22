package main

import "testing"

func TestStartCurrentSnippetPickerUsesCurrentSession(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-startup-snippet"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	m := newModel(false, false, false, true)
	m.currentSession = session
	m.items = []string{"another-session"}
	if err := m.startCurrentSnippetPicker(); err != nil {
		t.Fatal(err)
	}
	if m.mode != modeSnippetSelect {
		t.Fatalf("mode = %v, want snippet picker", m.mode)
	}
	if m.snippetTarget.session != session {
		t.Fatalf("target session = %q, want %q", m.snippetTarget.session, session)
	}
	if len(m.snippetChoices) == 0 {
		t.Fatal("expected global snippets for current pane")
	}
}

func TestInitialLoadOpensCurrentSnippetPicker(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-initial-snippet"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	m := newModel(false, false, false, true)
	m.currentSession = session
	updated, _ := m.Update(itemsMsg{
		src:   srcDefault,
		items: []string{session},
		info:  tmuxSessionInfo(),
	})
	got := updated.(model)
	if got.mode != modeSnippetSelect {
		t.Fatalf("mode = %v, want snippet picker", got.mode)
	}
	if got.snippetTarget.session != session {
		t.Fatalf("target session = %q, want %q", got.snippetTarget.session, session)
	}
}
