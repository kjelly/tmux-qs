package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestFormatAge verifies the human-readable age formatter.
func TestFormatAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "now"},
		{30 * time.Second, "now"},
		{59 * time.Second, "now"},
		{time.Minute, "1m"},
		{3 * time.Minute, "3m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{2 * time.Hour, "2h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{7 * 24 * time.Hour, "7d"},
		{30 * 24 * time.Hour, "1mo"},
		{365 * 24 * time.Hour, "1y"},
		{-1 * time.Minute, "now"}, // negative clamped to 0
	}
	for _, c := range cases {
		if got := formatAge(c.d); got != c.want {
			t.Errorf("formatAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestJumpToSessionSkipsNonSessionEntries verifies alt+j/alt+k only
// stops on entries that are existing tmux sessions, and that pressing
// the key past the last/first session cycles back to the other end.
func TestJumpToSessionSkipsNonSessionEntries(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.items = []string{"~/github/sesh", "work", "~/notes", "scratch"}
	m.sessionPaths = map[string]string{
		"work":    "/home/u/work",
		"scratch": "/tmp/scratch",
	}
	m.filtered = []int{0, 1, 2, 3}
	m.cursor = 0
	m.refilter()

	// alt+j from cursor 0: should jump to index 1 ("work", a session)
	m.jumpToSession(1)
	if m.cursor != 1 {
		t.Errorf("alt+j should land on work (idx 1), got %d", m.cursor)
	}
	// alt+j again: should jump to index 3 ("scratch"), skipping "notes"
	m.jumpToSession(1)
	if m.cursor != 3 {
		t.Errorf("alt+j should land on scratch (idx 3), got %d", m.cursor)
	}
	// alt+j again (cycling): wraps to "work" (idx 1)
	m.jumpToSession(1)
	if m.cursor != 1 {
		t.Errorf("alt+j should cycle back to work (idx 1), got %d", m.cursor)
	}
	// alt+k from idx 1: goes back to scratch (idx 3)
	m.jumpToSession(-1)
	if m.cursor != 3 {
		t.Errorf("alt+k should land on scratch (idx 3), got %d", m.cursor)
	}
	// alt+k again: back to work (idx 1)
	m.jumpToSession(-1)
	if m.cursor != 1 {
		t.Errorf("alt+k should land on work (idx 1), got %d", m.cursor)
	}
}

// TestJumpToSessionNoSessions is a no-op when there are no sessions
// to jump to.
func TestJumpToSessionNoSessions(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.items = []string{"~/foo", "~/bar"}
	m.sessionPaths = map[string]string{} // empty
	m.filtered = []int{0, 1}
	m.cursor = 0
	m.refilter()
	m.jumpToSession(1)
	if m.cursor != 0 {
		t.Errorf("cursor should not move when no sessions, got %d", m.cursor)
	}
}

// TestAltNCreatesNewSession verifies the key handler returns a
// non-nil cmd that, when run, produces a switchedMsg.
func TestAltNCreatesNewSession(t *testing.T) {
	if _, err := runOut("tmux", "display-message", "-p", "#S"); err != nil {
		t.Skip("no tmux server")
	}
	// Clean up any prior test session.
	_ = run("tmux", "kill-session", "-t", "qs-test-new")
	defer run("tmux", "kill-session", "-t", "qs-test-new")

	m := newModel()
	m.items = []string{"anything"}
	m.filtered = []int{0}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc, Runes: []rune{' '}, Alt: true})
	// Alt+n isn't a direct key in tea.KeyMsg; use the String-based path.
	_ = updated
	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}, Alt: true})
	if cmd == nil {
		t.Fatal("alt+n should return a non-nil cmd")
	}
	msg := cmd()
	switched, ok := msg.(switchedMsg)
	if !ok {
		t.Fatalf("alt+n should produce switchedMsg, got %T", msg)
	}
	if !startsWith(switched.path, "qs-") {
		t.Errorf("new session name should start with qs-, got %q", switched.path)
	}
}

// TestCtrlRRenamesSession verifies rename returns a cmd that, on
// success, returns nil (no UI update needed) and on failure returns
// a uiErrMsg.
func TestCtrlRRenamesSession(t *testing.T) {
	if _, err := runOut("tmux", "display-message", "-p", "#S"); err != nil {
		t.Skip("no tmux server")
	}
	_ = run("tmux", "kill-session", "-t", "qs-rename-src")
	_ = run("tmux", "kill-session", "-t", "qs-rename-dst")
	if err := run("tmux", "new-session", "-d", "-s", "qs-rename-src"); err != nil {
		t.Skipf("cannot create session: %v", err)
	}
	defer run("tmux", "kill-session", "-t", "qs-rename-src")
	defer run("tmux", "kill-session", "-t", "qs-rename-dst")

	m := newModel()
	m.items = []string{"qs-rename-src"}
	m.filtered = []int{0}
	m.input.SetValue("qs-rename-dst")
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	if cmd == nil {
		t.Fatal("ctrl+r should return a non-nil cmd")
	}
	msg := cmd()
	if msg != nil {
		if errMsg, ok := msg.(uiErrMsg); ok {
			t.Fatalf("ctrl+r should succeed, got uiErrMsg: %v", errMsg.err)
		}
		t.Fatalf("ctrl+r should return nil on success, got %T: %+v", msg, msg)
	}
	// Verify the rename actually happened.
	out, _ := runOut("tmux", "list-sessions", "-F", "#{session_name}")
	if !contains(out, "qs-rename-dst") {
		t.Errorf("expected renamed session qs-rename-dst, got list: %q", out)
	}
}

// TestCtrlREmptyNameErrors verifies that ctrl+r with no input text
// surfaces a "type a new name first" error instead of calling tmux.
func TestCtrlREmptyNameErrors(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.items = []string{"work"}
	m.sessionPaths = map[string]string{"work": "/home/u/work"}
	m.filtered = []int{0}
	m.input.SetValue("")
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	m2 := updated.(model)
	if m2.errText != "type a new name first" {
		t.Errorf("errText = %q, want %q", m2.errText, "type a new name first")
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
