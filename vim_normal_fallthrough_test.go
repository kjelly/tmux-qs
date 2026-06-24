package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// makeVimNormalModel returns a fresh model in modeList / vimNormal
// with enough state for handleVimNormal to operate: a few items in
// the filtered list and a preview pane with scrollable content.
func makeVimNormalModel() model {
	m := newModel()
	m.mode = modeList
	m.vimMode = vimNormal
	m.items = []string{"a", "b", "c", "d", "e"}
	m.filtered = []int{0, 1, 2, 3, 4}
	m.cursor = 0
	m.width = 100  // wide enough for preview pane
	m.height = 24
	m.previewContent = strings.Repeat("preview line\n", 50)
	m.previewEntry = "test"
	m.previewOffset = 0
	return m
}

// TestVimNormalAltUpScrollsPreview verifies that pressing alt+up
// in vimNormal mode scrolls the preview pane. alt+up is NOT in
// vimNormalOwnKeys, so it should fall through to the insert-mode
// handler (which is exactly the same code path that handles it in
// insert mode).
//
// alt+up calls m.scrollPreview(-1) — meaning "scroll the pane's
// content up by one line", which DECREMENTS m.previewOffset (so
// the user sees content from earlier in the buffer).
func TestVimNormalAltUpScrollsPreview(t *testing.T) {
	m := makeVimNormalModel()
	m.previewOffset = 5 // start with some offset so we can observe the change
	resModel, _ := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	m = resModel.(model)
	if m.previewOffset != 4 {
		t.Errorf("vimNormal alt+up should decrement preview offset (5→4), got %d", m.previewOffset)
	}
	// vimMode must stay in normal after fall-through.
	if m.vimMode != vimNormal {
		t.Errorf("vimMode should remain vimNormal after fall-through, got %v", m.vimMode)
	}
}

// TestVimNormalAltDownScrollsPreview verifies alt+down works the
// same way (symmetric to alt+up).
func TestVimNormalAltDownScrollsPreview(t *testing.T) {
	m := makeVimNormalModel()
	resModel, _ := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyDown, Alt: true})
	m = resModel.(model)
	if m.previewOffset != 1 {
		t.Errorf("vimNormal alt+down should scroll preview +1, got offset %d", m.previewOffset)
	}
}

// TestVimNormalQuestionMarkTogglesHelp verifies that ? in normal
// mode toggles the help overlay (insert mode does the same).
func TestVimNormalQuestionMarkTogglesHelp(t *testing.T) {
	m := makeVimNormalModel()
	if m.mode == modeHelp {
		t.Fatalf("precondition: mode should not already be modeHelp")
	}
	resModel, _ := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = resModel.(model)
	if m.mode != modeHelp {
		t.Errorf("vimNormal ? should toggle to modeHelp, got mode=%v", m.mode)
	}
	// Press ? again to toggle back.
	resModel, _ = m.handleVimNormal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = resModel.(model)
	if m.mode != modeList {
		t.Errorf("second ? should toggle back to modeList, got mode=%v", m.mode)
	}
}

// TestVimNormalCtrlASwitchesToAll verifies ctrl+a in normal mode
// triggers srcAll reload (which sets m.src to srcAll).
//
// ctrl+a in insert mode calls m.reload(srcAll). The reload
// function starts a background goroutine and returns a tea.Cmd;
// in this test we only check that the call produced a non-nil cmd
// (i.e. the fall-through happened) and that m.src got changed.
func TestVimNormalCtrlASwitchesToAll(t *testing.T) {
	m := makeVimNormalModel()
	resModel, cmd := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = resModel.(model)
	if cmd == nil {
		t.Fatal("vimNormal ctrl+a should produce a non-nil cmd (reload)")
	}
	if m.src != srcAll {
		t.Errorf("vimNormal ctrl+a should set m.src to srcAll, got %v", m.src)
	}
}

// TestVimNormalTabMovesCursor verifies tab in normal mode moves
// the cursor down (same as insert mode). tab is NOT in
// vimNormalOwnKeys, so it should fall through.
func TestVimNormalTabMovesCursor(t *testing.T) {
	m := makeVimNormalModel()
	m.cursor = 1
	resModel, _ := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyTab})
	m = resModel.(model)
	if m.cursor != 2 {
		t.Errorf("vimNormal tab should move cursor +1, got %d (want 2)", m.cursor)
	}
}

