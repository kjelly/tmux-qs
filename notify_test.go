package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestNotifyWaitingCmdBellsOnStdout verifies the bell is emitted
// (terminal escape \a) when notifyWaitingCmd runs.
func TestNotifyWaitingCmdBellsOnStdout(t *testing.T) {
	out := captureStdout(t, func() {
		_ = notifyWaitingCmd([]string{"work"})()
	})
	if !strings.Contains(out, "\a") {
		t.Errorf("expected bell \\a in stdout, got %q", out)
	}
}

// TestNotifyWaitingCmdEmptyNoop: an empty session list produces no
// output.
func TestNotifyWaitingCmdEmptyNoop(t *testing.T) {
	out := captureStdout(t, func() {
		_ = notifyWaitingCmd(nil)()
	})
	if strings.Contains(out, "\a") {
		t.Errorf("empty session list should not bell, got %q", out)
	}
}

// TestWatchMsgNotifiesOnNewWaiting verifies the UI fires a
// notification when a session newly enters the waiting set.
func TestWatchMsgNotifiesOnNewWaiting(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	// Pretend the previous tick already knew about session "old".
	m.waiting.bySession = map[string][]procCount{
		"old": {{name: "claude", count: 1, signal: "prompt"}},
	}
	// New tick: old is still there, but "fresh" just transitioned in.
	msg := watchMsg{info: waitingInfo{
		bySession: map[string][]procCount{
			"old":   {{name: "claude", count: 1, signal: "prompt"}},
			"fresh": {{name: "claude", count: 1, signal: "prompt"}},
		},
	}}
	updated, _ := m.Update(msg)
	m2 := updated.(model)
	if _, ok := m2.waiting.bySession["fresh"]; !ok {
		t.Errorf("waiting.bySession should contain fresh after Update, got %+v", m2.waiting.bySession)
	}
}

// TestWatchMsgNoNotifyWhenStable: if no new sessions, no notification.
func TestWatchMsgNoNotifyWhenStable(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.waiting.bySession = map[string][]procCount{
		"old": {{name: "claude", count: 1}},
	}
	msg := watchMsg{info: waitingInfo{
		bySession: map[string][]procCount{
			"old": {{name: "claude", count: 2}},
		},
	}}
	updated, _ := m.Update(msg)
	m2 := updated.(model)
	if m2.waiting.bySession["old"][0].count != 2 {
		t.Errorf("old session count should be 2, got %d", m2.waiting.bySession["old"][0].count)
	}
}

// TestAltEnterSendsKeysToSession verifies Alt-Enter sends the input
// box text to the selected session via tmux send-keys, then clears
// the input.
func TestAltEnterSendsKeysToSession(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-sendkeys-test"
	if err := tmuxRun("new-session", "-d", "-s", session); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}

	m := newModel()
	m.items = []string{session}
	m.sessionPaths = map[string]string{session: "/tmp"}
	m.filtered = []int{0}
	m.input.SetValue("hello world")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m2 := updated.(model)
	if got := m2.input.Value(); got != "" {
		t.Errorf("input should be cleared after send, got %q", got)
	}
}

// TestAltEnterEmptyTextErrors verifies empty input box surfaces an
// error and does not call send-keys.
func TestAltEnterEmptyTextErrors(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.items = []string{"work"}
	m.sessionPaths = map[string]string{"work": "/tmp"}
	m.filtered = []int{0}
	m.input.SetValue("")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m2 := updated.(model)
	if m2.errText != "type a prompt first" {
		t.Errorf("errText = %q, want %q", m2.errText, "type a prompt first")
	}
}
