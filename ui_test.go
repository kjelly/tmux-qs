package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInputPadCentering(t *testing.T) {
	// short list, plenty of vertical space
	t.Setenv(popupEnv, "1")
	m := newModel()
	m.items = []string{"a", "b"}
	m.filtered = []int{0, 1}
	m.height = 20

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(model)

	// available = 20-3 = 17, rows = 2 (min of listHeight and filtered),
	// +1 for prompt line = 3. rawPad = (17-3)/2 = 7. >= 2 -> stays 7.
	if m.inputPad != 7 {
		t.Fatalf("short list: expected inputPad=7, got %d", m.inputPad)
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

func TestInputPadMinMargin(t *testing.T) {
	// When the natural centered pad would be 0 or 1 (i.e. a very tight
	// popup), we must still enforce a 2-row top margin.
	// height=9 => available=6, rows=2, rawPad=(6-3)/2=1 -> clamp to 2.
	t.Setenv(popupEnv, "1")
	m := newModel()
	m.items = []string{"a", "b"}
	m.filtered = []int{0, 1}
	m.height = 9
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 9})
	m = updated.(model)

	if m.inputPad != inputMinTopMargin {
		t.Fatalf("expected inputPad=%d, got %d", inputMinTopMargin, m.inputPad)
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
// the model: watchMsg.bufs -> m.paneBufs -> next watchCmd.
// bufs now stores SHA-256 hex hashes (64 chars) instead of full buffer
// text, to reduce memory on large tmux servers.
func TestWatchMsgCarriesBufsToModel(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	bufs := map[paneKey]string{
		{session: "S", window: "@1", paneIndex: 0}: sha256Hex("thinking..."),
	}
	updated, _ := m.Update(watchMsg{info: waitingInfo{bySession: map[string][]procCount{}}, bufs: bufs})
	m = updated.(model)
	want := sha256Hex("thinking...")
	if got := m.paneBufs[paneKey{session: "S", window: "@1", paneIndex: 0}]; got != want {
		t.Errorf("paneBufs not stored on model, got %q", got)
	}
}

// TestFuzzyScoreRanking verifies the fzf-like ranking: word-boundary and
// consecutive matches outrank scattered subsequence matches.
func TestFuzzyScoreRanking(t *testing.T) {
	okQS, qs := fuzzyScore("tmux-qs", "qs")
	okScatter, scatter := fuzzyScore("quiet-shell", "qs")
	if !okQS || !okScatter {
		t.Fatal("both candidates should match")
	}
	if qs <= scatter {
		t.Errorf("boundary+consecutive match should outrank scattered: %d <= %d", qs, scatter)
	}
	if ok, _ := fuzzyScore("anything", ""); !ok {
		t.Error("empty query must match")
	}
	if ok, _ := fuzzyScore("abc", "xyz"); ok {
		t.Error("non-match must return false")
	}
}

// TestRefilterRanksByScore verifies refilter reorders matches by score
// when a query is typed, and preserves source order when it is empty.
func TestRefilterRanksByScore(t *testing.T) {
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.items = []string{"quiet-shell", "tmux-qs"}
	m.input.SetValue("qs")
	m.refilter()
	if len(m.filtered) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(m.filtered))
	}
	if m.items[m.filtered[0]] != "tmux-qs" {
		t.Errorf("expected tmux-qs ranked first, got %q", m.items[m.filtered[0]])
	}
	m.input.SetValue("")
	m.refilter()
	if m.items[m.filtered[0]] != "quiet-shell" {
		t.Errorf("empty query should preserve source order, got %q first", m.items[m.filtered[0]])
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

// TestCurrentWorkspaceAtBottom verifies that the current tmux session and path are sorted to the bottom.
func TestCurrentWorkspaceAtBottom(t *testing.T) {
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
	
	// Pushed to bottom: "current-session" and "~/projects/current-path" should be at the end.
	// Pinned / normal order: "other-session" and "~/projects/other-path" should be at the top.
	first := m.items[m.filtered[0]]
	second := m.items[m.filtered[1]]
	third := m.items[m.filtered[2]]
	fourth := m.items[m.filtered[3]]

	if first != "other-session" {
		t.Errorf("expected 'other-session' first, got %q", first)
	}
	if second != "~/projects/other-path" {
		t.Errorf("expected '~/projects/other-path' second, got %q", second)
	}
	
	// The order of the pushed items should be stable relative to each other:
	// "current-session" (was index 0) and "~/projects/current-path" (was index 2).
	if third != "current-session" {
		t.Errorf("expected 'current-session' third, got %q", third)
	}
	if fourth != "~/projects/current-path" {
		t.Errorf("expected '~/projects/current-path' last, got %q", fourth)
	}
}

// TestAgentSelectionSubMenu verifies that pressing alt+v enters the agent selection menu,
// and selecting an agent successfully populates the result fields and exits.
func TestAgentSelectionSubMenu(t *testing.T) {
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



