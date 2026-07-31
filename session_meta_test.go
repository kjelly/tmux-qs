package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestIsEinkSessionName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"work-eink", true},
		{" work-eink ", true},
		{"work", false},
		{"eink-work", false},
	} {
		if got := isEinkSessionName(tc.name); got != tc.want {
			t.Errorf("isEinkSessionName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAltETogglesSelectedRunningSession(t *testing.T) {
	m := model{
		mode:         modeList,
		items:        []string{"work"},
		filtered:     []int{0},
		sessionPaths: map[string]string{"work": "/tmp/work"},
	}
	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}, Alt: true})
	if cmd == nil {
		t.Fatal("alt+e should return a quit command")
	}
	got := updated.(model)
	if got.resultEinkTarget != "work" {
		t.Fatalf("alt+e target = %q, want work", got.resultEinkTarget)
	}
}

func TestAltERejectsNonSessionEntry(t *testing.T) {
	m := model{
		mode:         modeList,
		items:        []string{"/tmp/work"},
		filtered:     []int{0},
		sessionPaths: map[string]string{},
	}
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}, Alt: true})
	got := updated.(model)
	if got.resultEinkTarget != "" {
		t.Fatalf("non-session entry unexpectedly got eink target %q", got.resultEinkTarget)
	}
	if got.errText == "" {
		t.Fatal("non-session entry should show an error")
	}
}

func TestEinkCreateCommandAvailability(t *testing.T) {
	if einkCreateCommandAvailable("tmux-qs-eink") {
		t.Fatal("create command should be hidden in an -eink session")
	}
	if !einkCreateCommandAvailable("tmux-qs") {
		t.Fatal("create command should be available in a base session")
	}
}

// TestMergeEinkActivityPrefersNewerTwin verifies that a base
// session's meta picks up its -eink twin's activity when the twin
// was used more recently — the fix for a session that the user only
// actually worked in via its -eink (light-mode) counterpart looking
// stale in the picker.
func TestMergeEinkActivityPrefersNewerTwin(t *testing.T) {
	older := time.Unix(1000, 0)
	newer := time.Unix(2000, 0)
	out := map[string]sessionInfo{
		"work": {meta: sessionMeta{lastActive: older, hasLastAct: true}},
	}
	mergeEinkActivity(out, map[string]time.Time{"work": newer})
	if !out["work"].meta.hasLastAct || !out["work"].meta.lastActive.Equal(newer) {
		t.Errorf("expected base session to adopt the newer -eink twin activity, got %+v", out["work"].meta)
	}
}

// TestMergeEinkActivityKeepsNewerBase verifies the merge doesn't
// regress a base session whose own activity is already newer than
// its -eink twin's.
func TestMergeEinkActivityKeepsNewerBase(t *testing.T) {
	older := time.Unix(1000, 0)
	newer := time.Unix(2000, 0)
	out := map[string]sessionInfo{
		"work": {meta: sessionMeta{lastActive: newer, hasLastAct: true}},
	}
	mergeEinkActivity(out, map[string]time.Time{"work": older})
	if !out["work"].meta.lastActive.Equal(newer) {
		t.Errorf("base session's newer activity should win, got %+v", out["work"].meta)
	}
}

// TestMergeEinkActivityIgnoresMissingBase verifies a stray -eink
// activity record for a base session that isn't in out (e.g. the
// base was killed but its group clone lingers) is a no-op, not a
// panic or a phantom entry.
func TestMergeEinkActivityIgnoresMissingBase(t *testing.T) {
	out := map[string]sessionInfo{}
	mergeEinkActivity(out, map[string]time.Time{"ghost": time.Unix(2000, 0)})
	if len(out) != 0 {
		t.Errorf("expected no entries created for a missing base session, got %+v", out)
	}
}

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

// TestAltMCreatesNewSession verifies the key handler returns a
// non-nil cmd that, when run, produces a switchedMsg.
func TestAltMCreatesNewSession(t *testing.T) {
	withTestTmuxServer(t)

	m := newModel()
	m.items = []string{"anything"}
	m.filtered = []int{0}
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	if cmd == nil {
		t.Fatal("alt+m should return a non-nil cmd")
	}
	msg := cmd()
	switched, ok := msg.(switchedMsg)
	if !ok {
		t.Fatalf("alt+m should produce switchedMsg, got %T", msg)
	}
	if !startsWith(switched.path, "qs-") {
		t.Errorf("new session name should start with qs-, got %q", switched.path)
	}
}

// TestCtrlRRenamesSession verifies rename returns a cmd that, on
// success, returns nil (no UI update needed) and on failure returns
// a uiErrMsg.
func TestCtrlRRenamesSession(t *testing.T) {
	withTestTmuxServer(t)
	if err := tmuxRun("new-session", "-d", "-s", "qs-rename-src"); err != nil {
		t.Skipf("cannot create session: %v", err)
	}

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
	out, _ := tmuxRunOut("list-sessions", "-F", "#{session_name}")
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
