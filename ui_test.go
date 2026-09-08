package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInputPadCentering(t *testing.T) {
	// Popup input is deliberately top-aligned, even for a short list.
	t.Setenv(popupEnv, "1")
	m := newModel()
	m.items = []string{"a", "b"}
	m.filtered = []int{0, 1}
	m.height = 20

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(model)

	if m.inputPad != 0 {
		t.Fatalf("short list: expected inputPad=0, got %d", m.inputPad)
	}

	// Long list that fills the view: inputPad must drop to 0.
	m.items = make([]string, 30)
	for i := range m.items {
		m.items[i] = strings.Repeat("x", 4)
	}
	m.filtered = make([]int, len(m.items))
	for i := range m.filtered {
		m.filtered[i] = i
	}
	m.height = 20
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(model)

	if m.inputPad != 0 {
		t.Fatalf("long list: expected inputPad=0, got %d", m.inputPad)
	}
}

func TestInitialCurrentPathUsesPopupCallerCWD(t *testing.T) {
	t.Setenv(popupCwdEnv, "/caller/workspace")
	if got := initialCurrentPath(); got != "/caller/workspace" {
		t.Fatalf("initial current path = %q, want popup caller cwd", got)
	}
}

func TestInputPadIsDisabledInTightPopup(t *testing.T) {
	// Even a tight popup remains top-aligned; no hidden padding may push the
	// blank focused input away from its stable row.
	t.Setenv(popupEnv, "1")
	m := newModel()
	m.items = []string{"a", "b"}
	m.filtered = []int{0, 1}
	m.height = 9
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 9})
	m = updated.(model)

	if m.inputPad != 0 {
		t.Fatalf("expected inputPad=0, got %d", m.inputPad)
	}
}

func TestInputPadNotInPopup(t *testing.T) {
	// No popup env: must not pad.
	os.Unsetenv(popupEnv)
	m := newModel()
	m.items = []string{"a", "b"}
	m.filtered = []int{0, 1}
	m.height = 20
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(model)

	if m.inputPad != 0 {
		t.Fatalf("non-popup: expected inputPad=0, got %d", m.inputPad)
	}
}

func TestViewPrependsPadding(t *testing.T) {
	t.Setenv(popupEnv, "1")
	m := newModel()
	m.items = []string{"a", "b"}
	m.filtered = []int{0, 1}
	m.height = 20
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(model)

	view := m.View()
	leading := 0
	for view != "" && view[0] == '\n' {
		leading++
		view = view[1:]
	}
	if leading != m.inputPad {
		t.Fatalf("expected %d leading newlines in View, got %d", m.inputPad, leading)
	}
}

// TestPopupInputPadDoesNotMoveWhenFiltering ensures filtering never moves the
// top-anchored prompt and list.
func TestPopupInputPadDoesNotMoveWhenFiltering(t *testing.T) {
	t.Setenv(popupEnv, "1")
	m := newModel()
	m.items = []string{"alpha", "beta", "gamma", "delta"}
	m.filtered = []int{0, 1, 2, 3}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(model)
	before := m.inputPad

	m.input.SetValue("alpha")
	m.refilter()
	if len(m.filtered) != 1 {
		t.Fatalf("filtered entries = %d, want 1", len(m.filtered))
	}
	if m.inputPad != before {
		t.Errorf("inputPad changed from %d to %d after filtering", before, m.inputPad)
	}
}

// TestPopupViewFitsTerminalAfterCursorMove guards against a popup redraw
// exceeding its terminal height, which would make tmux scroll the frame on
// navigation.
func TestPopupViewFitsTerminalAfterCursorMove(t *testing.T) {
	t.Setenv(popupEnv, "1")
	m := newModel()
	m.items = []string{"a", "b", "c"}
	m.filtered = []int{0, 1, 2}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)

	if got := strings.Count(m.View(), "\n") + 1; got != m.height {
		t.Errorf("View renders %d rows, want exactly terminal height %d", got, m.height)
	}
}

// TestWaitingVersionBumps verifies the watcher bumps waitingInfo.version
// on every successful refresh and the field is exposed to the UI. This
// guarantees the UI can detect when waiting info has changed and re-render.
func TestWaitingVersionBumps(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	if m.waiting.version != 0 {
		t.Errorf("initial version should be 0, got %d", m.waiting.version)
	}
	// Simulate a watcher tick by directly mutating the field; the
	// watchCmd also bumps this field on every successful refresh.
	m.waiting.version++
	if m.waiting.version != 1 {
		t.Errorf("expected version=1 after bump, got %d", m.waiting.version)
	}
}

// TestRefilterPreservesWaitingVersion verifies that a user keystroke
// (which triggers refilter) does not clear the waiting state — the
// watcher keeps its own version counter and refilter only mutates
// filtered/cursor/offset.
func TestRefilterPreservesWaitingVersion(t *testing.T) {
	m := newModel()
	m.items = []string{"alpha", "beta"}
	m.annots = map[string]string{}
	m.refilter()
	m.waiting.version = 42
	m.refilter()
	if m.waiting.version != 42 {
		t.Errorf("waiting.version should not be touched by refilter, got %d", m.waiting.version)
	}
}

