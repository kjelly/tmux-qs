package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHelpToggleEnterAndExit verifies `?` enters help mode and Esc
// returns to list mode.
func TestHelpToggleEnterAndExit(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	if m.mode == modeHelp {
		t.Fatal("initial mode should not be help")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m2 := updated.(model)
	if m2.mode != modeHelp {
		t.Errorf("after '?', mode should be help, got %d", m2.mode)
	}
	// Esc returns to list.
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m3 := updated.(model)
	if m3.mode != modeList {
		t.Errorf("after Esc, mode should be list, got %d", m3.mode)
	}
	// Toggle off: '?' from list also exits help if we're already in it.
	updated, _ = m3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m4 := updated.(model)
	if m4.mode != modeHelp {
		t.Errorf("'?' should re-enter help, got %d", m4.mode)
	}
	updated, _ = m4.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m5 := updated.(model)
	if m5.mode != modeList {
		t.Errorf("'?' should toggle help off, got %d", m5.mode)
	}
}

// TestViewHelpRendersBindings verifies the help overlay lists key
// bindings when m.mode is modeHelp.
func TestViewHelpRendersBindings(t *testing.T) {
	m := newModel()
	m.mode = modeHelp
	m.height = 30
	m.width = 80
	view := m.View()
	for _, expect := range []string{
		"key bindings",
		"Alt-Enter",
		"Alt-n",
		"Ctrl-r",
		"Ctrl-y",
		"Ctrl-Space",
		"branch mode",
	} {
		if !strings.Contains(view, expect) {
			t.Errorf("help overlay should contain %q, got:\n%s", expect, view)
		}
	}
}

// TestMouseWheelUpDownMovesCursor verifies scroll-wheel events move
// the cursor in the picker.
func TestMouseWheelUpDownMovesCursor(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.items = []string{"a", "b", "c", "d"}
	m.filtered = []int{0, 1, 2, 3}
	m.cursor = 0
	updated, _ := m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
	})
	m2 := updated.(model)
	if m2.cursor != 1 {
		t.Errorf("wheel down should move cursor to 1, got %d", m2.cursor)
	}
	updated, _ = m2.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
	})
	m3 := updated.(model)
	if m3.cursor != 0 {
		t.Errorf("wheel up should move cursor back to 0, got %d", m3.cursor)
	}
}

// TestMouseLeftClickJumpsCursor verifies left-click in the list area
// jumps the cursor to the clicked row.
func TestMouseLeftClickJumpsCursor(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.height = 30
	m.width = 80
	m.items = []string{"a", "b", "c", "d"}
	m.filtered = []int{0, 1, 2, 3}
	m.cursor = 0
	m.inputPad = 0
	// Y=0 is blank, Y=1 is prompt, Y=2 is header, Y=3 is "a", Y=4 is "b",
	// Y=5 is "c". Clicking Y=5 should move cursor to idx 2.
	// With inputPad=0, layout is:
	//   Y=0   prompt line
	//   Y=1   header line
	//   Y=2   first list row (cursor idx 0)
	//   Y=3   second list row (cursor idx 1)
	// Clicking Y=3 should move cursor to idx 1.
	updated, _ := m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		Y:      3,
	})
	m2 := updated.(model)
	if m2.cursor != 1 {
		t.Errorf("click on row 2 should set cursor=1, got %d", m2.cursor)
	}
}
