package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// layout constants used by View(). Kept here so view-related magic
// numbers live in one place. 80 is the column-count threshold at which
// the right-side preview pane appears; 42 is the total horizontal
// overhead of the preview pane + divider.
const (
	previewColumnMinWidth = 80
	previewColumnOverhead = 42
	previewColumnWidth    = previewColumnOverhead - 3 // right column = overhead - " │ "
)

func (m model) View() string {
	// Help overlay short-circuits the normal layout.
	if m.mode == modeHelp {
		return m.viewHelp()
	}

	var b strings.Builder

	for i := 0; i < m.inputPad; i++ {
		b.WriteString("\n")
	}

	prompt := m.src.prompt()
	if m.mode == modeBranch {
		prompt = "🌿  "
	} else if m.mode == modeAgentSelect {
		prompt = "🤖  "
	}
	headerLine := header
	if m.mode == modeAgentSelect {
		headerLine = "  Select AI Agent to open workspace with"
	}
	// In vimNormal mode, blur the textinput (no cursor) and show
	// a "NORMAL" indicator instead of the prompt icon. The input
	// text is still visible so the user can see their query.
	if m.mode == modeList && m.vimMode == vimNormal {
		m.input.Blur()
		countPrefix := ""
		if m.vimCount != "" {
			countPrefix = m.vimCount
		}
		indicator := m.styles.warn.Render("-- NORMAL --")
		if countPrefix != "" {
			indicator = m.styles.warn.Render("-- NORMAL " + countPrefix + " --")
		}
		b.WriteString(indicator + " " + m.input.View() + "\n")
	} else {
		b.WriteString(prompt + m.input.View() + "\n")
	}
	b.WriteString(m.styles.dim.Render(headerLine) + "\n")

	h := m.listHeight()
	end := m.offset + h
	if end > len(m.filtered) {
		end = len(m.filtered)
	}

	// Prepare left column lines
	var leftLines []string
	leftWidth := m.width
	if m.width >= previewColumnMinWidth {
		leftWidth = m.width - previewColumnOverhead
	}

	for row := m.offset; row < end; row++ {
		idx := m.filtered[row]
		line := m.renderEntry(idx, row == m.cursor)
		if lipgloss.Width(line) > leftWidth {
			line = lipgloss.NewStyle().MaxWidth(leftWidth).Render(line)
		}
		leftLines = append(leftLines, line)
		// Inline detail line (only used when screen width is narrow, otherwise preview pane is used)
		if m.showDetail && row == m.cursor && m.width < previewColumnMinWidth {
			if d := m.detailLine(); d != "" {
				for _, dl := range strings.Split(strings.TrimSuffix(d, "\n"), "\n") {
					leftLines = append(leftLines, dl)
				}
			}
		}
	}
	// Pad leftLines to h
	for len(leftLines) < h {
		leftLines = append(leftLines, "")
	}

	// Prepare right column lines (if screen width is wide enough)
	var rightLines []string
	if m.width >= previewColumnMinWidth {
		rawPreviewLines := strings.Split(m.previewContent, "\n")
		for _, l := range rawPreviewLines {
			styled := l
			if lipgloss.Width(styled) > previewColumnWidth {
				styled = lipgloss.NewStyle().MaxWidth(previewColumnWidth).Render(styled)
			}
			rightLines = append(rightLines, styled)
		}
	}
	// Pad rightLines to h
	for len(rightLines) < h {
		rightLines = append(rightLines, "")
	}

	// Join left and right columns side-by-side
	if m.width >= previewColumnMinWidth {
		leftW := m.width - previewColumnOverhead
		for i := 0; i < h; i++ {
			leftLine := leftLines[i]
			if leftLen := lipgloss.Width(leftLine); leftLen < leftW {
				leftLine += strings.Repeat(" ", leftW-leftLen)
			}
			b.WriteString(leftLine + m.styles.dim.Render(" │ ") + rightLines[i] + "\n")
		}
	} else {
		for i := 0; i < h; i++ {
			b.WriteString(leftLines[i] + "\n")
		}
	}

	switch {
	case m.copyConfirm != "":
		b.WriteString(m.styles.success.Render("✓ copied " + m.copyConfirm))
	case m.errText != "":
		b.WriteString(m.styles.err.Render("✗ " + m.errText))
	case m.loading:
		b.WriteString(m.styles.dim.Render("…"))
	case m.waiting.totalWaiting > 0:
		b.WriteString(m.styles.warn.Render(fmt.Sprintf("⚠ %d session(s) waiting", m.waiting.totalWaiting)))
		b.WriteString(" ")
		b.WriteString(m.styles.dim.Render(fmt.Sprintf("%d/%d", len(m.filtered), m.entryCount())))
	default:
		b.WriteString(m.styles.dim.Render(fmt.Sprintf("%d/%d", len(m.filtered), m.entryCount())))
	}
	return b.String()
}

