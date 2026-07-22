package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

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
	} else if m.mode == modeAgentSelect || m.mode == modeSnippetSelect {
		prompt = "🤖  "
	}
	headerLine := header
	if m.mode == modeAgentSelect {
		headerLine = "  Select AI Agent to open workspace with"
	} else if m.mode == modeSnippetSelect {
		headerLine = "  Snippets → " + m.snippetTarget.label() + "  [" + m.snippetTarget.command + "] · Enter: send & close · Space: send"
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
		// Visual separator between the mode indicator and the
		// user's input. The `│` (U+2502) reuses the same
		// symbol and dim style as the preview-pane left/right
		// divider (line below), so the two visual regions in
		// the popup share one design language. The trailing
		// dim space after the divider adds a small extra gap
		// so the input feels like its own block rather than
		// butting up against the rule.
		sep := m.styles.dim.Render("│")
		inputPad := m.styles.dim.Render(" ")
		b.WriteString(indicator + " " + sep + " " + inputPad + m.input.View() + "\n")
	} else {
		b.WriteString(prompt + m.input.View() + "\n")
	}
	b.WriteString(m.styles.dim.Render(headerLine) + "\n")
	if m.mode == modeSnippetSelect && m.width < previewColumnMinWidth {
		b.WriteString(m.viewSnippetInlinePreview())
	}

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
		preview := m.previewContent
		if m.mode == modeSnippetSelect {
			preview = m.snippetPanePreview()
		}
		rawPreviewLines := strings.Split(preview, "\n")
		// Apply scroll offset: skip the first m.previewOffset lines
		// (so the user can scroll DOWN through long previews).
		offset := m.previewOffset
		if offset > len(rawPreviewLines) {
			offset = len(rawPreviewLines)
		}
		visible := rawPreviewLines[offset:]
		for _, l := range visible {
			styled := l
			if lipgloss.Width(styled) > previewColumnWidth {
				styled = lipgloss.NewStyle().MaxWidth(previewColumnWidth).Render(styled)
			}
			rightLines = append(rightLines, styled)
		}
		// If the preview is scrolled (or can scroll), replace the
		// last visible line with a compact scroll-position indicator
		// like "↓ 12/40" so the user knows there's more content.
		if maxOff := previewMaxOffset(m.previewContent, m.listHeight()); maxOff > 0 || offset > 0 {
			if len(rightLines) > 0 {
				// Replace last line with the indicator.
				rightLines = append(rightLines[:len(rightLines)-1],
					m.styles.dim.Render(fmt.Sprintf("↓ %d/%d", offset+1, len(rawPreviewLines))))
			}
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
	case m.sendConfirm != "":
		b.WriteString(m.styles.success.Render("✓ " + m.sendConfirm))
	case m.copyConfirm != "":
		b.WriteString(m.styles.success.Render("✓ copied " + m.copyConfirm))
	case m.errText != "":
		b.WriteString(m.styles.err.Render("✗ " + m.errText))
	case len(m.killedUndo) > 0:
		b.WriteString(m.styles.dim.Render(fmt.Sprintf("Alt-u: undo kill (%d session(s))", len(m.killedUndo))))
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

func (m model) snippetPanePreview() string {
	head := "Target: " + m.snippetTarget.label() + "\nProcess: " + m.snippetTarget.command + "\n\n"
	if m.snippetPreview == "" {
		return head + "(no pane output)"
	}
	return head + m.snippetPreview
}

// viewSnippetInlinePreview preserves the same target context on narrow
// terminals, where the regular side preview cannot be rendered.
func (m model) viewSnippetInlinePreview() string {
	lines := strings.Split(m.snippetPreview, "\n")
	const maxLines = 3
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	var b strings.Builder
	b.WriteString(m.styles.dim.Render("  "+m.snippetTarget.label()+" · "+m.snippetTarget.command) + "\n")
	for _, line := range lines {
		if lipgloss.Width(line) > m.width-2 {
			line = lipgloss.NewStyle().MaxWidth(m.width - 2).Render(line)
		}
		b.WriteString(m.styles.dim.Render("  "+line) + "\n")
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

	if m.mode == modeAgentSelect || m.mode == modeSnippetSelect {
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
	if m.src == srcPanes || m.src == srcWindows || m.src == srcTmux {
		rawItem = strings.SplitN(rawItem, "\t", 2)[0]
	}
	trimmed := strings.TrimSpace(rawItem)
	// Apply fuzzy-match highlighting to the raw item text BEFORE
	// prepending the mark/pin prefixes (so the prefixes don't get
	// falsely highlighted) and BEFORE the isCursor style (so the
	// highlight's bold attribute persists even on the cursor row).
	//
	// In srcAll / srcDefault mode we use highlightComposite so
	// the user's query can also match the branch (the fuzzy
	// engine was given "item  branch" via
	// model.compositeEntryText, and the resulting match indices
	// span both segments). The branch segment is included
	// inline so we skip the separate branch append below.
	var text string
	if (m.src == srcAll || m.src == srcDefault) && m.matchIdx != nil {
		if br, ok := m.annots[rawItem]; ok && br != "" {
			text = highlightComposite(rawItem, br, m.matchIdx[idx],
				m.styles.highlight, m.styles.branch)
		} else {
			text = highlightMatches(rawItem, m.matchIdx[idx], m.styles.highlight)
		}
	} else {
		text = highlightMatches(rawItem, m.matchIdx[idx], m.styles.highlight)
	}
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
	// Branch append: skipped in srcAll / srcDefault when the
	// branch was already rendered inline by highlightComposite
	// above. For other sources (and for srcAll/srcDefault rows
	// with no branch info) the legacy behavior applies.
	if (m.src != srcAll && m.src != srcDefault) || m.annots[rawItem] == "" {
		if br, ok := m.annots[rawItem]; ok {
			b.WriteString(" ")
			b.WriteString(m.styles.branch.Render(" " + br))
			if m.dirty[rawItem] {
				b.WriteString(m.styles.warn.Render("*"))
			}
		}
	} else {
		// Branch already inline; just append the dirty mark
		// (highlightComposite doesn't include it).
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
	// Disambiguate when this row's directory basename appears in
	// more than one session on the server. Only rendered for
	// collisions — non-colliding sessions keep their clean
	// single-line layout. Honored only when the user hasn't
	// disabled the behavior via [naming] show_path_when_duplicate.
	if showPathWhenDuplicate(m.cfg()) {
		if si, ok := m.sessionInfo[trimmed]; ok && si.path != "" {
			if m.dupBasenames[filepath.Base(si.path)] {
				b.WriteString(" ")
				b.WriteString(m.styles.dim.Render(shortDisambiguator(si.path)))
			}
		}
	}
	// Tags for entries that have them (config-defined or auto-detected).
	if tags := m.entryTags(trimmed); len(tags) > 0 {
		b.WriteString(" ")
		b.WriteString(m.styles.dim.Render("[" + strings.Join(tags, ", ") + "]"))
	}
	// Group for entries that have one (config-defined only). Groups
	// are rendered as a short prefix in the dim color so the user
	// can scan for "is this session in my work group?" at a glance.
	if g := entryGroup(trimmed); g != "" {
		b.WriteString(" ")
		b.WriteString(m.styles.dim.Render("≡ " + g))
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

// highlightMatches returns s with the characters at the given byte
// offsets rendered with the highlight style. Indices are byte offsets
// into the original s; multi-byte runes are handled correctly because
// the function walks the string rune-by-rune and highlights the whole
// rune whenever any byte of it is in the index set.
//
// Out-of-range or empty index sets are a no-op (returns s unchanged).
// Duplicate indices pointing at the same rune are merged (the rune is
// only rendered once).
func highlightMatches(s string, indices []int, style lipgloss.Style) string {
	if len(indices) == 0 || s == "" {
		return s
	}
	// Mark every byte that belongs to a rune containing at least one
	// match index. Then walk the string rune-by-rune and emit
	// alternating styled / unstyled segments.
	highlight := make(map[int]bool, len(indices))
	for _, i := range indices {
		if i < 0 || i >= len(s) {
			continue
		}
		// Walk back to the rune start (utf8.RuneStart).
		start := i
		for start > 0 && !utf8.RuneStart(s[start]) {
			start--
		}
		// Mark every byte of that rune.
		_, size := utf8.DecodeRuneInString(s[start:])
		for k := 0; k < size && start+k < len(s); k++ {
			highlight[start+k] = true
		}
	}
	var b strings.Builder
	inHighlight := false
	var seg strings.Builder
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		isMatch := false
		for k := 0; k < size; k++ {
			if highlight[i+k] {
				isMatch = true
				break
			}
		}
		if isMatch != inHighlight {
			if seg.Len() > 0 {
				if inHighlight {
					b.WriteString(style.Render(seg.String()))
				} else {
					b.WriteString(seg.String())
				}
				seg.Reset()
			}
			inHighlight = isMatch
		}
		seg.WriteString(s[i : i+size])
		i += size
	}
	if seg.Len() > 0 {
		if inHighlight {
			b.WriteString(style.Render(seg.String()))
		} else {
			b.WriteString(seg.String())
		}
	}
	return b.String()
}

// highlightComposite splits a set of match indices — produced by
// the fuzzy engine against the composite text "rawItem  branch"
// (see model.compositeEntryText) — into the item-segment and
// branch-segment subsets, then applies the per-segment highlight
// style to each. The two-space separator between item and branch
// is not matchable; indices never land on it.
//
// We do NOT call highlightMatches against the full composite
// string with a single style, because:
//   - we want the branch segment to use m.styles.branch so the
//     user can tell at a glance which part of the row matched
//   - the item-vs-branch boundary has to be exact (we can't
//     accidentally highlight a separator character)
//
// When the indices are empty (or all out of range) we render
// both segments unstyled so the row looks identical to a
// non-fuzzy match.
func highlightComposite(rawItem, branch string, indices []int,
	itemStyle, branchStyle lipgloss.Style) string {
	if branch == "" {
		// No branch: fall back to plain item highlight, no
		// separator. This is the "annotMsg hasn't arrived
		// yet" case.
		return highlightMatches(rawItem, indices, itemStyle)
	}
	itemLen := len(rawItem)
	sepLen := 2 // "  " between item and branch
	branchStart := itemLen + sepLen

	var itemIdx, branchIdx []int
	for _, i := range indices {
		switch {
		case i < itemLen:
			itemIdx = append(itemIdx, i)
		case i >= branchStart:
			branchIdx = append(branchIdx, i-branchStart)
		}
	}

	var out string
	if len(itemIdx) > 0 {
		out = highlightMatches(rawItem, itemIdx, itemStyle)
	} else {
		out = rawItem
	}
	out += "  "
	if len(branchIdx) > 0 {
		out += branchStyle.Render(highlightMatches(branch, branchIdx, branchStyle))
	} else {
		out += branch
	}
	return out
}
