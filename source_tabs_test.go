package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSourceTabAtMapsVisibleTabs(t *testing.T) {
	pos := 2
	for _, tab := range sourceTabs {
		got, ok := sourceTabAtPosition(pos+1, 0, 200)
		if !ok || got != tab.src {
			t.Errorf("sourceTabAt(%d) = %v, %v; want %v, true", pos+1, got, ok, tab.src)
		}
		pos += len([]rune(sourceTabText(tab))) + 1
	}
}

func TestSourceTabAtIgnoresSeparators(t *testing.T) {
	if _, ok := sourceTabAtPosition(0, 0, 200); ok {
		t.Fatal("leading padding should not activate a source tab")
	}
}

func TestNarrowSourceTabsRemainVisibleAndClickable(t *testing.T) {
	const width = 42
	layout := sourceTabLayout(width)
	if sourceTabRowCount(width) < 2 {
		t.Fatal("narrow source tabs should wrap")
	}
	if len(layout) != len(sourceTabs) {
		t.Fatalf("layout has %d tabs, want %d", len(layout), len(sourceTabs))
	}
	for _, item := range layout {
		got, ok := sourceTabAtPosition(item.x, item.row, width)
		if !ok || got != item.tab.src {
			t.Errorf("click at (%d,%d) = %v,%v; want %v,true", item.x, item.row, got, ok, item.tab.src)
		}
	}
}

func TestAdjacentSourceTabWraps(t *testing.T) {
	m := model{src: srcDefault}
	if got := m.adjacentSourceTab(-1); got != sourceTabs[len(sourceTabs)-1].src {
		t.Fatalf("previous tab = %v, want final tab", got)
	}
	if got := m.adjacentSourceTab(1); got != sourceTabs[1].src {
		t.Fatalf("next tab = %v, want second tab", got)
	}
}

func TestArrowKeysDoNotChangeSource(t *testing.T) {
	m := newModel()
	m.src = srcDefault
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := updated.(model).src; got != srcDefault {
		t.Fatalf("right arrow changed source to %v", got)
	}
	updated, _ = updated.(model).Update(tea.KeyMsg{Type: tea.KeyLeft})
	if got := updated.(model).src; got != srcDefault {
		t.Fatalf("left arrow changed source to %v", got)
	}
}

func TestCtrlFActivatesFilesSource(t *testing.T) {
	m := newModel()
	m.currentPath = t.TempDir()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	got := updated.(model)
	if got.src != srcFiles || got.fileSearchDir != m.currentPath {
		t.Fatalf("Ctrl-f source=%v dir=%q, want files at %q", got.src, got.fileSearchDir, m.currentPath)
	}
}
