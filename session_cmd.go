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

// renameSessionCmd renames the given session. Returning nil leaves the
// picker running (no UI update needed for a successful rename from the
// model's perspective — the next Ctrl-a reload will reflect the new name).
func renameSessionCmd(oldName, newName string) tea.Cmd {
	return func() tea.Msg {
		if err := tmuxRun("rename-session", "-t", oldName, newName); err != nil {
			return uiErrMsg{err}
		}
		return nil
	}
}
