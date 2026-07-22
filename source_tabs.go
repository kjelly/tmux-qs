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

// Keep this row deliberately short: it is both a mouse target and a visual
// index of the sources most useful during controller-driven navigation.
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

// sourceTabAt returns the source tab under a terminal X coordinate. The
// leading two spaces match the list/header indentation in View().
func sourceTabAt(x int) (sourceKind, bool) {
	if x < 2 {
		return srcDefault, false
	}
	pos := 2
	for _, tab := range sourceTabs {
		text := sourceTabText(tab)
		end := pos + lipgloss.Width(text)
		if x >= pos && x < end {
			return tab.src, true
		}
		pos = end + 1
	}
	return srcDefault, false
}

func (m model) sourceTabsLine() string {
	parts := make([]string, 0, len(sourceTabs))
	for _, tab := range sourceTabs {
		text := sourceTabText(tab)
		if m.src == tab.src || (m.src == srcDefault && tab.src == srcDefault) {
			text = m.styles.selected.Render(text)
		} else {
			text = m.styles.dim.Render(text)
		}
		parts = append(parts, text)
	}
	return fmt.Sprintf("  %s", strings.Join(parts, " "))
}