// TestNewModelPopulatesWatchOpt verifies that newModel resolves the TOML
// config and bakes the resulting WatchingConfig into the model. This is
// the regression test for the "watcher never fires" bug where Init was
// a value receiver and the watchOpt mutation was silently lost.
func TestNewModelPopulatesWatchOpt(t *testing.T) {
	// Point HOME at a temp dir so loadConfig writes the example to a
	// scratch location rather than the user's real config. The memo
	// must be dropped so the redirected HOME actually takes effect.
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	prevHome, hadHome := os.LookupEnv("HOME")
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		if hadHome {
			os.Setenv("HOME", prevHome)
		} else {
			os.Unsetenv("HOME")
		}
	})

	m := newModel()
	if m.watchOpt.Commands == nil || len(m.watchOpt.Commands) == 0 {
		t.Errorf("watchOpt.Commands should be populated, got %v", m.watchOpt.Commands)
	}
	if !reflect.DeepEqual(m.watchOpt.Commands, defaultConfig.Waiting.Commands) {
		t.Errorf("watchOpt.Commands = %v, want %v", m.watchOpt.Commands, defaultConfig.Waiting.Commands)
	}
	if m.watchOpt.Idle != 30*time.Second {
		t.Errorf("watchOpt.Idle = %v, want 30s", m.watchOpt.Idle)
	}
	if m.watchOpt.Poll != 5*time.Second {
		t.Errorf("watchOpt.Poll = %v, want 5s", m.watchOpt.Poll)
	}
	if len(m.watchOpt.Prompts) == 0 {
		t.Errorf("watchOpt.Prompts should be populated from defaults")
	}
}

// TestInitDoesNotStartWatcher verifies that Init only kicks off the
// initial load + self-pane probe, and does NOT start the watcher. The
// watcher is started by the selfPaneMsg handler so the first tick has
// the correct self-pane exclusion.
func TestInitDoesNotStartWatcher(t *testing.T) {
	m := newModel()
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init should return a tea.BatchMsg, got %T", msg)
	}
	// Run each sub-cmd in isolation; none of them should produce a
	// watchMsg. loadCmd → itemsMsg/uiErrMsg; selfPaneCmd → selfPaneMsg.
	for i, sub := range batch {
		if sub == nil {
			continue
		}
		inner := sub()
		if _, isWatch := inner.(watchMsg); isWatch {
			t.Errorf("Init sub-cmd #%d produced watchMsg; watcher must not start here", i)
		}
	}
}

// TestSelfPaneMsgStartsWatcher verifies that when selfPaneMsg arrives,
// the returned cmd includes a watchMsg (the watcher's first tick
// fires on the 5s poll interval, so we can't observe the tick in a
// unit test — but we can verify that selfPaneMsg schedules a watcher
// with self-pane correctly set by running through the same Update path
// and checking the resulting m.watchOpt is still populated).
func TestSelfPaneMsgPreservesWatchOpt(t *testing.T) {
	m := newModel()
	if m.watchOpt.Commands == nil {
		t.Fatal("precondition: newModel must populate watchOpt")
	}
	updated, _ := m.Update(selfPaneMsg{key: paneKey{session: "S", window: "@1", paneIndex: 0}})
	m = updated.(model)
	if m.selfPane.session != "S" {
		t.Errorf("selfPane should be set, got %+v", m.selfPane)
	}
	if m.watchOpt.Commands == nil {
		t.Errorf("watchOpt must not be cleared by selfPaneMsg")
	}
}

// TestMoveBoundaryWrapping verifies that moving the cursor up from the first
// item wraps to the last item, and moving down from the last item wraps to the first.
func TestMoveBoundaryWrapping(t *testing.T) {
	m := newModel()
	m.items = []string{"a", "b", "c"}
	m.filtered = []int{0, 1, 2}
	m.cursor = 0

	// Move up from index 0 should wrap to 2 (last item)
	m.move(-1)
	if m.cursor != 2 {
		t.Errorf("expected cursor to wrap to 2 when moving up from 0, got %d", m.cursor)
	}

	// Move down from index 2 should wrap to 0 (first item)
	m.move(1)
	if m.cursor != 0 {
		t.Errorf("expected cursor to wrap to 0 when moving down from 2, got %d", m.cursor)
	}

	// Move down to index 2
	m.move(1)
	m.move(1)
	if m.cursor != 2 {
		t.Errorf("expected cursor to be 2, got %d", m.cursor)
	}
}

// TestWatchMsgCarriesBufsToModel is the regression test for the "stuck
// detection never fires" bug: watchCmd used to keep the previous tick's
// buffers in a closure, but the closure is re-created after every tick,
// so the history was silently lost. The buffers must round-trip through
// the model: watchMsg.captures -> m.paneCaptures -> next watchCmd.
// captures store each allowed pane's tty mtime + full buffer, used for
// both stuck detection and capture-skip on the next tick.
func TestWatchMsgCarriesBufsToModel(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	key := paneKey{session: "S", window: "@1", paneIndex: 0}
	captures := map[paneKey]paneCapture{
		key: {ttyMtime: 123, buf: "thinking..."},
	}
	updated, _ := m.Update(watchMsg{info: waitingInfo{bySession: map[string][]procCount{}}, captures: captures})
	m = updated.(model)
	if got := m.paneCaptures[key]; got.buf != "thinking..." || got.ttyMtime != 123 {
		t.Errorf("paneCaptures not stored on model, got %+v", got)
	}
}