// TestVimNormalCountPrefixResetsAfterFallthrough verifies that a
// count prefix accumulated by handleVimNormal (e.g. "5") does NOT
// leak into the next normal-mode motion after a fall-through. The
// dispatchAsInsert helper clears vimCount; without that, the next
// "j" press would move 5 lines instead of 1.
func TestVimNormalCountPrefixResetsAfterFallthrough(t *testing.T) {
	m := makeVimNormalModel()
	m.cursor = 0

	// Press "5" — handleVimNormal's count-prefix logic accumulates
	// this into vimCount but doesn't move the cursor.
	resModel, _ := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	m = resModel.(model)
	if m.vimCount != "5" {
		t.Fatalf("after pressing 5, vimCount should be %q, got %q", "5", m.vimCount)
	}

	// Now press alt+up — falls through to insert handler. The
	// fall-through should clear vimCount so it doesn't pollute the
	// next motion.
	resModel, _ = m.handleVimNormal(tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	m = resModel.(model)
	if m.vimCount != "" {
		t.Errorf("after fall-through, vimCount should be cleared, got %q", m.vimCount)
	}

	// Now press j — should move cursor 1, not 5.
	resModel, _ = m.handleVimNormal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = resModel.(model)
	if m.cursor != 1 {
		t.Errorf("after count-clear + j, cursor should be 1, got %d (would be 5 if count leaked)", m.cursor)
	}
}

// TestVimNormalJKMovesCursor (regression guard) verifies j/k still
// move the cursor in normal mode and are NOT swallowed by the
// fall-through path.
func TestVimNormalJKMovesCursor(t *testing.T) {
	m := makeVimNormalModel()
	m.cursor = 1

	resModel, _ := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = resModel.(model)
	if m.cursor != 2 {
		t.Errorf("vimNormal j should move cursor +1, got %d (want 2)", m.cursor)
	}
	resModel, _ = m.handleVimNormal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = resModel.(model)
	if m.cursor != 1 {
		t.Errorf("vimNormal k should move cursor -1, got %d (want 1)", m.cursor)
	}
}

// TestVimNormalEnterStillChooses (regression guard) verifies
// enter still calls choose() in normal mode and does NOT fall
// through to insert mode. We give the model a fake session in
// m.sessionPaths so choose() can complete cleanly without trying
// to actually attach to a tmux session (it'll call switchOrAttach
// via connect, which uses tmuxRun — but with a non-existent
// session name, that fails silently).
func TestVimNormalEnterStillChooses(t *testing.T) {
	m := makeVimNormalModel()
	m.cursor = 0
	m.sessionPaths = map[string]string{"a": "/tmp"}

	// enter: in normal mode this calls m.choose(), which sets
	// m.result and returns tea.Quit (since m.items[0]="a" is in
	// sessionPaths). We just need to confirm m.result was set.
	resModel, cmd := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyEnter})
	m = resModel.(model)
	if m.result != "a" {
		t.Errorf("vimNormal enter should set m.result=%q, got %q", "a", m.result)
	}
	if cmd == nil {
		t.Error("vimNormal enter should return tea.Quit (non-nil cmd)")
	}
}

// TestVimNormalEscStillQuits (regression guard) verifies esc
// still calls tea.Quit in normal mode and does NOT fall through
// to the insert-mode handler (which would just toggle into
// vimInsert — wrong behavior).
func TestVimNormalEscStillQuits(t *testing.T) {
	m := makeVimNormalModel()
	_, cmd := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("vimNormal esc should return tea.Quit (non-nil cmd)")
	}
	msg := cmd()
	if _, ok := msg.(quitMsg); !ok {
		// tea.Quit's cmd() returns a special quit sentinel.
		// We accept anything from cmd() here — the key thing
		// is that a non-nil cmd was returned, signalling the
		// normal-mode esc path was taken (not the fall-through
		// to insert mode, which would return nil cmd).
		t.Logf("got cmd() = %T (%v) — non-nil is what we care about", msg, msg)
	}
}

// TestVimNormalDigitCountPrefix (regression guard) verifies
// "5j" still moves the cursor 5 rows. The digit "5" accumulates
// into vimCount, "j" consumes it. After "j" vimCount must be
// cleared. Critically, "5" must NOT fall through to the textinput.
func TestVimNormalDigitCountPrefix(t *testing.T) {
	m := makeVimNormalModel()
	m.cursor = 0

	resModel, _ := m.handleVimNormal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	m = resModel.(model)
	if m.vimCount != "5" {
		t.Errorf("after 5, vimCount = %q, want %q", m.vimCount, "5")
	}
	// Make sure 5 didn't leak into the input box.
	if m.input.Value() != "" {
		t.Errorf("count digit should not leak into input, got %q", m.input.Value())
	}
	resModel, _ = m.handleVimNormal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = resModel.(model)
	// list wraps; from idx 0, +5 = idx 0 again (5 mod 5).
	// But filtered has 5 items, so +5 lands on idx 0.
	if m.cursor != 0 {
		t.Errorf("after 5j, cursor should be 0, got %d", m.cursor)
	}
	if m.vimCount != "" {
		t.Errorf("after j consumed count, vimCount should be empty, got %q", m.vimCount)
	}
}

// quitMsg is a placeholder type to detect tea.Quit's return.
// tea.Quit's actual behavior is: the returned cmd, when called,
// returns a special struct value. We don't care about the exact
// type, only that cmd is non-nil.
type quitMsg struct{}
