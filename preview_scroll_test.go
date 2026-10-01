package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestPreviewMaxOffset(t *testing.T) {
	cases := []struct {
		name    string
		content string
		height  int
		want    int
	}{
		{"empty content", "", 10, 0},
		{"zero height", "a\nb\nc", 0, 0},
		{"negative height", "a\nb\nc", -1, 0},
		{"content fits", "a\nb\nc", 10, 0},
		{"content exactly fits", "a\nb\nc", 3, 0},
		{"content overflows by 1", "a\nb\nc\nd", 3, 1},
		{"content overflows by many", strings.Repeat("line\n", 100), 10, 91}, // 101 lines, h=10 → max=91
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := previewMaxOffset(c.content, c.height)
			if got != c.want {
				t.Errorf("previewMaxOffset(content=%d lines, h=%d) = %d, want %d",
					strings.Count(c.content, "\n")+1, c.height, got, c.want)
			}
		})
	}
}

func TestScrollPreview(t *testing.T) {
	m := model{
		width:          100, // wide enough for preview pane
		height:         24,
		previewContent: strings.Repeat("line\n", 50), // 51 lines after split
		previewOffset:  0,
		previewEntry:   "test",
	}
	// listHeight() = 24 - 4 = 20 (prompt + source tabs + header + status)
	// maxOffset = 51 - 20 = 31
	m.scrollPreview(10)
	if m.previewOffset != 10 {
		t.Errorf("after scroll +10, offset = %d, want 10", m.previewOffset)
	}
	// Scroll past max — should clamp
	m.scrollPreview(100)
	if m.previewOffset != 31 {
		t.Errorf("after scroll past max, offset = %d, want 31", m.previewOffset)
	}
	// Scroll back to 0
	m.scrollPreview(-100)
	if m.previewOffset != 0 {
		t.Errorf("after scroll to top, offset = %d, want 0", m.previewOffset)
	}
	// Already at 0 — scrolling up is a no-op (no negative offset)
	m.scrollPreview(-5)
	if m.previewOffset != 0 {
		t.Errorf("after scroll up at top, offset = %d, want 0", m.previewOffset)
	}
}

func TestScrollPreviewNoop(t *testing.T) {
	// Narrow terminal — no preview pane
	m := model{
		width:          79, // < 80 threshold
		height:         24,
		previewContent: "line1\nline2",
	}
	m.scrollPreview(10)
	if m.previewOffset != 0 {
		t.Errorf("narrow terminal: offset should remain 0, got %d", m.previewOffset)
	}
	// Empty preview content
	m2 := model{
		width:  100,
		height: 24,
	}
	m2.scrollPreview(10)
	if m2.previewOffset != 0 {
		t.Errorf("empty preview: offset should remain 0, got %d", m2.previewOffset)
	}
}

func TestSessionPreviewStartsAtPaneTailAndKeepsManualScroll(t *testing.T) {
	m := newModel()
	m.width, m.height = 100, 24
	m.items = []string{"work", "other"}
	m.filtered = []int{0, 1}
	m.cursor = 0
	if cmd := m.updatePreviewCmd(); cmd == nil {
		t.Fatal("expected preview load for work")
	}

	lines := []string{"  5.1 [bash] @ /work"}
	for i := 1; i <= 40; i++ {
		lines = append(lines, "pane line "+strconv.Itoa(i))
	}
	paneEnd := len(lines)
	lines = append(lines, "", "Git Status:", "  clean")
	updated, _ := m.Update(previewMsg{
		generation: m.loadGeneration,
		request:    m.previewRequest,
		entry:      "work",
		content:    strings.Join(lines, "\n"),
		paneEnd:    paneEnd,
	})
	m = updated.(model)
	want := paneEnd - m.listHeight()
	if m.previewOffset != want {
		t.Fatalf("initial offset = %d, want pane tail offset %d", m.previewOffset, want)
	}
	visible := strings.Split(m.previewContent, "\n")[m.previewOffset : m.previewOffset+m.listHeight()]
	if got := visible[len(visible)-1]; got != lines[paneEnd-1] {
		t.Fatalf("last visible line = %q, want latest pane line %q", got, lines[paneEnd-1])
	}
	if strings.Contains(strings.Join(visible, "\n"), "Git Status:") {
		t.Fatal("trailing Git details displaced the pane tail")
	}
	if view := m.View(); !strings.Contains(view, lines[paneEnd-1]) {
		t.Fatalf("rendered preview does not show latest pane line: %q", lines[paneEnd-1])
	}

	m.scrollPreview(-10)
	if m.previewOffset != want-10 || m.previewFollowTail {
		t.Fatalf("scroll back: offset=%d followTail=%v", m.previewOffset, m.previewFollowTail)
	}
	updated, _ = m.Update(previewMsg{
		generation: m.loadGeneration,
		request:    m.previewRequest,
		entry:      "work",
		content:    strings.Join(lines, "\n"),
		paneEnd:    paneEnd,
	})
	m = updated.(model)
	if m.previewOffset != want-10 {
		t.Fatalf("refresh reset manual scroll to %d", m.previewOffset)
	}

	m.cursor = 1
	if cmd := m.updatePreviewCmd(); cmd == nil {
		t.Fatal("expected preview load for other")
	}
	if m.previewOffset != 0 || !m.previewFollowTail {
		t.Fatalf("new entry: offset=%d followTail=%v", m.previewOffset, m.previewFollowTail)
	}
	m.cursor = 0
	m.updatePreviewCmd() // fresh cache hit
	if m.previewOffset != want {
		t.Fatalf("cached preview offset = %d, want %d", m.previewOffset, want)
	}
}