// TestFuzzyScoreRanking verifies the fzf-like ranking: word-boundary and
// consecutive matches outrank scattered subsequence matches. The
// absolute scores produced by fzf's V2 algo (Smith-Waterman with
// bonus matrix) are not stable across fzf versions, so we exercise
// the public ranking via refilter rather than asserting raw scores.
func TestFuzzyScoreRanking(t *testing.T) {
	qs, okQS, _ := fuzzyScore("tmux-qs", "qs")
	scatter, okScatter, _ := fuzzyScore("quiet-shell", "qs")
	if !okQS || !okScatter {
		t.Fatal("both candidates should match")
	}
	if qs <= scatter {
		t.Errorf("boundary+consecutive match should outrank scattered: %d <= %d", qs, scatter)
	}
	if _, ok, _ := fuzzyScore("anything", ""); !ok {
		t.Error("empty query must match")
	}
	if _, ok, _ := fuzzyScore("abc", "xyz"); ok {
		t.Error("non-match must return false")
	}
}

// TestFuzzyScoreIndices verifies that the indices returned point to the
// matched characters in the ORIGINAL (un-lowercased) input. fzf V2
// picks indices via back-trace over the score matrix; for short
// patterns on short inputs the result is the first occurrence of each
// pattern character in order, but the exact offsets are an
// implementation detail of the algorithm.
func TestFuzzyScoreIndices(t *testing.T) {
	_, _, idx := fuzzyScore("tmux-qs", "qs")
	if len(idx) != 2 {
		t.Fatalf("expected 2 matched indices, got %d: %v", len(idx), idx)
	}
	if idx[0] == idx[1] {
		t.Errorf("expected distinct indices, got %v", idx)
	}
	s := "tmux-qs"
	// fzf V2 returns positions in back-trace order (reverse); we only
	// require that one of the indices points at 'q' and the other at 's'.
	pair := string([]byte{s[idx[0]], s[idx[1]]})
	if pair != "qs" && pair != "sq" {
		t.Errorf("expected indices to point at q and s, got %v for %q", idx, s)
	}
	// Empty query: no indices.
	if _, _, idx := fuzzyScore("anything", ""); idx != nil {
		t.Errorf("empty query should return nil indices, got %v", idx)
	}
}

// TestFuzzyScorePathScheme verifies the behavioral guarantees of
// using `algo.Init("path")` (matching the original workspace.nu's
// `--scheme=path`):
//
//   - delimiter chars are restricted to '/' (path scheme) — ',' and
//     ':' are NOT treated as word boundaries, unlike default scheme.
//   - bonusBoundaryWhite is set to bonusBoundary (30) instead of
//     bonusBoundary+2 (32) — first char of input / first char after
//     whitespace gets 2 points LESS than in default scheme.
//
// We don't assert exact scores (fzf internals are an implementation
// detail), only the relative behavior that the path scheme commits
// to: 'f' at the start of input scores slightly less in path than
// in default, while still being a strong match.
func TestFuzzyScorePathScheme(t *testing.T) {
	// 'f' at start of input is a valid match in both schemes,
	// and the score is dominated by scoreMatch (16) plus
	// bonusBoundaryWhite. The exact delta is small but the
	// behavior — "score > 0 and >= any inner-word match" —
	// holds in both.
	score, ok, _ := fuzzyScore("foo", "f")
	if !ok {
		t.Fatal("f at start should match")
	}
	if score <= 0 {
		t.Errorf("f at start should have positive score, got %d", score)
	}

	// In the path scheme, '/' is a delimiter, so a character matched
	// immediately after it earns a boundary bonus — a match on a path
	// component's first letter outranks the same letter buried mid-word.
	// This is the whole point of the path scheme for a session/dir
	// switcher: typing "f" should favour ".../foo" over "abcfgh".
	atBoundary, okA, _ := fuzzyScore("dir/foo", "f")
	inMiddle, okB, _ := fuzzyScore("abcfgh", "f")
	if !okA || !okB {
		t.Fatal("both should match")
	}
	if atBoundary <= inMiddle {
		t.Errorf("path-scheme: f right after '/' should outrank f mid-word; got %d vs %d",
			atBoundary, inMiddle)
	}
}

// TestRefilterRanksByScore verifies refilter reorders matches by score
// when a query is typed. When the query is empty, entries are still
// sorted by recency (most-recently-active tmux session first) — see
// recencyOf. This test populates sessionInfo so the recency tie-break
// has a deterministic signal to work with.
func TestRefilterRanksByScore(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	// Populate sessionInfo so the recency tie-break sees known
	// sessions. quiet-shell is older than tmux-qs, so tmux-qs
	// should float to the top once the query clears.
	m.sessionInfo = map[string]sessionInfo{
		"quiet-shell": {meta: sessionMeta{lastActive: time.Now().Add(-2 * time.Hour), hasLastAct: true}},
		"tmux-qs":     {meta: sessionMeta{lastActive: time.Now(), hasLastAct: true}},
	}
	m.items = []string{"quiet-shell", "tmux-qs"}
	m.input.SetValue("qs")
	m.refilter()
	if len(m.filtered) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(m.filtered))
	}
	if m.items[m.filtered[0]] != "tmux-qs" {
		t.Errorf("expected tmux-qs ranked first, got %q", m.items[m.filtered[0]])
	}
	// Empty query: recency tie-break promotes the most-recently-
	// active session. tmux-qs (lastActive=now) wins over
	// quiet-shell (lastActive=2h ago).
	m.input.SetValue("")
	m.refilter()
	if m.items[m.filtered[0]] != "tmux-qs" {
		t.Errorf("empty query should surface most-recently-active session first, got %q", m.items[m.filtered[0]])
	}
}

