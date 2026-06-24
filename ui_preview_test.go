package main

import (
	"os"
	"strings"
	"testing"
)

// TestSessionPreviewActivePaneOnly verifies that the session preview
// shows ONLY the active window's active pane (plus its capture buffer),
// and that the old "Tmux Windows:" / "Tmux Panes & Output:" headers are
// no longer present. This is a regression test for the simplified
// preview behavior.
func TestSessionPreviewActivePaneOnly(t *testing.T) {
	withTestTmuxServer(t)

	const session = "qs-preview-test"
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot get working directory: %v", err)
	}

	// Create session with two windows, each with two panes. The active
	// window (the one we just created) has its first pane active.
	if err := tmuxRun("new-session", "-d", "-s", session, "-c", wd); err != nil {
		t.Skipf("cannot create root session: %v", err)
	}
	// Discover the actual base index by querying the just-created
	// session's first window (its index is always the base index,
	// since `renumber-windows` always picks the lowest free number).
	allAfterCreate, err := tmuxRunLines("list-windows", "-t", session, "-F", "#{window_index}")
	if err != nil || len(allAfterCreate) == 0 {
		t.Skipf("cannot list windows: %v", err)
	}
	activeWin := allAfterCreate[0]
	// Add a second window with a couple of splits.
	if err := tmuxRun("new-window", "-t", session, "-c", wd); err != nil {
		t.Fatalf("new-window: %v", err)
	}
	if err := tmuxRun("split-window", "-t", session, "-c", wd); err != nil {
		t.Fatalf("split-window: %v", err)
	}
	// Make the first window active again (so we know which one should
	// appear in the preview).
	if err := tmuxRun("select-window", "-t", session+":"+activeWin); err != nil {
		t.Fatalf("select-window: %v", err)
	}
	// Discover the active pane's index in that window (it might be
	// 0 or whatever the user has set `pane-base-index` to).
	activePaneOut, err := tmuxRunOut("display-message", "-p", "-t", session+":"+activeWin, "#{pane_index}")
	if err != nil || activePaneOut == "" {
		t.Fatalf("cannot resolve pane_index: %v", err)
	}
	activePane := activePaneOut

	// Write something recognizable into the active pane's buffer so we
	// can confirm capture-pane was called on the right pane.
	marker := "QS-PREVIEW-MARKER-XYZ"
	_ = tmuxRun("send-keys", "-t", session+":"+activeWin+"."+activePane, "echo "+marker, "Enter")

	// Fire the preview command. Pass no sessionPaths so the directory
	// branch is skipped — we only want to exercise the session branch.
	cmd := loadPreviewCmd(session, nil, nil)
	msg := cmd()
	pm, ok := msg.(previewMsg)
	if !ok {
		t.Fatalf("expected previewMsg, got %T (%+v)", msg, msg)
	}

	body := pm.content
	t.Logf("preview content:\n%s", body)

	// Active window's active pane index should appear in the output.
	wantPane := activeWin + "." + activePane
	if !strings.Contains(body, wantPane) {
		t.Errorf("expected pane index %q in preview, got:\n%s", wantPane, body)
	}

	// The second window's panes (whatever indices they got) must NOT
	// appear. We discover all panes in non-active windows and check.
	allWins, _ := tmuxRunLines("list-windows", "-t", session, "-F", "#{window_index}")
	for _, w := range allWins {
		if w == activeWin {
			continue
		}
		// Enumerate panes in this non-active window; their indices
		// should never appear in the preview.
		panesInW, _ := tmuxRunLines("list-panes", "-t", session+":"+w, "-F", "#{pane_index}")
		for _, p := range panesInW {
			notWant := w + "." + p
			if strings.Contains(body, notWant) {
				t.Errorf("expected no pane %q in preview (window %s is not active), got:\n%s", notWant, w, body)
			}
		}
	}

	// The marker we echoed into the active pane must be in the capture
	// output.
	if !strings.Contains(body, marker) {
		t.Errorf("expected capture marker %q in preview, got:\n%s", marker, body)
	}

	// Old headers must be gone.
	if strings.Contains(body, "Tmux Windows:") {
		t.Errorf("expected no 'Tmux Windows:' header, got:\n%s", body)
	}
	if strings.Contains(body, "Tmux Panes & Output:") {
		t.Errorf("expected no 'Tmux Panes & Output:' header, got:\n%s", body)
	}
}

// TestSessionPreviewFallbackWhenNoActiveWindow makes sure the preview
// doesn't crash if list-panes returns an empty result. The output
// should still be a valid previewMsg (possibly with no pane section
// at all).
func TestSessionPreviewFallbackWhenNoActiveWindow(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-preview-empty"
	wd, _ := os.Getwd()
	if err := tmuxRun("new-session", "-d", "-s", session, "-c", wd); err != nil {
		t.Skipf("cannot create session: %v", err)
	}
	// Discover the actual base index by querying the just-created
	// session's first window.
	allAfterCreate, err := tmuxRunLines("list-windows", "-t", session, "-F", "#{window_index}")
	if err != nil || len(allAfterCreate) == 0 {
		t.Skipf("cannot list windows: %v", err)
	}
	activeWin := allAfterCreate[0]
	// Immediately kill the only window, leaving the session empty.
	if err := tmuxRun("kill-window", "-t", session+":"+activeWin); err != nil {
		t.Fatalf("kill-window: %v", err)
	}

	cmd := loadPreviewCmd(session, nil, nil)
	msg := cmd()
	pm, ok := msg.(previewMsg)
	if !ok {
		t.Fatalf("expected previewMsg, got %T", msg)
	}
	t.Logf("preview content (empty session):\n%s", pm.content)
	// No assertions on content beyond "no panic"; the contract is that
	// this must produce SOME previewMsg without crashing.
}

func TestUIPreviewUpdateAndLayout(t *testing.T) {
	m := newModel()
	m.items = []string{"test-session"}
	m.filtered = []int{0}
	m.cursor = 0

	// 1. Check updatePreviewCmd sets previewEntry and returns a command
	cmd := m.updatePreviewCmd()
	if cmd == nil {
		t.Fatal("expected updatePreviewCmd to return a command")
	}
	if m.previewEntry != "test-session" {
		t.Errorf("expected previewEntry to be 'test-session', got %q", m.previewEntry)
	}

	// 2. Check previewMsg update
	updated, _ := m.Update(previewMsg{entry: "test-session", content: "git status:\n  clean\nrecent commits:\n  abc1234 initial commit"})
	m = updated.(model)
	if !strings.Contains(m.previewContent, "abc1234") {
		t.Errorf("expected previewContent to be updated, got %q", m.previewContent)
	}

	// 3. Test View layout split for wide terminals (width >= 80)
	m.width = 90
	m.height = 20
	viewWide := m.View()
	if !strings.Contains(viewWide, " │ ") {
		t.Error("expected wide view to contain the side-by-side divider ' │ '")
	}

	// 4. Test View layout fallback for narrow terminals (width < 80)
	m.width = 60
	m.height = 20
	viewNarrow := m.View()
	if strings.Contains(viewNarrow, " │ ") {
		t.Error("expected narrow view to NOT contain the side-by-side divider ' │ '")
	}
}
