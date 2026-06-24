package main

import "github.com/charmbracelet/lipgloss"

// Adaptive-color palette. Dark variants keep close to the legacy
// 256-color look (so existing dark-mode users see no regression);
// Light variants are picked to stay readable on a white background.
// See the go-tui-theme skill for the rule of thumb: light = deep &
// saturated (Tailwind 600-900), dark = bright (Tailwind 300-400).
var (
	colorCursor    = lipgloss.AdaptiveColor{Light: "#7C2D12", Dark: "#FDA4AF"} // close to legacy 212
	colorSelected  = lipgloss.AdaptiveColor{Light: "#7C2D12", Dark: "#F9A8D4"} // close to legacy 212
	colorBranch    = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"} // close to legacy 36
	colorDim       = lipgloss.AdaptiveColor{Light: "#4B5563", Dark: "#9CA3AF"}
	colorError     = lipgloss.AdaptiveColor{Light: "#991B1B", Dark: "#FCA5A5"} // close to legacy 203
	colorWarn      = lipgloss.AdaptiveColor{Light: "#92400E", Dark: "#FCD34D"} // close to legacy 214
	colorSuccess   = lipgloss.AdaptiveColor{Light: "#065F46", Dark: "#6EE7B7"} // close to legacy 42
	// Highlight (fuzzy-match) is the only style that was historically
	// a single bright color (226) regardless of theme — which made it
	// nearly invisible on a white background. The Light variant is
	// near-black so matched characters pop; the Dark variant keeps the
	// bright yellow that looks right on a black background.
	colorHighlight = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#FACC15"}
)

// styleBundle holds the resolved lipgloss styles for the current
// session, populated from config (or defaults). Created once in
// newModel and never mutated.
type styleBundle struct {
	cursor    lipgloss.Style
	selected  lipgloss.Style
	branch    lipgloss.Style
	dim       lipgloss.Style
	err       lipgloss.Style
	warn      lipgloss.Style
	success   lipgloss.Style
	highlight lipgloss.Style
}

// resolveHighlight returns the color a user-configured highlight
// should resolve to. Priority:
//  1. StyleConfig.Highlight (explicit, most specific).
//  2. StyleConfig.Warn (legacy: highlight reused the warn color so old
//     configs keep working unchanged).
//  3. colorHighlight default (Light: #111827, Dark: #FACC15).
//
// A non-empty s.Highlight / s.Warn is a single ANSI 256-color
// number (e.g. "226") — we mirror it to both Light and Dark so the
// user's choice is preserved verbatim.
func resolveHighlight(s StyleConfig) lipgloss.AdaptiveColor {
	override := s.Highlight
	if override == "" {
		override = s.Warn
	}
	if override != "" {
		return lipgloss.AdaptiveColor{Light: string(lipgloss.Color(override)), Dark: string(lipgloss.Color(override))}
	}
	return colorHighlight
}

func newStyleBundle(s StyleConfig) styleBundle {
	mk := func(c lipgloss.AdaptiveColor, fb lipgloss.AdaptiveColor) lipgloss.Style {
		style := lipgloss.NewStyle()
		style = style.Foreground(c)
		return style
	}
	bold := func(c lipgloss.AdaptiveColor, fb lipgloss.AdaptiveColor) lipgloss.Style {
		st := mk(c, fb)
		return st.Bold(true)
	}
	// Highlight style is bold + colored. Bold makes the matched
	// characters visually distinct even when the cursor's foreground
	// color matches the highlight's foreground color.
	boldColored := func(c lipgloss.AdaptiveColor, fb lipgloss.AdaptiveColor) lipgloss.Style {
		st := mk(c, fb)
		return st.Bold(true)
	}
	return styleBundle{
		cursor:    bold(colorCursor, colorCursor),
		selected:  mk(colorSelected, colorSelected),
		branch:    mk(colorBranch, colorBranch),
		dim:       mk(colorDim, colorDim),
		err:       mk(colorError, colorError),
		warn:      bold(colorWarn, colorWarn),
		success:   mk(colorSuccess, colorSuccess),
		highlight: boldColored(resolveHighlight(s), colorHighlight),
	}
}
