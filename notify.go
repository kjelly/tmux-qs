package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// notifyWaitingCmd emits a terminal bell and (if available) a desktop
// notification. Used when a session transitions from "not waiting" to
// "waiting" — the TUI may be hidden (popup closed) so an in-app
// status update is not enough.
//
// Tools are probed in order; the first to succeed wins. Failures
// are silent: a missing `notify-send` on a headless server is normal,
// not an error.
func notifyWaitingCmd(sessions []string) tea.Cmd {
	return func() tea.Msg {
		if len(sessions) == 0 {
			return nil
		}
		// Always ring the bell — works in any terminal, requires
		// no external tool.
		_, _ = os.Stdout.WriteString("\a")
		// Desktop notification is best-effort. We try notify-send
		// (Linux) and osascript (macOS); on Wayland, notify-send
		// is the standard.
		summary := fmt.Sprintf("tmux-qs: %d session(s) waiting", len(sessions))
		body := strings.Join(sessions, ", ")
		_ = run("notify-send", "-a", "tmux-qs", summary, body)
		_ = run("osascript", "-e",
			`display notification "`+body+`" with title "`+summary+`"`)
		return nil
	}
}
