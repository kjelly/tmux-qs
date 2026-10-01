package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// sanitizeSessionName strips invalid characters (like ':' and '.') and
// replaces spaces with hyphens to create a valid tmux session name.
func sanitizeSessionName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, ":", "-")
	name = strings.ReplaceAll(name, ".", "-")
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, name)
	return name
}

// newSessionCmd returns a tea.Cmd that creates a new empty tmux session
// with a sanitized user-provided name (or an auto-generated one if empty)
// and emits a switchedMsg so the caller connects to it.
func newSessionCmd(name string) tea.Cmd {
	return func() tea.Msg {
		name = sanitizeSessionName(name)
		if name == "" {
			name = fmt.Sprintf("qs-%d", time.Now().Unix())
		}
		for i := 0; i < 8; i++ {
			candidate := name
			if i > 0 {
				candidate = fmt.Sprintf("%s-%d", name, i)
			}
			if err := tmuxRun("new-session", "-d", "-s", candidate); err == nil {
				return switchedMsg{path: candidate}
			}
		}
		return uiErrMsg{fmt.Errorf("could not create a new session")}
	}
}

// renameSessionCmd renames the given session. A successful rename normally
// returns nil so the picker keeps running; sessions with a persisted note
// return sessionRenamedMsg so the in-memory note key follows the new name.
func renameSessionCmd(oldName, newName string) tea.Cmd {
	return renameSessionCmdOn(oldName, newName, getTmuxServer())
}

type sessionRenamedMsg struct {
	oldName string
	newName string
	server  tmuxServerSpec
}

func renameSessionCmdOn(oldName, newName string, server tmuxServerSpec) tea.Cmd {
	return func() tea.Msg {
		if err := tmuxRunOn(server, "rename-session", "-t", oldName, newName); err != nil {
			return uiErrMsg{err}
		}
		// Keep a note attached to the workspace when the user renames its
		// tmux session. Sessions without notes retain the historical nil
		// result so callers that only care about tmux success stay unchanged.
		if err := moveSessionNote(oldName, newName, server); err != nil {
			return uiErrMsg{err}
		}
		if notes := loadSessionNotes(); notes[sessionNoteKey(newName, server)] != "" {
			return sessionRenamedMsg{oldName: oldName, newName: newName, server: server}
		}
		return nil
	}
}
