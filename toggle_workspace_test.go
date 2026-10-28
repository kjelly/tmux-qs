package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAltQKeepsPickerWhenNoWorkspaceExists(t *testing.T) {
	// Isolate tmux: the only client has no previous or other-client session.
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*client_last_session*) printf 'test-client\\twork\\t\\n' ;;\n" +
		"*client_activity*) printf '1\\ttest-client\\twork\\n' ;;\n" +
		"*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(popupClientEnv, "test-client")

	m := newModel()
	m.items = []string{"one", "two"}
	m.filtered = []int{0, 1}
	m.cursor = 1
	m.input.SetValue("search")
	key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}, Alt: true}
	updated, cmd := m.Update(key)
	m = updated.(model)
	if cmd == nil || !m.toggleSwitchPending {
		t.Fatal("Alt-q must start a workspace switch before quitting")
	}
	duplicate, duplicateCmd := m.Update(key)
	if duplicateCmd != nil || !duplicate.(model).toggleSwitchPending {
		t.Fatal("repeated Alt-q must not start a second switch")
	}
	msg, ok := cmd().(toggleWorkspaceMsg)
	if !ok || msg.err == nil || !strings.Contains(msg.err.Error(), "no previous or other client session") {
		t.Fatalf("switch result = %#v, want missing workspace error", msg)
	}
	updated, cmd = m.Update(msg)
	m = updated.(model)
	if cmd != nil || m.toggleSwitchPending || m.errText == "" {
		t.Fatal("failed switch must keep the picker open and show the error")
	}
	if m.input.Value() != "search" || m.cursor != 1 || !slices.Equal(m.items, []string{"one", "two"}) || !slices.Equal(m.filtered, []int{0, 1}) {
		t.Fatal("failed switch changed the current input or selection")
	}
	_, cmd = m.Update(key)
	if cmd == nil {
		t.Fatal("Alt-q must allow a retry after failure")
	}
}

func TestAltQQuitsAfterWorkspaceSwitch(t *testing.T) {
	m := newModel()
	m.toggleSwitchPending = true
	m.errText = "previous failure"
	updated, cmd := m.Update(toggleWorkspaceMsg{})
	m = updated.(model)
	if m.toggleSwitchPending || m.errText != "" || cmd == nil {
		t.Fatal("successful switch must clear pending/error state and quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("successful workspace switch must quit the picker")
	}
}
