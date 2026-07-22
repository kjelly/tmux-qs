package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestVimModeDisabledByDefault(t *testing.T) {
	m := newModel()
	if m.vimEnabled {
		t.Error("expected vimEnabled to be false by default")
	}

	// Without --vim, Esc quits instead of entering vim normal mode.
	m.vimMode = vimInsert
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected Esc to return a quit command")
	}
}

func TestVimModeEnabledExplicitly(t *testing.T) {
	m := newModel(false, false, true)
	m.vimMode = vimInsert
	res, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if res.(model).vimMode != vimNormal {
		t.Error("expected Esc to enter vim normal mode when --vim is enabled")
	}
	if cmd != nil {
		t.Errorf("expected no command while entering vim normal mode, got %v", cmd)
	}
}

func TestVimModeDisabledExplicitly(t *testing.T) {
	// Create model with vimEnabled = false
	m := newModel(false, false, false)
	if m.vimEnabled {
		t.Error("expected vimEnabled to be false when configured")
	}

	// Pressing esc should quit instead of toggling to vimNormal
	m.vimMode = vimInsert
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected a cmd to be returned")
	}
	// Check if cmd is a tea.Quit msg
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestVimModeRestoreOverriddenWhenDisabled(t *testing.T) {
	m := newModel(false, false, false)
	lv := lastView{
		VimMode: int(vimNormal),
	}
	applyLastView(&m, lv)
	if m.vimMode != vimInsert {
		t.Errorf("expected vimMode to be overridden to vimInsert when vimEnabled is false, got %v", m.vimMode)
	}
}
