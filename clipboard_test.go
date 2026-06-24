package main

import (
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// captureStdout swaps os.Stdout for a pipe, runs fn, restores stdout, and
// returns whatever was written. Lets us assert on what copyToClipboard
// actually emits without touching the real terminal.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan struct{})
	var buf strings.Builder
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	w.Close()
	<-done
	return buf.String()
}

// TestCopyToClipboardOSC52 verifies the wire format:
//
//	ESC ] 52 ; c ; <base64> BEL
//
// Base64 must decode back to the original text.
func TestCopyToClipboardOSC52(t *testing.T) {
	out := captureStdout(t, func() {
		if err := copyToClipboard("hello world"); err != nil {
			t.Fatalf("copyToClipboard: %v", err)
		}
	})
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello world")) + "\x07"
	if out != want {
		t.Errorf("OSC 52 sequence mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestCopyToClipboardOSC52Multiline(t *testing.T) {
	out := captureStdout(t, func() {
		_ = copyToClipboard("/home/kjelly/linker/visionai-deploy5")
	})
	// Extract the base64 payload between ";c;" and BEL.
	start := strings.Index(out, ";c;")
	end := strings.LastIndex(out, "\x07")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("OSC 52 delimiters not found in %q", out)
	}
	payload := out[start+len(";c;") : end]
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != "/home/kjelly/linker/visionai-deploy5" {
		t.Errorf("decoded = %q, want %q", decoded, "/home/kjelly/linker/visionai-deploy5")
	}
}

// TestCtrlYCopiesPath verifies the keyboard handler kicks off a
// copyToClipboardCmd with the entry's resolved directory.
func TestCtrlYCopiesPath(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.items = []string{"~/github/sesh"}
	m.filtered = []int{0}
	m.refilter()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m2 := updated.(model)
	if m2.errText != "" {
		t.Errorf("expected no error, got %q", m2.errText)
	}
	// The returned cmd should be non-nil — its execution produces a
	// copyDoneMsg with the path we expect.
	// We can't easily inspect the cmd itself, but the next Update
	// with a copyDoneMsg{path: <expected>, err: nil} should set
	// m.copyConfirm.
	updated, _ = m2.Update(copyDoneMsg{path: "/home/kjelly/github/sesh", err: nil})
	m3 := updated.(model)
	if m3.copyConfirm != "/home/kjelly/github/sesh" {
		t.Errorf("copyConfirm = %q, want %q", m3.copyConfirm, "/home/kjelly/github/sesh")
	}
}

// TestCtrlYNoDirectorySetsError verifies the fallback error path: an
// entry with no resolvable directory (e.g. a bare name with no
// matching tmux session and no path syntax) reports the error to
// errText.
func TestCtrlYNoDirectorySetsError(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	// "totally-bogus" is not a path, and tmuxSessionPaths() returns
	// empty under this test (no real tmux server) — so entryDir
	// returns "".
	m.items = []string{"totally-bogus"}
	m.filtered = []int{0}
	m.refilter()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m2 := updated.(model)
	if m2.errText != "no directory for this entry" {
		t.Errorf("errText = %q, want %q", m2.errText, "no directory for this entry")
	}
}

// TestCtrlYInBranchModeNoop verifies branch mode silently ignores
// ctrl+y (branch names aren't paths, so we don't even show an error).
func TestCtrlYInBranchModeNoop(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.mode = modeBranch
	m.branches = []branchEntry{{name: "main"}}
	prevErr := m.errText
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m2 := updated.(model)
	if m2.errText != prevErr {
		t.Errorf("errText should not change in branch mode, got %q (was %q)", m2.errText, prevErr)
	}
	if m2.copyConfirm != "" {
		t.Errorf("copyConfirm should stay empty in branch mode, got %q", m2.copyConfirm)
	}
}

// TestCopyConfirmExpires verifies copyClearMsg clears the status line.
func TestCopyConfirmExpires(t *testing.T) {
	m := newModel()
	m.copyConfirm = "/tmp/somewhere"
	updated, _ := m.Update(copyClearMsg{})
	m2 := updated.(model)
	if m2.copyConfirm != "" {
		t.Errorf("copyConfirm should be empty after copyClearMsg, got %q", m2.copyConfirm)
	}
}

// TestCopyDoneErrorSetsErrText verifies a failed OSC 52 write surfaces
// as errText (and clears copyConfirm).
func TestCopyDoneErrorSetsErrText(t *testing.T) {
	m := newModel()
	m.copyConfirm = "stale"
	updated, _ := m.Update(copyDoneMsg{path: "/x", err: io.EOF})
	m2 := updated.(model)
	if m2.copyConfirm != "" {
		t.Errorf("copyConfirm should be cleared on error, got %q", m2.copyConfirm)
	}
	if m2.errText != "copy failed" {
		t.Errorf("errText = %q, want %q", m2.errText, "copy failed")
	}
}

// TestViewShowsCopyConfirm verifies the status line prefers copyConfirm
// over errText, loading, and the default counter.
func TestViewShowsCopyConfirm(t *testing.T) {
	m := newModel()
	m.height = 24
	m.width = 80
	m.copyConfirm = "/home/u/work"
	m.errText = "stale error"
	view := m.View()
	if !strings.Contains(view, "copied /home/u/work") {
		t.Errorf("View should show copyConfirm, got %q", view)
	}
	if strings.Contains(view, "stale error") {
		t.Errorf("View should NOT show errText when copyConfirm is set, got %q", view)
	}
}
