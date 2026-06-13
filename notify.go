package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// notifyWaitingCmd emits a terminal bell, OSC terminal notification
// sequences, and (if available) a desktop notification. Used when a
// session transitions from "not waiting" to "waiting" — the TUI may
// be hidden (popup closed) so an in-app status update is not enough.
//
// Tools are probed in order; the first to succeed wins. Failures
// are silent: a missing `notify-send` on a headless server is normal,
// not an error.
func notifyWaitingCmd(sessions []string) tea.Cmd {
	return func() tea.Msg {
		if len(sessions) == 0 {
			return nil
		}
		_, _ = os.Stdout.WriteString("\a")
		summary := fmt.Sprintf("tmux-qs: %d session(s) waiting", len(sessions))
		body := strings.Join(sessions, ", ")
		sendTerminalNotification(summary, body)
		_ = run("notify-send", "-a", "tmux-qs", summary, body)
		_ = run("osascript", "-e",
			`display notification "`+body+`" with title "`+summary+`"`)
		return nil
	}
}

// sendTerminalNotification emits OSC escape sequences that modern
// terminal emulators interpret as in-window notifications. Three
// variants are sent to maximize compatibility:
//
//	OSC 9   — iTerm2 native
//	OSC 99  — kitty extension
//	OSC 777 — WezTerm extension
//
// When running inside tmux ($TMUX is set), each sequence is wrapped
// with tmux's DCS passthrough protocol so it reaches the outer
// terminal.
func sendTerminalNotification(title, msg string) {
	osc9 := fmt.Sprintf("\x1b]9;%s: %s\x07", title, msg)
	osc99 := fmt.Sprintf("\x1b]9;9;%s: %s\x07", title, msg)
	osc777 := fmt.Sprintf("\x1b]777;notify;%s;%s\x07", title, msg)

	if os.Getenv("TMUX") != "" {
		osc9 = wrapTmuxPassthrough(osc9)
		osc99 = wrapTmuxPassthrough(osc99)
		osc777 = wrapTmuxPassthrough(osc777)
	}

	fmt.Print(osc9)
	fmt.Print(osc99)
	fmt.Print(osc777)
}

// wrapTmuxPassthrough wraps an escape sequence for tmux's DCS
// passthrough protocol. Inside the DCS wrapper, every \x1b is
// doubled so tmux forwards it correctly to the outer terminal.
func wrapTmuxPassthrough(seq string) string {
	escaped := strings.ReplaceAll(seq, "\x1b", "\x1b\x1b")
	return "\x1bPtmux;" + escaped + "\x1b\\"
}
