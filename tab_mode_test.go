package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTabTogglesSessionAndWindowModes(t *testing.T) {
	m := newModel()
	m.src = srcDefault
	m.input.SetValue("keep no query")

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.src != srcTmux || !m.loading || cmd == nil {
		t.Fatalf("Tab from session mode = src=%v loading=%v cmd=%v; want window mode reload", m.src, m.loading, cmd)
	}
	if m.input.Value() != "" {
		t.Fatalf("mode switch should clear filter, got %q", m.input.Value())
	}

	next, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.src != srcDefault || !m.loading || cmd == nil {
		t.Fatalf("Tab from window mode = src=%v loading=%v cmd=%v; want session mode reload", m.src, m.loading, cmd)
	}
}

func TestTabMovesInTransientPickers(t *testing.T) {
	m := newModel()
	m.mode = modeSnippetSelect
	m.items = []string{"one", "two"}
	m.refilter()

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.cursor != 1 || m.src != srcDefault {
		t.Fatalf("Tab in snippet picker should move cursor, got cursor=%d src=%v", m.cursor, m.src)
	}
}