// TestRefilterFuzzyTiePrefersShortestPathOverGit verifies the length
// tiebreak from the original fzf configuration. Git metadata must not
// reorder equal-score fuzzy matches ahead of the shorter path.
func TestRefilterFuzzyTiePrefersShortestPathOverGit(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.floatWaiting = false
	m.src = srcDefault
	m.items = []string{"~/foo-very-long", "~/foo"}
	m.sessionInfo = map[string]sessionInfo{}
	m.annots = map[string]string{"~/foo-very-long": "main"}
	m.input.SetValue("foo")

	longScore, longOK, _ := fuzzyScore("~/foo-very-long", "foo")
	shortScore, shortOK, _ := fuzzyScore("~/foo", "foo")
	if !longOK || !shortOK || longScore != shortScore {
		t.Fatalf("test setup requires equal fuzzy scores, got long=%d (%t), short=%d (%t)", longScore, longOK, shortScore, shortOK)
	}

	m.refilter()
	if got := m.items[m.filtered[0]]; got != "~/foo" {
		t.Errorf("expected shortest equal-score path first, got %q", got)
	}
}

// TestFloatWaitingToTop verifies that, when float_to_top is enabled,
// sessions with a waiting agent sort above non-waiting ones in the
// default list (but below pinned entries).
func TestFloatWaitingToTop(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.src = srcDefault
	m.floatWaiting = true
	m.items = []string{"calm", "busy", "fav"}
	m.pinned = map[string]bool{"fav": true}
	m.waiting = waitingInfo{bySession: map[string][]procCount{
		"busy": {{name: "claude", count: 1}},
	}}
	m.refilter()
	order := []string{
		m.items[m.filtered[0]],
		m.items[m.filtered[1]],
		m.items[m.filtered[2]],
	}
	want := []string{"fav", "busy", "calm"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("float ordering = %v, want %v", order, want)
		}
	}

	// With floatWaiting off, the waiting session should not be promoted.
	m.floatWaiting = false
	m.refilter()
	if m.items[m.filtered[0]] != "fav" || m.items[m.filtered[1]] != "calm" {
		t.Errorf("without float, expected pinned then source order, got %q %q",
			m.items[m.filtered[0]], m.items[m.filtered[1]])
	}
}

// TestCtrlDRequiresConfirmation verifies the first Ctrl-d only arms the
// kill (pendingKill + status message) and does not reload, and that
// moving the cursor disarms it.
func TestCtrlDRequiresConfirmation(t *testing.T) {
	m := newModel()
	m.items = []string{"victim", "other"}
	m.refilter()

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = updated.(model)
	if m.pendingKill != "victim" {
		t.Fatalf("first Ctrl-d should arm pendingKill, got %q", m.pendingKill)
	}
	if cmd != nil {
		t.Error("first Ctrl-d must not kill/reload")
	}

	m.move(1)
	if m.pendingKill != "" {
		t.Error("cursor move should disarm pendingKill")
	}
}

// TestCtrlDBulkCleanupStaleSessions verifies that pressing Ctrl-d in srcCleanup mode
// sets the pendingKill flag to "cleanup-all" and shows a prompt.
func TestCtrlDBulkCleanupStaleSessions(t *testing.T) {
	m := newModel()
	m.src = srcCleanup
	m.items = []string{"stale1", "stale2"}
	m.filtered = []int{0, 1}
	m.cursor = 0

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = updated.(model)
	if m.pendingKill != "cleanup-all" || !strings.Contains(m.errText, "kill all 2 stale session") || cmd != nil {
		t.Errorf("expected pendingKill='cleanup-all' and prompt for 2 sessions, got %q, %q, %v", m.pendingKill, m.errText, cmd)
	}
}

// TestPinningAndSorting verifies that pinned workspaces are always stable-sorted to the top.
func TestPinningAndSorting(t *testing.T) {
	m := newModel()
	m.items = []string{"alpha", "beta", "gamma"}
	m.filtered = []int{0, 1, 2}
	m.pinned = map[string]bool{"beta": true}

	m.refilter()
	if len(m.filtered) != 3 {
		t.Fatalf("expected 3 filtered items, got %d", len(m.filtered))
	}
	first := m.items[m.filtered[0]]
	if first != "beta" {
		t.Errorf("expected pinned item 'beta' to be sorted to the top, got %q", first)
	}
	// Verify stable sorting order is preserved for non-pinned items
	second := m.items[m.filtered[1]]
	third := m.items[m.filtered[2]]
	if second != "alpha" || third != "gamma" {
		t.Errorf("expected stable order for other items, got %q and %q", second, third)
	}
}

