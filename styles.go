package main

import "github.com/charmbracelet/lipgloss"

// Default lipgloss colors. Used when the user hasn't customized
// [style] in the config file.
const (
	defaultCursorColor   = "212"
	defaultSelectedColor = "212"
	defaultBranchColor   = "36"
	defaultDimColor      = ""
	defaultErrorColor    = "203"
	defaultWarnColor     = "214"
	defaultSuccessColor  = "42"
)

// styleBundle holds the resolved lipgloss styles for the current
// session, populated from config (or defaults). Created once in
// newModel and never mutated.
type styleBundle struct {
	cursor   lipgloss.Style
	selected lipgloss.Style
	branch   lipgloss.Style
	dim      lipgloss.Style
	err      lipgloss.Style
	warn     lipgloss.Style
	success  lipgloss.Style
}

func newStyleBundle(s StyleConfig) styleBundle {
	mk := func(c, fb string) lipgloss.Style {
		if c == "" {
			c = fb
		}
		style := lipgloss.NewStyle()
		if c != "" {
			style = style.Foreground(lipgloss.Color(c))
		}
		return style
	}
	bold := func(c, fb string) lipgloss.Style {
		st := mk(c, fb)
		return st.Bold(true)
	}
	return styleBundle{
		cursor:   bold(s.Cursor, defaultCursorColor),
		selected: mk(s.Selected, defaultSelectedColor),
		branch:   mk(s.Branch, defaultBranchColor),
		dim:      mk(s.Dim, defaultDimColor),
		err:      mk(s.Error, defaultErrorColor),
		warn:     bold(s.Warn, defaultWarnColor),
		success:  mk(s.Success, defaultSuccessColor),
	}
}
