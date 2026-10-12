package main

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

	// Create a session with two windows, then activate the second pane in
	// the active window. This must differ from the first pane's capture.
	if err := tmuxRun("new-session", "-d", "-s", session, "-c", wd); err != nil {
		t.Skipf("cannot create root session: %v", err)
	}
	// Add a second window with a split. The active pane starts as the
	// newly created (non-first) pane in that window.
	if err := tmuxRun("new-window", "-t", session, "-c", wd); err != nil {
		t.Fatalf("new-window: %v", err)
	}
	if err := tmuxRun("split-window", "-t", session, "-c", wd); err != nil {
		t.Fatalf("split-window: %v", err)
	}
	activeWin, err := tmuxRunOut("display-message", "-p", "-t", session, "#{window_index}")
	if err != nil || activeWin == "" {
		t.Fatalf("cannot resolve active window: %v", err)
	}
	activePanes, err := tmuxRunLines("list-panes", "-t", session+":"+activeWin, "-F", "#{pane_id}")
	if err != nil || len(activePanes) < 2 {
		t.Fatalf("expected at least 2 panes in active window, got %v (err: %v)", activePanes, err)
	}
	activePaneID := activePanes[len(activePanes)-1]
	inactiveMarker := "QS-INACTIVE-FIRST-PANE-MARKER"
	if err := tmuxRun("send-keys", "-t", activePanes[0], "echo "+inactiveMarker, "Enter"); err != nil {
		t.Fatalf("write first pane marker: %v", err)
	}
	if err := tmuxRun("select-pane", "-t", activePaneID); err != nil {
		t.Fatalf("select non-first active pane: %v", err)
	}
	activePane, err := tmuxRunOut("display-message", "-p", "-t", session, "#{pane_index}")
	if err != nil || activePane == "" {
		t.Fatalf("cannot resolve active pane index: %v", err)
	}
	activeTargetID, err := tmuxRunOut("display-message", "-p", "-t", session, "#{pane_id}")
	if err != nil || activeTargetID != activePaneID {
		t.Fatalf("session target resolved to pane %q, want active pane %q (err: %v)", activeTargetID, activePaneID, err)
	}

	// Write something recognizable into the active pane's buffer so we
	// can confirm capture-pane was called on the right pane.
	marker := "QS-PREVIEW-MARKER-XYZ"
	if err := tmuxRun("send-keys", "-t", activePaneID, "echo "+marker, "Enter"); err != nil {
		t.Fatalf("write active pane marker: %v", err)
	}
	states, _, err := collectPanesOn(getTmuxServer(), WatchingConfig{}, nil, time.Now(), getTmuxCapabilities(getTmuxServer()))
	if err != nil {
		t.Fatalf("collect pane states: %v", err)
	}
	if got := activePaneIDsBySession(states)[session]; got != activePaneID {
		t.Fatalf("watcher active pane = %q, want %q", got, activePaneID)
	}

	// Fire the preview command. Pass no sessionPaths so the directory
	// branch is skipped — we only want to exercise the session branch.
	cmd := loadPreviewCmd(session, nil, nil, 0, 0)
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

	// Panes in the non-active window must NOT appear. Discover those
	// panes and check each index.
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
	if pm.paneEnd <= 0 || pm.paneEnd > len(strings.Split(body, "\n")) {
		t.Fatalf("invalid pane tail boundary %d in preview:\n%s", pm.paneEnd, body)
	}
	if strings.Contains(body, inactiveMarker) {
		t.Errorf("preview should not show the inactive first pane marker %q, got:\n%s", inactiveMarker, body)
	}

	// Old headers must be gone.
	if strings.Contains(body, "Tmux Windows:") {
		t.Errorf("expected no 'Tmux Windows:' header, got:\n%s", body)
	}
	if strings.Contains(body, "Tmux Panes & Output:") {
		t.Errorf("expected no 'Tmux Panes & Output:' header, got:\n%s", body)
	}
}