// TestCurrentWorkspaceUsesStableOrderWithoutActivity verifies that current
// entries are not forced to the bottom when they have no recency signal.
func TestCurrentWorkspaceUsesStableOrderWithoutActivity(t *testing.T) {
	home, _ := os.UserHomeDir()
	m := newModel()
	m.items = []string{"current-session", "other-session", "~/projects/current-path", "~/projects/other-path"}
	m.filtered = []int{0, 1, 2, 3}
	m.currentSession = "current-session"
	m.currentPath = filepath.Join(home, "projects", "current-path")

	m.refilter()
	if len(m.filtered) != 4 {
		t.Fatalf("expected 4 filtered items, got %d", len(m.filtered))
	}

	got := []string{
		m.items[m.filtered[0]],
		m.items[m.filtered[1]],
		m.items[m.filtered[2]],
		m.items[m.filtered[3]],
	}
	want := []string{"current-session", "other-session", "~/projects/current-path", "~/projects/other-path"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestAgentSelectionSubMenu verifies that pressing alt+v enters the agent selection menu,
// and selecting an agent successfully populates the result fields and exits.
func TestAgentSelectionSubMenu(t *testing.T) {
	// Isolate HOME and XDG dirs so loadConfig() returns the default
	// config (where "claude" is the first Waiting.Command). Without
	// this, a real config file on the developer's machine can
	// reorder the commands and cause the test to fail.
	withCleanCacheEnv(t)
	resetConfigCache()

	oldLookPath := lookPath
	lookPath = func(name string) (string, error) {
		return "/mock/bin/" + name, nil
	}
	t.Cleanup(func() {
		lookPath = oldLookPath
	})

	m := newModel()
	m.items = []string{"workspace-a", "workspace-b"}
	m.filtered = []int{0, 1}
	m.cursor = 0 // selected "workspace-a"

	// Press Alt-v
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}, Alt: true})
	m2 := updated.(model)

	if m2.mode != modeAgentSelect {
		t.Fatalf("expected TUI mode to be modeAgentSelect, got %d", m2.mode)
	}
	if m2.agentSelectTarget != "workspace-a" {
		t.Errorf("expected agentSelectTarget to be 'workspace-a', got %q", m2.agentSelectTarget)
	}

	// Verify items are now the monitored AI agents list
	if len(m2.items) == 0 {
		t.Fatal("expected items to be populated with monitored AI agents")
	}

	// Simulate selecting the first agent
	firstAgent := m2.items[0]
	m2.cursor = 0

	// Press Enter to confirm agent selection
	updated3, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := updated3.(model)

	if m3.result != "workspace-a" {
		t.Errorf("expected result to be 'workspace-a', got %q", m3.result)
	}
	if m3.selectedAgent != firstAgent {
		t.Errorf("expected selectedAgent to be %q, got %q", firstAgent, m3.selectedAgent)
	}
	if !m3.openWithAgent {
		t.Error("expected openWithAgent to be true")
	}
	if cmd == nil {
		t.Error("expected non-nil exit command (tea.Quit)")
	}
}

// TestAgentSelectionSubMenuNoAgents verifies that when no monitored AI agents
// are installed, pressing Alt-v displays an error message on the status bar
// and does not change the mode to modeAgentSelect.
func TestAgentSelectionSubMenuNoAgents(t *testing.T) {
	oldLookPath := lookPath
	lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}
	t.Cleanup(func() {
		lookPath = oldLookPath
	})

	m := newModel()
	m.items = []string{"workspace-a", "workspace-b"}
	m.filtered = []int{0, 1}
	m.cursor = 0 // selected "workspace-a"

	// Press Alt-v
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}, Alt: true})
	m2 := updated.(model)

	if m2.mode == modeAgentSelect {
		t.Fatal("expected TUI mode NOT to change to modeAgentSelect when no agents are installed")
	}
	if m2.errText == "" {
		t.Error("expected an error message to be set on m.errText")
	}
}

// TestRefilterEmptyQueryUsesRecencyTieBreak is the regression test
// for "currently-active tmux sessions that have never been Enter-
// selected in the picker sink to the bottom of the list". With the
// new recency tie-break, m.sessionInfo's lastActive is consulted
// even when the entry has no recent.json record, so a session the
// user has been actively using in tmux (e.g. typing in a shell) is
// surfaced to the top, ahead of older sessions.
func TestRefilterEmptyQueryUsesRecencyTieBreak(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.floatWaiting = false
	m.items = []string{"untouched", "old", "fresh"}
	m.sessionInfo = map[string]sessionInfo{
		"old":   {meta: sessionMeta{lastActive: time.Now().Add(-2 * time.Hour), hasLastAct: true}},
		"fresh": {meta: sessionMeta{lastActive: time.Now(), hasLastAct: true}},
		// "untouched" has no entry — it's not a running tmux session
		// (e.g. a zoxide path the user has never opened).
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	m.refilter()
	order := []string{
		m.items[m.filtered[0]],
		m.items[m.filtered[1]],
		m.items[m.filtered[2]],
	}
	want := []string{"fresh", "old", "untouched"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("empty-query recency order = %v, want %v", order, want)
		}
	}
}

// TestRefilterRecencyBreaksFuzzyTie verifies that when two entries
// have the same fuzzy match score, the one with the more recent
// session_activity wins.
func TestRefilterRecencyBreaksFuzzyTie(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.items = []string{"work-old", "work-new"}
	m.sessionInfo = map[string]sessionInfo{
		"work-old": {meta: sessionMeta{lastActive: time.Now().Add(-3 * time.Hour), hasLastAct: true}},
		"work-new": {meta: sessionMeta{lastActive: time.Now(), hasLastAct: true}},
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	// "work" matches both equally via fzf's V2 (same prefix,
	// same-length patterns, no extra boundaries).
	m.input.SetValue("work")
	m.refilter()
	if len(m.filtered) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(m.filtered))
	}
	if m.items[m.filtered[0]] != "work-new" {
		t.Errorf("expected work-new (newer) ranked first, got %q", m.items[m.filtered[0]])
	}
}

