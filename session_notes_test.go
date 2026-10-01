package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func newSessionNoteTestModel(t *testing.T) model {
	t.Helper()
	withCleanCacheEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	resetConfigCache()
	t.Cleanup(resetConfigCache)

	m := model{
		input:        textinput.New(),
		mode:         modeList,
		src:          srcDefault,
		items:        []string{"work"},
		annots:       map[string]string{},
		dirty:        map[string]bool{},
		sessionPaths: map[string]string{"work": "/tmp/work"},
		sessionInfo:  map[string]sessionInfo{"work": {path: "/tmp/work"}},
		sessionNotes: map[string]string{},
		noteEditor:   newSessionNoteEditor(),
		styles:       newStyleBundle(StyleConfig{}),
		width:        80,
		height:       24,
		fuzzy:        newFuzzyEngine(),
	}
	m.refilter()
	return m
}

func TestSessionNotesRoundTripAndClear(t *testing.T) {
	withCleanCacheEnv(t)

	key := sessionNoteKey("work", tmuxServerSpec{})
	if err := writeSessionNote(key, "  first line\r\nsecond line  "); err != nil {
		t.Fatalf("write note: %v", err)
	}
	got := loadSessionNotes()
	if got[key] != "first line\nsecond line" {
		t.Fatalf("loaded note = %q, want normalized multiline note", got[key])
	}

	if err := writeSessionNote(key, " \t "); err != nil {
		t.Fatalf("clear note: %v", err)
	}
	if got := loadSessionNotes(); len(got) != 0 {
		t.Fatalf("cleared notes = %#v, want empty", got)
	}
}

func TestSessionNoteKeySeparatesServers(t *testing.T) {
	defaultKey := sessionNoteKey("work", tmuxServerSpec{})
	otherKey := sessionNoteKey("work", tmuxServerSpec{flag: "-L", value: "other"})
	if defaultKey == otherKey {
		t.Fatal("notes for different tmux servers must not share a key")
	}
}

func TestSessionNoteEditorSavesAndReportsInList(t *testing.T) {
	m := newSessionNoteTestModel(t)

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = updated.(model)
	if m.mode != modeSessionNote || m.noteSession != "work" {
		t.Fatalf("Ctrl-l opened mode=%v session=%q, want note editor for work", m.mode, m.noteSession)
	}

	m.noteEditor.SetValue("remember the deploy window")
	updated, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil || m.mode != modeList {
		t.Fatalf("Enter should save and return to list: mode=%v cmd=%v", m.mode, cmd)
	}

	msg := cmd()
	saved, _ := m.Update(msg)
	m = saved.(model)
	key := sessionNoteKey("work", tmuxServerSpec{})
	if m.sessionNotes[key] != "remember the deploy window" {
		t.Fatalf("in-memory note = %q", m.sessionNotes[key])
	}
	if got := loadSessionNotes()[key]; got != "remember the deploy window" {
		t.Fatalf("persisted note = %q", got)
	}
	if m.noteConfirm == "" {
		t.Fatal("successful save should show a confirmation")
	}
}

func TestSessionNoteEditorEscCancelsAndRestoresFilter(t *testing.T) {
	m := newSessionNoteTestModel(t)
	m.input.SetValue("work")
	m.refilter()
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = updated.(model)
	m.noteEditor.SetValue("discard me")

	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.mode != modeList || m.input.Value() != "work" {
		t.Fatalf("Esc did not restore list/filter: mode=%v input=%q", m.mode, m.input.Value())
	}
	if len(loadSessionNotes()) != 0 {
		t.Fatal("cancelled note must not be persisted")
	}
}

func TestSessionNoteSnippetActionUsesEnterPath(t *testing.T) {
	m := newSessionNoteTestModel(t)
	m.mode = modeSnippetSelect
	m.items = []string{sessionNoteSnippet}
	m.snippetChoices = []SnippetConfig{sessionNoteSnippetChoice()}
	m.snippetTarget = paneTarget{session: "work", server: tmuxServerSpec{}}
	m.refilter()

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd != nil || m.mode != modeSessionNote || m.noteSession != "work" {
		t.Fatalf("snippet note action opened mode=%v session=%q cmd=%v", m.mode, m.noteSession, cmd)
	}
}

func TestSessionNoteVisibleInRowAndPreview(t *testing.T) {
	m := newSessionNoteTestModel(t)
	m.sessionNotes[sessionNoteKey("work", tmuxServerSpec{})] = "ship after review"
	m.previewEntry = "work"
	m.previewContent = "Pane Output:\n  ready"

	view := m.View()
	if !strings.Contains(view, "📝") {
		t.Fatalf("row should show note marker:\n%s", view)
	}
	if !strings.Contains(view, "Session Note:") || !strings.Contains(view, "ship after review") {
		t.Fatalf("wide preview should show session note:\n%s", view)
	}
}
