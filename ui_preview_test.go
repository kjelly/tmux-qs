package main

import (
	"strings"
	"testing"
)

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