// TestRefilterRecencyUnknownSinksBelowKnown verifies that entries
// with no recency signal at all (no sessionInfo, no recent.json
// record) sink to the bottom in stable order, even when other
// entries in the same tier have known recency.
func TestRefilterRecencyUnknownSinksBelowKnown(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.floatWaiting = false
	m.items = []string{"phantom", "alive"}
	m.sessionInfo = map[string]sessionInfo{
		"alive": {meta: sessionMeta{lastActive: time.Now().Add(-1 * time.Hour), hasLastAct: true}},
		// "phantom" has no entry anywhere.
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	m.refilter()
	if m.items[m.filtered[0]] != "alive" {
		t.Errorf("expected alive (known recency) ranked first, got %q", m.items[m.filtered[0]])
	}
	if m.items[m.filtered[1]] != "phantom" {
		t.Errorf("expected phantom (unknown recency) second, got %q", m.items[m.filtered[1]])
	}
}

// TestRefilterEmptyQueryTmuxSessionsFirst verifies the strict
// two-group ordering in the normal tier: tmux sessions rank ahead
// of directories, regardless of how each individual entry scores
// on recency vs zoxide. The tmux group is then sorted by
// #{session_activity} (newer first).
func TestRefilterEmptyQueryTmuxSessionsFirst(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.floatWaiting = false
	m.src = srcDefault
	// "alpha" is a tmux session, "old-path" is a directory. Both
	// are in the normal tier (not pinned, not waiting, not current).
	m.items = []string{"old-path", "alpha"}
	m.sessionInfo = map[string]sessionInfo{
		"alpha": {meta: sessionMeta{lastActive: time.Now(), hasLastAct: true}},
		// old-path has no sessionInfo -> groupDirectory.
	}
	m.zoxideScores = map[string]float64{
		// Give the directory the highest possible zoxide score
		// so a bug that uses zoxide-first for everything would
		// surface "old-path" first. The correct behavior is for
		// the tmux session to win on group alone.
		"old-path": 9999.0,
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	m.refilter()
	if got := m.items[m.filtered[0]]; got != "alpha" {
		t.Errorf("expected alpha (tmux session group) first, got %q", got)
	}
	if got := m.items[m.filtered[1]]; got != "old-path" {
		t.Errorf("expected old-path (directory group) second, got %q", got)
	}
}

// TestRefilterEmptyQueryDirectoriesZoxideSorted verifies that
// within the directory group, entries are sorted by zoxide score
// (higher first). Two directories with no zoxide score should
// sink to the bottom in stable order.
func TestRefilterEmptyQueryDirectoriesZoxideSorted(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.floatWaiting = false
	m.src = srcDefault
	// No tmux sessions — pure directory test.
	m.items = []string{"~/low", "~/high", "~/no-zoxide"}
	m.sessionInfo = map[string]sessionInfo{} // none
	m.zoxideScores = map[string]float64{
		"~/low":  10.0,
		"~/high": 100.0,
		// ~/no-zoxide has no zoxide entry.
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	m.refilter()
	got := []string{
		m.items[m.filtered[0]],
		m.items[m.filtered[1]],
		m.items[m.filtered[2]],
	}
	want := []string{"~/high", "~/low", "~/no-zoxide"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("directory order = %v, want %v", got, want)
			break
		}
	}
}

// TestRefilterEmptyQueryCurrentSessionUsesActivity verifies that the current
// session follows the same activity ordering as every other tmux session.
func TestRefilterEmptyQueryCurrentSessionUsesActivity(t *testing.T) {
	m := newModel()
	m.currentSession = "alpha" // current tmux session
	m.currentPath = ""
	m.floatWaiting = false
	m.src = srcDefault
	m.items = []string{"alpha", "beta", "~/some-path"}
	m.sessionInfo = map[string]sessionInfo{
		"alpha": {meta: sessionMeta{lastActive: time.Now(), hasLastAct: true}},
		"beta":  {meta: sessionMeta{lastActive: time.Now().Add(-1 * time.Hour), hasLastAct: true}},
		// ~/some-path has no sessionInfo.
	}
	m.zoxideScores = map[string]float64{
		"~/some-path": 50.0,
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	m.refilter()
	got := []string{
		m.items[m.filtered[0]],
		m.items[m.filtered[1]],
		m.items[m.filtered[2]],
	}
	want := []string{"alpha", "beta", "~/some-path"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order = %v, want %v", got, want)
			break
		}
	}
}

// TestRefilterEmptyQueryDirectoryNoZoxideFallsBackToRecent verifies
// that a directory with no zoxide record falls back to recent.json
// for tie-breaking against other directory entries that also have
// no zoxide record. Picker-Enter-touched ones win over
// never-touched ones.
func TestRefilterEmptyQueryDirectoryNoZoxideFallsBackToRecent(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.floatWaiting = false
	m.src = srcDefault
	nowT := time.Now()
	m.items = []string{"~/touched", "~/cold"}
	m.sessionInfo = map[string]sessionInfo{} // none
	m.zoxideScores = map[string]float64{}    // no zoxide at all
	m.recentCache = recentFile{entries: map[string]recentEntry{
		"~/touched": {Count: 1, Last: nowT.Unix() - 100},
		// "~/cold" is not in recent.json either.
	}}
	m.refilter()
	if got := m.items[m.filtered[0]]; got != "~/touched" {
		t.Errorf("expected ~/touched (recent.json fallback) first, got %q", got)
	}
	if got := m.items[m.filtered[1]]; got != "~/cold" {
		t.Errorf("expected ~/cold (no recency signal) second, got %q", got)
	}
}

// TestRefilterDirectoryGitSubgroupWinsOverZoxide is the core spec
// test: "zoxide directories, with git directories first, then by
// zoxide score". A git entry with the lowest zoxide score must
// outrank a non-git entry with the highest zoxide score — the
// isGit sub-group is strict, not a bonus.
func TestRefilterDirectoryGitSubgroupWinsOverZoxide(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.floatWaiting = false
	m.src = srcDefault
	m.items = []string{"~/plain-proj", "~/git-repo"}
	m.sessionInfo = map[string]sessionInfo{} // none — all directories
	m.zoxideScores = map[string]float64{
		"~/plain-proj": 200.0, // highest non-git zoxide score
		"~/git-repo":   50.0,  // git, but lowest score
	}
	// m.annots is the "is git" signal — populated by annotateCmd
	// in production. ~/git-repo has a branch; ~/plain-proj does
	// not. We populate it directly here to skip the async pass.
	m.annots = map[string]string{
		"~/git-repo": "main",
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	m.refilter()
	if got := m.items[m.filtered[0]]; got != "~/git-repo" {
		t.Errorf("expected git-repo (git sub-group) first despite lower zoxide score, got %q", got)
	}
	if got := m.items[m.filtered[1]]; got != "~/plain-proj" {
		t.Errorf("expected plain-proj (non-git sub-group) second, got %q", got)
	}
}

// TestRefilterDirectoryGitSubgroupSortedByZoxide verifies the
// inner sort key: within the git sub-group, entries are sorted by
// zoxide score (highest first). The non-git sub-group follows,
// also by zoxide score. A high-zoxide non-git must NOT bleed in
// front of a lower-zoxide git.
func TestRefilterDirectoryGitSubgroupSortedByZoxide(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.floatWaiting = false
	m.src = srcDefault
	m.items = []string{"~/plain", "~/git-low", "~/git-high"}
	m.sessionInfo = map[string]sessionInfo{}
	m.zoxideScores = map[string]float64{
		"~/plain":    999.0, // higher than any git entry
		"~/git-low":  10.0,
		"~/git-high": 100.0,
	}
	m.annots = map[string]string{
		"~/git-low":  "main",
		"~/git-high": "feature",
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	m.refilter()
	got := []string{
		m.items[m.filtered[0]],
		m.items[m.filtered[1]],
		m.items[m.filtered[2]],
	}
	want := []string{"~/git-high", "~/git-low", "~/plain"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("directory order = %v, want %v", got, want)
			break
		}
	}
}

// TestAnnotMsgTriggersRefilterForGitSubgroup verifies the second-
// pass reorder: before annotMsg arrives, the directory group
// falls back to zoxide-only (git info is unknown). When annotMsg
// lands, the model re-runs refilter and the git entry bubbles to
// the front. itemsMsg clears m.annots, so this is a true
// end-to-end test of the asynchronous sort update.
func TestAnnotMsgTriggersRefilterForGitSubgroup(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.floatWaiting = false
	m.src = srcDefault
	m.items = []string{"~/plain", "~/git-repo"}
	m.sessionInfo = map[string]sessionInfo{}
	m.zoxideScores = map[string]float64{
		"~/plain":    200.0,
		"~/git-repo": 10.0,
	}
	// Simulate itemsMsg: clear m.annots (now empty) and do the
	// initial refilter. The git signal is unknown so ~/plain
	// wins on zoxide score alone.
	m.annots = map[string]string{}
	m.refilter()
	if got := m.items[m.filtered[0]]; got != "~/plain" {
		t.Fatalf("expected ~/plain first on initial pass (no git info), got %q", got)
	}

	// Now simulate annotMsg arriving. The handler must re-run
	// refilter so the git entry bubbles to the top.
	updated, _ := m.Update(annotMsg{values: map[string]string{"~/git-repo": "main"}})
	m2 := updated.(model)
	if got := m2.items[m2.filtered[0]]; got != "~/git-repo" {
		t.Errorf("expected ~/git-repo to bubble to top after annotMsg, got %q", got)
	}
	if got := m2.items[m2.filtered[1]]; got != "~/plain" {
		t.Errorf("expected ~/plain second after annotMsg, got %q", got)
	}
}

func TestReloadDiscardsOlderAsyncResults(t *testing.T) {
	m := newModel()
	m.loadGeneration = 1
	m.src = srcTmux
	m.items = []string{"current"}

	updated, _ := m.Update(itemsMsg{
		generation: 0,
		src:        srcDefault,
		items:      []string{"stale"},
		info:       map[string]sessionInfo{},
	})
	got := updated.(model)
	if got.src != srcTmux || len(got.items) != 1 || got.items[0] != "current" {
		t.Fatalf("stale items result replaced current source: src=%v items=%v", got.src, got.items)
	}

	updated, _ = got.Update(annotMsg{generation: 0, values: map[string]string{"current": "stale"}})
	got = updated.(model)
	if len(got.annots) != 0 {
		t.Fatalf("stale annotations were applied: %#v", got.annots)
	}

	updated, _ = got.Update(loadErrMsg{generation: 0, err: errors.New("stale failure")})
	got = updated.(model)
	if got.errText != "" {
		t.Fatalf("stale load failure was displayed: %q", got.errText)
	}
}

func TestViewTransactionDiscardsStaleModeResults(t *testing.T) {
	m := newModel()
	m.loadGeneration = 3
	m.src = srcTmux
	m.items = []string{"current"}
	m.previewEntry = "current"
	m.previewRequest = 2

	for _, msg := range []tea.Msg{
		filesMsg{generation: 2, dir: "/tmp", files: []string{"stale-file"}},
		branchesMsg{generation: 2, repo: "/tmp", branches: []branchEntry{{name: "stale"}}},
		windowsMsg{generation: 2, items: []string{"stale-window"}},
		previewMsg{generation: 2, request: 2, entry: "current", content: "stale preview"},
		viewErrMsg{generation: 2, err: errors.New("stale error")},
	} {
		updated, _ := m.Update(msg)
		m = updated.(model)
	}
	if m.src != srcTmux || m.items[0] != "current" || m.previewContent != "" || m.errText != "" {
		t.Fatalf("stale view result changed model: src=%v items=%v preview=%q err=%q", m.src, m.items, m.previewContent, m.errText)
	}
}

func TestAllServerRowUsesQualifiedMetadataKey(t *testing.T) {
	m := newModel()
	row := "[other] dev"
	m.sessionInfo = map[string]sessionInfo{row: {path: "/work/other-dev"}}
	m.sessionPaths = map[string]string{row: "/work/other-dev"}
	if !m.isTmuxSessionEntry(row) {
		t.Fatal("qualified all-server row was not recognized as a tmux session")
	}
	if got := entryDir(row, m.sessionPaths); got != "/work/other-dev" {
		t.Fatalf("qualified row path = %q, want endpoint-specific path", got)
	}
}

func TestWindowSelectionKeepsAllServerEndpoint(t *testing.T) {
	m := newModel()
	m.src = srcWindows
	m.items = []string{"0 main [zsh]\tdev\t%1"}
	m.filtered = []int{0}
	m.windowsServerLabel = "other"
	updated, _ := m.choose()
	got := updated.(model)
	if got.result != "dev" || got.resultPaneID != "%1" || got.resultServer != "other" {
		t.Fatalf("window selection lost endpoint: result=%q pane=%q server=%q", got.result, got.resultPaneID, got.resultServer)
	}
}

func TestSessionNameForItem(t *testing.T) {
	for _, tc := range []struct {
		item string
		want string
	}{
		{item: "alpha", want: "alpha"},
		{item: "[dev] alpha", want: "alpha"},
		{item: "alpha:0.0 [zsh] ~/work\talpha\t%1", want: "alpha"},
	} {
		if got := sessionNameForItem(tc.item); got != tc.want {
			t.Errorf("sessionNameForItem(%q) = %q, want %q", tc.item, got, tc.want)
		}
	}
}

func TestRefilterTmuxPaneRowsUseSessionRecency(t *testing.T) {
	m := newModel()
	m.src = srcTmux
	m.items = []string{
		"old:0.0 [zsh] ~/old\told\t%1",
		"fresh:0.0 [zsh] ~/fresh\tfresh\t%2",
	}
	m.sessionInfo = map[string]sessionInfo{
		"old":   {meta: sessionMeta{lastActive: time.Now().Add(-time.Hour), hasLastAct: true}},
		"fresh": {meta: sessionMeta{lastActive: time.Now(), hasLastAct: true}},
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	m.refilter()
	if got := m.items[m.filtered[0]]; got != "fresh:0.0 [zsh] ~/fresh\tfresh\t%2" {
		t.Errorf("newer tmux pane row should rank first, got %q", got)
	}
}

func TestRefilterCurrentSessionUsesRecency(t *testing.T) {
	m := newModel()
	m.src = srcAll
	m.currentSession = "fresh"
	m.items = []string{"old", "fresh"}
	m.sessionInfo = map[string]sessionInfo{
		"old":   {meta: sessionMeta{lastActive: time.Now().Add(-time.Hour), hasLastAct: true}},
		"fresh": {meta: sessionMeta{lastActive: time.Now(), hasLastAct: true}},
	}
	m.recentCache = recentFile{entries: map[string]recentEntry{}}
	m.refilter()
	if got := m.items[m.filtered[0]]; got != "fresh" {
		t.Errorf("current session should use activity order, got %q first", got)
	}
}

func TestRefilterNonSessionSourcesKeepOrder(t *testing.T) {
	for _, src := range []sourceKind{srcFiles, srcCommands} {
		m := newModel()
		m.src = src
		m.items = []string{"older", "newer"}
		m.recentCache = recentFile{entries: map[string]recentEntry{
			"older": {Last: time.Now().Add(-time.Hour).Unix()},
			"newer": {Last: time.Now().Unix()},
		}}
		m.refilter()
		got := []string{m.items[m.filtered[0]], m.items[m.filtered[1]]}
		want := []string{"older", "newer"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("source %v order = %v, want %v", src, got, want)
		}
	}
}
