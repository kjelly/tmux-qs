package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type sourceTab struct {
	label string
	key   string
	src   sourceKind
}

type positionedSourceTab struct {
	tab  sourceTab
	text string
	x    int
	row  int
}

// These tabs are both mouse targets and a visual index of the sources most
// useful during controller-driven navigation.
var sourceTabs = []sourceTab{
	{label: "Sessions", src: srcDefault},
	{label: "All", key: "^a", src: srcAll},
	{label: "Waiting", key: "^w", src: srcWaiting},
	{label: "Tmux", key: "^t", src: srcTmux},
	{label: "Panes", key: "^e", src: srcPanes},
	{label: "Config", key: "^g", src: srcConfigs},
	{label: "Files", key: "^f", src: srcFiles},
	{label: "Commands", key: "^o", src: srcCommands},
}

func sourceTabText(tab sourceTab) string {
	if tab.key == "" {
		return "[" + tab.label + "]"
	}
	return "[" + tab.label + " " + tab.key + "]"
}

func compactSourceTabText(tab sourceTab) string {
	if tab.key == "" {
		return tab.label
	}
	return tab.label + " " + tab.key
}

// sourceTabLayout wraps complete tabs instead of allowing the terminal
// renderer to clip the later sources on narrow screens. Every advertised tab
// therefore remains visible and clickable.
func sourceTabLayout(width int) []positionedSourceTab {
	if width < 1 {
		width = 80
	}
	pos, row := 2, 0
	fullWidth := 2
	for _, tab := range sourceTabs {
		fullWidth += lipgloss.Width(sourceTabText(tab)) + 1
	}
	compact := fullWidth-1 > width
	layout := make([]positionedSourceTab, 0, len(sourceTabs))
	for _, tab := range sourceTabs {
		text := sourceTabText(tab)
		if compact {
			text = compactSourceTabText(tab)
		}
		textWidth := lipgloss.Width(text)
		if pos > 2 && pos+textWidth > width {
			row++
			pos = 2
		}
		layout = append(layout, positionedSourceTab{tab: tab, text: text, x: pos, row: row})
		pos += textWidth + 1
	}
	return layout
}

func sourceTabRowCount(width int) int {
	layout := sourceTabLayout(width)
	if len(layout) == 0 {
		return 0
	}
	return layout[len(layout)-1].row + 1
}

func sourceTabAtPosition(x, row, width int) (sourceKind, bool) {
	for _, item := range sourceTabLayout(width) {
		if item.row == row && x >= item.x && x < item.x+lipgloss.Width(item.text) {
			return item.tab.src, true
		}
	}
	return srcDefault, false
}

func (m model) sourceTabsLine() string {
	rows := make([][]string, sourceTabRowCount(m.width))
	for _, item := range sourceTabLayout(m.width) {
		text := item.text
		if m.src == item.tab.src || (m.src == srcDefault && item.tab.src == srcDefault) {
			text = m.styles.selected.Render(text)
		} else {
			text = m.styles.dim.Render(text)
		}
		rows[item.row] = append(rows[item.row], text)
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("  %s", strings.Join(row, " ")))
	}
	return strings.Join(lines, "\n")
}

func (m model) adjacentSourceTab(delta int) sourceKind {
	index := 0
	for i, tab := range sourceTabs {
		if tab.src == m.src {
			index = i
			break
		}
	}
	return sourceTabs[(index+delta+len(sourceTabs))%len(sourceTabs)].src
}