// detailLine renders the per-pane detail for the cursor row, indented
// under the list. Returns "" if there's nothing useful to show. Used
// when m.showDetail is true.
func (m model) detailLine() string {
	if m.mode != modeList || len(m.filtered) == 0 {
		return ""
	}
	idx := m.filtered[m.cursor]
	entry := strings.TrimSpace(m.items[idx])
	panes := m.waiting.panesFor(entry)
	if len(panes) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range panes {
		age := formatAge(p.ttyIdle)
		b.WriteString(m.styles.dim.Render(fmt.Sprintf("    └ %s @ %s.%d · %s · %s",
			p.cmd, p.window, p.index, p.signal, age)))
		b.WriteString("\n")
	}
	return b.String()
}

// viewHelp renders the help overlay (modeHelp). Layout: a small title,
// a blank line, the helpText entries, a blank line, and a dismiss hint.
// All lines are written as-is; the popup's existing top-margin logic
// (recalcInputPad) is bypassed for help because the overlay uses the
// full terminal height.
func (m model) viewHelp() string {
	var b strings.Builder
	// Match the list-mode top padding so the overlay sits where the
	// list normally does.
	for i := 0; i < m.inputPad; i++ {
		b.WriteString("\n")
	}
	b.WriteString(m.styles.dim.Render("  ? for help · Esc to close") + "\n")
	for _, line := range helpText {
		if line == "" {
			b.WriteString("\n")
		} else {
			b.WriteString(m.styles.dim.Render(line) + "\n")
		}
	}
	return b.String()
}

func (m model) renderEntry(idx int, isCursor bool) string {
	var b strings.Builder
	if isCursor {
		b.WriteString(m.styles.cursor.Render("> "))
	} else {
		b.WriteString("  ")
	}

	if m.mode == modeAgentSelect {
		text := m.items[idx]
		if isCursor {
			text = m.styles.selected.Render(text)
		}
		b.WriteString(text)
		return b.String()
	}

	if m.mode == modeBranch {
		e := m.branches[idx]
		text := e.name
		var note string
		switch {
		case e.current:
			note = " *current"
		case e.worktreePath != "":
			note = " ⇒ " + e.worktreePath
		case e.remote:
			note = " (remote)"
		}
		if isCursor {
			text = m.styles.selected.Render(text)
		}
		b.WriteString(text)
		b.WriteString(m.styles.dim.Render(note))
		return b.String()
	}

	rawItem := m.items[idx]
	if m.src == srcPanes {
		rawItem = strings.SplitN(rawItem, "\t", 2)[0]
	}
	trimmed := strings.TrimSpace(rawItem)
	text := rawItem
	if m.marked != nil && m.marked[idx] {
		text = m.styles.success.Render("✓ ") + text
	}
	if m.pinned[trimmed] {
		text = "📌 " + text
	}
	if isCursor {
		text = m.styles.selected.Render(text)
	}
	b.WriteString(text)
	if br, ok := m.annots[rawItem]; ok {
		b.WriteString(" ")
		b.WriteString(m.styles.branch.Render(" " + br))
		if m.dirty[rawItem] {
			b.WriteString(m.styles.warn.Render("*"))
		}
	}
	// For entries that are existing tmux sessions, append a small
	// age hint ("3m", "2h", "1d") so the user can see at a glance
	// which sessions are stale.
	if meta, ok := m.sessionMeta[trimmed]; ok {
		ref := meta.lastActive
		if !meta.hasLastAct || ref.IsZero() {
			ref = meta.created
		}
		if !ref.IsZero() {
			age := formatAge(time.Since(ref))
			b.WriteString(" ")
			b.WriteString(m.styles.dim.Render(age))
		}
	}
	// Window/pane counts for existing tmux sessions.
	if si, ok := m.sessionInfo[trimmed]; ok && si.windows > 0 {
		b.WriteString(" ")
		b.WriteString(m.styles.dim.Render(fmt.Sprintf("%dw %dp", si.windows, si.panes)))
	}
	// Tags for entries that have them (config-defined or auto-detected).
	if tags := m.entryTags(trimmed); len(tags) > 0 {
		b.WriteString(" ")
		b.WriteString(m.styles.dim.Render("[" + strings.Join(tags, ", ") + "]"))
	}
	if procs := m.waiting.lookup(rawItem); len(procs) > 0 {
		// The remaining column width for the waiting-process suffix
		// is the screen width minus what we've already rendered
		// (marker + item + branch + age) and a small safety margin.
		used := lipgloss.Width(b.String()) + watchSuffixSafety
		remaining := m.width - used
		if remaining < 0 {
			remaining = 0
		}
		b.WriteString(" ")
		b.WriteString(m.styles.warn.Render("⚠ ⏳ waiting"))
		if remaining > 0 {
			suffix, _ := formatProcs(procs, remaining)
			if suffix != "" {
				b.WriteString(" ")
				b.WriteString(m.styles.dim.Render(suffix))
			}
		}
	}
	return b.String()
}
