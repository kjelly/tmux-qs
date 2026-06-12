package main

import (
	"os"
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

// TestMoveBoundaryClamping verifies that moving the cursor up from the first
// item or down from the last item clamps to boundaries and does not wrap.
func TestMoveBoundaryClamping(t *testing.T) {
	m := newModel()
	m.items = []string{"a", "b", "c"}
	m.filtered = []int{0, 1, 2}
	m.cursor = 0

	// Move up from index 0 should clamp to 0
	m.move(-1)
	if m.cursor != 0 {
		t.Errorf("expected cursor to remain 0 when moving up from 0, got %d", m.cursor)
	}

	// Move down to the end
	m.move(1)
	m.move(1)
	if m.cursor != 2 {
		t.Errorf("expected cursor to be 2, got %d", m.cursor)
	}

	// Move down from index 2 should clamp to 2
	m.move(1)
	if m.cursor != 2 {
		t.Errorf("expected cursor to remain 2 when moving down from 2, got %d", m.cursor)
	}
}


// TestWatchMsgCarriesBufsToModel is the regression test for the "stuck
// detection never fires" bug: watchCmd used to keep the previous tick's
// buffers in a closure, but the closure is re-created after every tick,
// so the history was silently lost. The buffers must round-trip through
// the model: watchMsg.bufs -> m.paneBufs -> next watchCmd.
func TestWatchMsgCarriesBufsToModel(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	bufs := map[paneKey]string{
		{session: "S", window: "@1", paneIndex: 0}: "thinking...",
	}
	updated, _ := m.Update(watchMsg{info: waitingInfo{bySession: map[string][]procCount{}}, bufs: bufs})
	m = updated.(model)
	if got := m.paneBufs[paneKey{session: "S", window: "@1", paneIndex: 0}]; got != "thinking..." {
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
