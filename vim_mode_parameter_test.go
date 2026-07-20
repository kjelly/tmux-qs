package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestVimModeEnabledByDefault(t *testing.T) {
	// By default, vimEnabled is true
	m := newModel()
	if !m.vimEnabled {
		t.Error("expected vimEnabled to be true by default")
	}

	// Pressing esc in insert mode should toggle to vimNormal
	m.vimMode = vimInsert
	res, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	resModel := res.(model)
	if resModel.vimMode != vimNormal {
		t.Error("expected pressing Esc to toggle to vimNormal when vimEnabled is true")
	}
	if cmd != nil {
		t.Errorf("expected cmd to be nil, got %v", cmd)
	}
}

func TestVimModeDisabled(t *testing.T) {
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
