package main

import (
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// ---------------------------------------------------------------------------
// L3: teatest program-level integration tests
//
// These tests launch a full tea.Model inside teatest's in-memory program
// harness (no real PTY), send messages and keystrokes, and verify both
// the final model state and the user-visible output.
//
// Every test isolates HOME and XDG_* env vars to a temp dir so the model
// never reads the developer's real config, cache, or pinned-sessions file.
// ---------------------------------------------------------------------------

// withIsolatedEnv redirects HOME and all XDG dirs to a temp directory so
// newModel() and the running program never touch the developer's real
// filesystem state.
func withIsolatedEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for _, kv := range [][2]string{
		{"HOME", home},
		{"XDG_CONFIG_HOME", home + "/.config"},
		{"XDG_CACHE_HOME", home + "/.cache"},
		{"XDG_DATA_HOME", home + "/.local/share"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	// Ensure we are not detected as "inside tmux" — otherwise main() would
	// try to open a popup. teatest bypasses main() so this is mostly
	// belt-and-suspenders.
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_QS_POPUP", "")
}

// waitForOutput polls the teatest output buffer until the predicate
// returns true or the timeout expires. Unlike teatest.WaitFor, this
// helper uses io.ReadAll directly to drain the buffer on each poll,
// accumulating the result in a local buffer for the predicate check.
func waitForOutput(t *testing.T, tm *teatest.TestModel, timeout time.Duration, predicate func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var accumulated strings.Builder
	for time.Now().Before(deadline) {
		raw, _ := io.ReadAll(tm.Output())
		if len(raw) > 0 {
			accumulated.Write(raw)
		}
		if predicate(normalizeOutput(accumulated.String())) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waitForOutput: condition not met after %s. Last output:\n%s",
		timeout, normalizeOutput(accumulated.String()))
}

// newTeatestModel creates a model, starts it in teatest, waits for the
// initial async commands to settle, then sends an itemsMsg with
// deterministic test data.
//
// To avoid a race where Init's loadCmd overwrites the test data, the
// model's source is set to srcFiles (which loadSource returns nil,nil
// for, completing almost instantly). A settle delay ensures loadCmd's
// itemsMsg is processed before our test itemsMsg is sent.
//
// Because newModel(false, false, false) disables vim mode, Esc will quit
// the program (instead of toggling to normal mode).
//
// The returned TestModel has test data loaded but the caller is
// responsible for waiting for it to appear in the output (via
// waitForOutput). This avoids the issue of draining the output buffer
// before the program has re-rendered.
func newTeatestModel(t *testing.T, items []string) *teatest.TestModel {
	t.Helper()
	withIsolatedEnv(t)

	m := newModel(false, false, false)
	m.src = srcFiles
	m.sessionPaths = map[string]string{}
	m.sessionInfo = map[string]sessionInfo{}

	tm := teatest.NewTestModel(
		t,
		m,
		teatest.WithInitialTermSize(100, 30),
	)

	// Settle: let Init's async commands (loadCmd, selfPaneCmd) complete.
	// loadSource(srcFiles) returns nil,nil instantly; selfPaneCmd forks
	// tmux which may take a few ms. 200ms is generous.
	time.Sleep(200 * time.Millisecond)

	// Send itemsMsg with test data. By now loadCmd's itemsMsg has
	// already been processed, so our itemsMsg will not be overwritten.
	tm.Send(itemsMsg{
		src:   srcDefault,
		items: items,
		info:  map[string]sessionInfo{},
	})

	// Brief delay for the program to process itemsMsg and re-render.
	time.Sleep(100 * time.Millisecond)

	return tm
}

// ansiRegexp matches all ANSI escape sequences: CSI (ESC [ ... final
// byte), OSC (ESC ] ... BEL or ST), and simple two-byte escapes (ESC X).
// This is more comprehensive than the project's stripAnsi which only
// handles SGR (ESC ... m) sequences. teatest output contains cursor
// movement, line clearing, and other non-SGR sequences.
var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>]|\x1b[^[\]()=>0-9A-Za-z]`)

// stripAllAnsi removes all ANSI escape sequences from s, including CSI,
// OSC, and simple two-byte escapes. Unlike the project's stripAnsi
// (which only strips SGR sequences ending in 'm'), this handles cursor
// movement, line clearing, bracketed paste mode, and other sequences
// that teatest programs emit.
func stripAllAnsi(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

// normalizeOutput strips ANSI escapes and CRLF so assertions can focus
// on visible text content.
func normalizeOutput(s string) string {
	s = stripAllAnsi(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// TestTeatestStartAndQuitWithCtrlC verifies the program starts, renders
// the header, and exits cleanly when Ctrl-C is sent.
func TestTeatestStartAndQuitWithCtrlC(t *testing.T) {
	tm := newTeatestModel(t, []string{"alpha", "beta", "gamma"})

	// Wait for the initial render to appear (header line is always shown).
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "help")
	})

	// Send Ctrl-C to quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	if _, ok := final.(model); !ok {
		t.Fatalf("final model = %T, want model", final)
	}
}

// TestTeatestStartAndQuitWithEsc verifies the program exits when Esc is
// pressed in non-vim mode (vimEnabled = false).
func TestTeatestStartAndQuitWithEsc(t *testing.T) {
	tm := newTeatestModel(t, []string{"a", "b"})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "help")
	})

	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	// The program should have exited (result not set because Esc quits
	// without selecting).
	if m.result != "" {
		t.Errorf("result = %q, want empty (Esc should quit without selecting)", m.result)
	}
}

// TestTeatestNavigationDownUp verifies cursor movement with Down and Up
// keys, and checks the final model state.
func TestTeatestNavigationDownUp(t *testing.T) {
	tm := newTeatestModel(t, []string{"alpha", "beta", "gamma"})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "alpha")
	})

	// Move down twice.
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})

	// Move back up once.
	tm.Send(tea.KeyMsg{Type: tea.KeyUp})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	// After down-down-up, cursor should be at index 1 ("beta").
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (down-down-up from 0)", m.cursor)
	}
}

// TestTeatestNavigationWrapping verifies that pressing Down on the last
// item wraps to the first item (and vice versa).
func TestTeatestNavigationWrapping(t *testing.T) {
	tm := newTeatestModel(t, []string{"one", "two", "three"})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "one")
	})

	// Press Up from the first item — should wrap to the last (index 2).
	tm.Send(tea.KeyMsg{Type: tea.KeyUp})

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 (wrapping from 0 up → last)", m.cursor)
	}
}

// TestTeatestHelpToggle verifies that ? opens the help overlay and the
// output contains the help title, then ? again closes it.
func TestTeatestHelpToggle(t *testing.T) {
	tm := newTeatestModel(t, []string{"a", "b"})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "help")
	})

	// Press ? to open help.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})

	// Wait for help text to appear.
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "Alt-o")
	})

	// Press ? again to close help.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m.mode != modeList {
		t.Errorf("mode = %v, want modeList (help should be closed)", m.mode)
	}
}

// TestTeatestEnterSelects verifies that pressing Enter on the selected
// item sets the result and quits the program.
func TestTeatestEnterSelects(t *testing.T) {
	tm := newTeatestModel(t, []string{"first", "second", "third"})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "first")
	})

	// Press Enter to select the first item ("first").
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m.result != "first" {
		t.Fatalf("result = %q, want %q", m.result, "first")
	}
}

// TestTeatestEnterAfterNavigation verifies that after navigating down,
// Enter selects the correct item.
func TestTeatestEnterAfterNavigation(t *testing.T) {
	tm := newTeatestModel(t, []string{"alpha", "beta", "gamma"})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "alpha")
	})

	// Move down to "beta".
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	// Press Enter.
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m.result != "beta" {
		t.Fatalf("result = %q, want %q", m.result, "beta")
	}
}

// TestTeatestResizeNoCrash verifies the program survives a series of
// WindowSizeMsg events without crashing, including very small sizes.
func TestTeatestResizeNoCrash(t *testing.T) {
	tm := newTeatestModel(t, []string{"a", "b", "c"})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "help")
	})

	// Resize to various dimensions.
	for _, sz := range []struct{ w, h int }{
		{120, 40},
		{80, 24},
		{50, 15},
		{40, 10},
		{200, 50},
		{80, 24},
	} {
		tm.Send(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		// Small delay to let the model process the resize and re-render.
		time.Sleep(20 * time.Millisecond)
	}

	// Quit cleanly.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m.width != 80 || m.height != 24 {
		t.Errorf("final size = %dx%d, want 80x24 (last resize)", m.width, m.height)
	}
}

// TestTeatestRapidKeystrokesNoPanic verifies that sending many rapid
// keystrokes does not cause a panic or deadlock.
func TestTeatestRapidKeystrokesNoPanic(t *testing.T) {
	tm := newTeatestModel(t, []string{"a", "b", "c", "d", "e"})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "help")
	})

	// Send 50 rapid down-arrow presses.
	for i := 0; i < 50; i++ {
		tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	}

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	// After 50 down presses on a 5-item list with wrapping, cursor
	// should be at (50 % 5) = 0.
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (50 down presses on 5 items wraps to 0)", m.cursor)
	}
}

// TestTeatestItemsMsgSetsList verifies that sending an itemsMsg
// populates the model's items and triggers a re-render showing the
// new entries.
func TestTeatestItemsMsgSetsList(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, false)
	m.src = srcFiles
	m.loading = true

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	time.Sleep(200 * time.Millisecond)

	// Send itemsMsg with test data.
	tm.Send(itemsMsg{
		src:   srcDefault,
		items: []string{"project-a", "project-b", "project-c"},
		info:  map[string]sessionInfo{},
	})

	// Wait for the items to appear in output.
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "project-a")
	})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if len(m2.items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(m2.items))
	}
	if m2.loading {
		t.Error("loading should be false after itemsMsg")
	}
}

// TestTeatestErrorMsg verifies that a uiErrMsg sets the error text and
// the error appears in the output.
func TestTeatestErrorMsg(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, false)
	m.src = srcFiles
	m.sessionPaths = map[string]string{}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	// Settle: let Init's async commands complete.
	time.Sleep(200 * time.Millisecond)

	// Send itemsMsg first to populate the list (so the error doesn't
	// get overwritten by a subsequent itemsMsg from loadCmd).
	tm.Send(itemsMsg{
		src:   srcDefault,
		items: []string{"a", "b"},
		info:  map[string]sessionInfo{},
	})
	time.Sleep(100 * time.Millisecond)

	// Now send an error message.
	tm.Send(uiErrMsg{err: errTestErr})

	// Wait for the error text to appear.
	waitForOutput(t, tm, 5*time.Second, func(s string) bool {
		return strings.Contains(s, "test error for teatest")
	})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m2.errText != "test error for teatest" {
		t.Errorf("errText = %q, want %q", m2.errText, "test error for teatest")
	}
}

// TestTeatestCopyDoneConfirmation verifies that after a copyDoneMsg, the
// confirmation text appears in the output and then is cleared by
// copyClearMsg.
func TestTeatestCopyDoneConfirmation(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, false)
	m.src = srcFiles
	m.sessionPaths = map[string]string{}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	time.Sleep(200 * time.Millisecond)
	tm.Send(itemsMsg{src: srcDefault, items: []string{"/home/user/project"}, info: map[string]sessionInfo{}})
	time.Sleep(100 * time.Millisecond)

	// Send a successful copyDoneMsg.
	tm.Send(copyDoneMsg{path: "/home/user/project", err: nil})

	// Wait for the copy confirmation to appear.
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "/home/user/project")
	})

	// Send copyClearMsg to clear the confirmation.
	tm.Send(copyClearMsg{})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m2.copyConfirm != "" {
		t.Errorf("copyConfirm = %q, want empty after copyClearMsg", m2.copyConfirm)
	}
}

// TestTeatestVimModeEscToggles verifies that in vim-enabled mode, Esc
// toggles from insert to normal mode (does NOT quit).
func TestTeatestVimModeEscToggles(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, true) // vim enabled
	m.src = srcFiles
	m.sessionPaths = map[string]string{}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	time.Sleep(200 * time.Millisecond)
	tm.Send(itemsMsg{src: srcDefault, items: []string{"a", "b"}, info: map[string]sessionInfo{}})
	time.Sleep(100 * time.Millisecond)

	// Press Esc to enter vim normal mode.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "NORMAL")
	})

	// Press Esc again in normal mode — this quits.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m2.result != "" {
		t.Errorf("result = %q, want empty (Esc in normal mode quits)", m2.result)
	}
}

// TestTeatestVimNormalJK verifies that j/k move the cursor in vim
// normal mode.
func TestTeatestVimNormalJK(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, true) // vim enabled
	m.src = srcFiles
	m.sessionPaths = map[string]string{}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	time.Sleep(200 * time.Millisecond)
	tm.Send(itemsMsg{src: srcDefault, items: []string{"alpha", "beta", "gamma"}, info: map[string]sessionInfo{}})
	time.Sleep(100 * time.Millisecond)

	// Enter vim normal mode.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "NORMAL")
	})

	// Press j (down) twice.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	// Press k (up) once.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})

	// Quit with Esc (in normal mode, Esc quits).
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	// j-j-k: 0 → 1 → 2 → 1
	if m2.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (j-j-k from 0)", m2.cursor)
	}
}

// TestTeatestVimNormalGG verifies that gg jumps to the first item.
func TestTeatestVimNormalGG(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, true)
	m.src = srcFiles
	m.sessionPaths = map[string]string{}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	time.Sleep(200 * time.Millisecond)
	tm.Send(itemsMsg{src: srcDefault, items: []string{"a", "b", "c", "d", "e"}, info: map[string]sessionInfo{}})
	time.Sleep(100 * time.Millisecond)

	// Move cursor to index 3 first.
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	time.Sleep(50 * time.Millisecond)

	// Enter vim normal mode.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "NORMAL")
	})

	// Press g, then g again (gg = go to top).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m2.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (gg jumps to top)", m2.cursor)
	}
}

// TestTeatestVimNormalG verifies that G jumps to the last item.
func TestTeatestVimNormalG(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, true)
	m.src = srcFiles
	m.sessionPaths = map[string]string{}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	time.Sleep(200 * time.Millisecond)
	tm.Send(itemsMsg{src: srcDefault, items: []string{"a", "b", "c", "d", "e"}, info: map[string]sessionInfo{}})
	time.Sleep(100 * time.Millisecond)

	// Enter vim normal mode.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "NORMAL")
	})

	// Press G (shift+g) to jump to the last item.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m2.cursor != 4 {
		t.Fatalf("cursor = %d, want 4 (G jumps to last)", m2.cursor)
	}
}

// TestTeatestVimNormalEnterSelects verifies that Enter in vim normal
// mode selects the current item and quits.
func TestTeatestVimNormalEnterSelects(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, true)
	m.src = srcFiles
	m.sessionPaths = map[string]string{}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	time.Sleep(200 * time.Millisecond)
	tm.Send(itemsMsg{src: srcDefault, items: []string{"first", "second"}, info: map[string]sessionInfo{}})
	time.Sleep(100 * time.Millisecond)

	// Enter vim normal mode.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "NORMAL")
	})

	// Move down to "second".
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	// Press Enter to select.
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m2.result != "second" {
		t.Fatalf("result = %q, want %q", m2.result, "second")
	}
}

// TestTeatestVimNormalCountPrefix verifies that a count prefix (e.g. "3j")
// moves the cursor by the specified amount.
func TestTeatestVimNormalCountPrefix(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, true)
	m.src = srcFiles
	m.sessionPaths = map[string]string{}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	time.Sleep(200 * time.Millisecond)
	tm.Send(itemsMsg{src: srcDefault, items: []string{"a", "b", "c", "d", "e", "f", "g"}, info: map[string]sessionInfo{}})
	time.Sleep(100 * time.Millisecond)

	// Enter normal mode.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "NORMAL")
	})

	// Type "3" then "j" — move down 3.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m2.cursor != 3 {
		t.Fatalf("cursor = %d, want 3 (3j from 0)", m2.cursor)
	}
}

// TestTeatestSourceSwitch verifies that ctrl+a switches to the "all"
// source and triggers a reload command.
func TestTeatestSourceSwitch(t *testing.T) {
	withIsolatedEnv(t)
	m := newModel(false, false, false)
	m.src = srcFiles
	m.loading = false
	m.sessionPaths = map[string]string{}

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	time.Sleep(200 * time.Millisecond)
	tm.Send(itemsMsg{src: srcDefault, items: []string{"initial"}, info: map[string]sessionInfo{}})
	time.Sleep(100 * time.Millisecond)

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "help")
	})

	// Press Ctrl-A to switch to srcAll.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlA})

	// Give the model a moment to process the reload.
	time.Sleep(50 * time.Millisecond)

	// Send itemsMsg to simulate the reload completing.
	tm.Send(itemsMsg{
		src:   srcAll,
		items: []string{"all-a", "all-b"},
		info:  map[string]sessionInfo{},
	})

	// Wait for the new items to appear.
	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "all-a")
	})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m2, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m2.src != srcAll {
		t.Errorf("src = %v, want srcAll after Ctrl-A", m2.src)
	}
}

// TestTeatestTypingFiltersList verifies that typing into the input box
// filters the list by fuzzy matching.
func TestTeatestTypingFiltersList(t *testing.T) {
	tm := newTeatestModel(t, []string{"apple", "apricot", "banana", "cherry"})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "apple")
	})

	// Type "ap" to filter.
	tm.Type("ap")

	// Give the model a moment to process the input and refilter.
	time.Sleep(50 * time.Millisecond)

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	// "apple" and "apricot" match "ap"; "banana" and "cherry" do not.
	if len(m.filtered) != 2 {
		t.Fatalf("len(filtered) = %d, want 2 (apple + apricot match 'ap')", len(m.filtered))
	}
}

// TestTeatestEmptyListNoCrash verifies that an empty item list does not
// cause a crash and the program can still quit.
func TestTeatestEmptyListNoCrash(t *testing.T) {
	tm := newTeatestModel(t, []string{})

	waitForOutput(t, tm, 3*time.Second, func(s string) bool {
		return strings.Contains(s, "help")
	})

	// Try navigating (should be no-op on empty list).
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyUp})

	// Try Enter (should be no-op on empty list).
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	// If Enter didn't quit (empty list), quit with Ctrl-C.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := final.(model)
	if !ok {
		t.Fatalf("final model = %T, want model", final)
	}
	if m.result != "" {
		t.Errorf("result = %q, want empty (empty list should not produce a selection)", m.result)
	}
}

// TestTeatestFinalOutputContainsHeader verifies that the final output
// includes the expected header line with keybinding hints.
func TestTeatestFinalOutputContainsHeader(t *testing.T) {
	tm := newTeatestModel(t, []string{"a", "b"})

	// Don't drain the output — just quit and check the final output.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	// Wait for the program to finish and get the final output.
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	output := normalizeOutput(string(readAllOrEmpty(t, tm.Output())))

	// The header should contain the "help" hint.
	if !strings.Contains(output, "help") {
		t.Errorf("final output does not contain 'help' header\noutput:\n%s", output)
	}
}

// readAllOrEmpty reads all available bytes from r, returning empty on
// error. Used for final output inspection after the program has finished.
func readAllOrEmpty(t *testing.T, r interface{ Read([]byte) (int, error) }) []byte {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf
}

// errTestErr is a sentinel error used in teatest tests.
var errTestErr = &testError{"test error for teatest"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// Ensure the _ import of os is not flagged (used in withIsolatedEnv via t.Setenv).
var _ = os.Setenv