func TestSessionPreviewUsesActiveEinkWindowForEinkClient(t *testing.T) {
	withTestTmuxServer(t)
	if err := tmuxRun("set-option", "-g", "base-index", "1"); err != nil {
		t.Fatalf("set preview window indices: %v", err)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TMUX_QS_CLIENT", "preview-eink-client")
	if err := setEinkClientForced(true); err != nil {
		t.Fatalf("force e-ink client: %v", err)
	}

	const base = "qs-preview-pair"
	const twin = base + "-eink"
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := tmuxRun("new-session", "-d", "-s", base, "-c", wd); err != nil {
		t.Fatalf("new base session: %v", err)
	}
	if err := tmuxRun("new-session", "-d", "-t", base, "-s", twin); err != nil {
		t.Fatalf("new grouped session: %v", err)
	}
	if err := tmuxRun("new-window", "-t", base, "-c", wd); err != nil {
		t.Fatalf("new shared window: %v", err)
	}
	if err := tmuxRun("select-window", "-t", base+":1"); err != nil {
		t.Fatalf("select base first window: %v", err)
	}
	if err := tmuxRun("select-window", "-t", twin+":2"); err != nil {
		t.Fatalf("select e-ink second window: %v", err)
	}
	basePane, err := tmuxRunOut("display-message", "-p", "-t", base, "#{pane_id}")
	if err != nil {
		t.Fatalf("resolve base pane: %v", err)
	}
	twinPane, err := tmuxRunOut("display-message", "-p", "-t", twin, "#{pane_id}")
	if err != nil || twinPane == basePane {
		t.Fatalf("grouped sessions must have different active panes: base=%q twin=%q (err: %v)", basePane, twinPane, err)
	}
	const baseMarker = "QS-BASE-FIRST-WINDOW"
	const twinMarker = "QS-EINK-ACTIVE-WINDOW"
	if err := tmuxRun("send-keys", "-t", basePane, "echo "+baseMarker, "Enter"); err != nil {
		t.Fatalf("write base marker: %v", err)
	}
	if err := tmuxRun("send-keys", "-t", twinPane, "echo "+twinMarker, "Enter"); err != nil {
		t.Fatalf("write e-ink marker: %v", err)
	}
	states, _, err := collectPanesOn(getTmuxServer(), WatchingConfig{}, nil, time.Now(), getTmuxCapabilities(getTmuxServer()))
	if err != nil {
		t.Fatalf("collect grouped pane states: %v", err)
	}
	activePanes := activePaneIDsBySession(states)
	if activePanes[base] != basePane || activePanes[twin] != twinPane {
		t.Fatalf("grouped active panes = base:%q e-ink:%q, want base:%q e-ink:%q", activePanes[base], activePanes[twin], basePane, twinPane)
	}

	msg := loadPreviewCmd(base, nil, nil, 0, 0)()
	preview, ok := msg.(previewMsg)
	if !ok {
		t.Fatalf("expected previewMsg, got %T", msg)
	}
	if !strings.Contains(preview.content, twinMarker) || strings.Contains(preview.content, baseMarker) {
		t.Fatalf("e-ink client preview should show the grouped session's active window, got:\n%s", preview.content)
	}
	if err := setEinkClientForced(false); err != nil {
		t.Fatalf("clear e-ink client override: %v", err)
	}
	msg = loadPreviewCmd(base, nil, nil, 0, 0)()
	preview = msg.(previewMsg)
	if !strings.Contains(preview.content, baseMarker) || strings.Contains(preview.content, twinMarker) {
		t.Fatalf("normal client preview should show the base session's active window, got:\n%s", preview.content)
	}
	if err := setEinkClientForced(true); err != nil {
		t.Fatalf("restore e-ink client override: %v", err)
	}
	if err := tmuxRun("kill-session", "-t", twin); err != nil {
		t.Fatalf("remove grouped session: %v", err)
	}
	msg = loadPreviewCmd(base, nil, nil, 0, 0)()
	preview = msg.(previewMsg)
	if !strings.Contains(preview.content, baseMarker) {
		t.Fatalf("preview should fall back to base session when no grouped session exists, got:\n%s", preview.content)
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

	cmd := loadPreviewCmd(session, nil, nil, 0, 0)
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
	updated, _ := m.Update(previewMsg{generation: m.loadGeneration, request: m.previewRequest, entry: "test-session", content: "git status:\n  clean\nrecent commits:\n  abc1234 initial commit"})
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

func TestPreviewColumnWidthGrowsOnWideTerminals(t *testing.T) {
	if got := previewColumnWidthFor(80); got != 39 {
		t.Errorf("80-column preview width = %d, want 39", got)
	}
	if got := previewColumnWidthFor(165); got < 70 {
		t.Errorf("165-column preview width = %d, want at least 70", got)
	}
}

func TestPreviewIsNotScheduledWhenColumnIsHidden(t *testing.T) {
	m := newModel()
	m.width = previewColumnMinWidth - 1
	m.items = []string{"session"}
	m.filtered = []int{0}
	if cmd := m.updatePreviewCmd(); cmd != nil {
		t.Fatal("hidden preview column should not schedule external preview work")
	}
}

func TestResizeToNarrowInvalidatesPreviewRequest(t *testing.T) {
	m := newModel()
	m.width = previewColumnMinWidth
	m.previewEntry = "session"
	m.previewContent = "old"
	m.previewRequest = 4
	updated, _ := m.Update(tea.WindowSizeMsg{Width: previewColumnMinWidth - 1, Height: 20})
	got := updated.(model)
	if got.previewEntry != "" || got.previewContent != "" || got.previewRequest != 5 {
		t.Fatalf("narrow resize did not invalidate preview: %+v", got)
	}
}

// tmux reports no pane_current_path until a new pane's child takes its
// terminal; the preview must still show the session's active pane.
func TestPreviewShowsActivePaneWithEmptyCurrentPath(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMUX_QS_CONFIG", "")
	t.Setenv("TMUX_QS_CLIENT", "")
	t.Setenv("LC_IS_EINK", "")
	installFakeTmuxFormat(t, map[string]string{
		"@tmux_qs": "", "window_index": "1", "pane_index": "0",
		"pane_id": "%7", "pane_current_command": "sleep", "pane_current_path": "",
	})
	pm, ok := loadPreviewCmdOn("work", nil, nil, 0, 0, tmuxServerSpec{})().(previewMsg)
	if !ok {
		t.Fatal("expected previewMsg")
	}
	if !strings.Contains(pm.content, "1.0 [sleep]") {
		t.Fatalf("active pane missing from preview:\n%s", pm.content)
	}
}
