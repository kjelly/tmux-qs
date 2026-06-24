package main

import (
	"strings"
	"testing"
)

// TestViewNormalModeRendersSeparator is the core contract: in
// vimNormal mode, the line that holds the mode indicator and the
// user's input must contain the dim-styled "│" character between
// them. Without this, `--NORMAL--` and the user's text run
// together visually and it's hard to tell at a glance where the
// indicator ends and the query begins.
func TestViewNormalModeRendersSeparator(t *testing.T) {
	m := newModel()
	m.mode = modeList
	m.vimMode = vimNormal
	m.width = 80
	m.height = 24
	m.input.SetValue("work")

	view := m.View()
	if !strings.Contains(view, "-- NORMAL --") {
		t.Errorf("expected '-- NORMAL --' in view, got:\n%s", view)
	}
	if !strings.Contains(view, "│") {
		t.Errorf("expected '│' separator in view, got:\n%s", view)
	}
	if !strings.Contains(view, "work") {
		t.Errorf("expected user input 'work' in view, got:\n%s", view)
	}
}

// TestViewNormalModeSeparatorComesAfterIndicator verifies the
// ordering: indicator first, then separator, then input. We
// look for the position of "-- NORMAL --" and "│" in the view
// and check that the indicator precedes the separator.
func TestViewNormalModeSeparatorComesAfterIndicator(t *testing.T) {
	m := newModel()
	m.mode = modeList
	m.vimMode = vimNormal
	m.width = 80
	m.height = 24
	m.input.SetValue("work")

	view := m.View()
	// Take only the first line (the input row); later lines
	// may legitimately contain other characters (e.g. the
	// dim "│" rendered in the list, the header, etc.).
	firstLine := strings.SplitN(view, "\n", 2)[0]
	idxIndicator := strings.Index(firstLine, "-- NORMAL --")
	idxSep := strings.Index(firstLine, "│")
	idxInput := strings.Index(firstLine, "work")
	if idxIndicator < 0 || idxSep < 0 || idxInput < 0 {
		t.Fatalf("missing one of indicator/sep/input in first line %q", firstLine)
	}
	if !(idxIndicator < idxSep && idxSep < idxInput) {
		t.Errorf("expected indicator < sep < input, got %d < %d < %d in %q",
			idxIndicator, idxSep, idxInput, firstLine)
	}
}

// TestViewNormalModeCountPrefixKeepsSeparator exercises the
// count-prefix path (e.g. user pressed "5" then "j"): the
// indicator renders as "-- NORMAL 5 --" and the separator must
// still appear after it. This guards against a refactor that
// accidentally hardcodes the indicator to the no-prefix
// variant.
func TestViewNormalModeCountPrefixKeepsSeparator(t *testing.T) {
	m := newModel()
	m.mode = modeList
	m.vimMode = vimNormal
	m.width = 80
	m.height = 24
	m.vimCount = "5"
	m.input.SetValue("work")

	view := m.View()
	firstLine := strings.SplitN(view, "\n", 2)[0]
	if !strings.Contains(firstLine, "-- NORMAL 5 --") {
		t.Errorf("expected '-- NORMAL 5 --' in first line, got:\n%s", firstLine)
	}
	idxIndicator := strings.Index(firstLine, "-- NORMAL 5 --")
	idxSep := strings.Index(firstLine, "│")
	if idxIndicator < 0 || idxSep < 0 {
		t.Fatalf("missing indicator or sep in first line %q", firstLine)
	}
	if idxIndicator >= idxSep {
		t.Errorf("expected '-- NORMAL 5 --' before separator, got indicator=%d sep=%d in %q",
			idxIndicator, idxSep, firstLine)
	}
}

// TestViewNormalModeEmptyInputShowsSeparator is the empty-input
// boundary: when the user hasn't typed anything, the input
// field still renders (as a placeholder cursor). The separator
// must still be visible so the user can tell "I'm in NORMAL
// mode, the input is empty, I can start pressing j/k".
func TestViewNormalModeEmptyInputShowsSeparator(t *testing.T) {
	m := newModel()
	m.mode = modeList
	m.vimMode = vimNormal
	m.width = 80
	m.height = 24
	// No input set — empty query is the common case immediately
	// after pressing Esc to enter normal mode.

	view := m.View()
	firstLine := strings.SplitN(view, "\n", 2)[0]
	if !strings.Contains(firstLine, "-- NORMAL --") {
		t.Errorf("expected indicator in first line, got:\n%s", firstLine)
	}
	if !strings.Contains(firstLine, "│") {
		t.Errorf("expected separator in first line, got:\n%s", firstLine)
	}
}

// TestViewInsertModeUnchanged is a regression guard: in
// vimInsert mode, the view must NOT show "-- NORMAL --" or the
// "│" separator on the input line (those are normal-mode
// constructs only). The first line of the rendered view should
// instead start with the src prompt (e.g. "⚡  ").
func TestViewInsertModeUnchanged(t *testing.T) {
	m := newModel()
	m.mode = modeList
	m.vimMode = vimInsert
	m.width = 80
	m.height = 24
	m.input.SetValue("work")

	view := m.View()
	firstLine := strings.SplitN(view, "\n", 2)[0]
	if strings.Contains(firstLine, "-- NORMAL --") {
		t.Errorf("insert mode should not render -- NORMAL --, got:\n%s", firstLine)
	}
	// The first non-empty character in the input row should
	// be the source prompt icon, not a dim "│". We don't pin
	// the exact glyph (it depends on m.src.prompt()) but we
	// do verify that the dim-style prefix from normal mode
	// is absent: the "│" appears later in the view (on the
	// preview-pane divider line) so we can't just check for
	// absence. Instead, we assert the first line starts with
	// the prompt by trimming the leading spaces and checking
	// the first char is not "│".
	trimmed := strings.TrimLeft(firstLine, " ")
	if strings.HasPrefix(trimmed, "│") {
		t.Errorf("insert mode first line should not start with '│', got %q", firstLine)
	}
}

// TestViewNormalModePreservesInputValue guards against a
// refactor that accidentally puts the separator BETWEEN
// characters of the input (e.g. by passing the wrong string
// to m.input.View()). The input value must appear verbatim
// after the separator.
func TestViewNormalModePreservesInputValue(t *testing.T) {
	m := newModel()
	m.mode = modeList
	m.vimMode = vimNormal
	m.width = 80
	m.height = 24
	m.input.SetValue("alpha beta")

	view := m.View()
	firstLine := strings.SplitN(view, "\n", 2)[0]
	if !strings.Contains(firstLine, "alpha beta") {
		t.Errorf("expected 'alpha beta' verbatim in first line, got:\n%s", firstLine)
	}
	// And the separator must come before the input.
	idxSep := strings.Index(firstLine, "│")
	idxInput := strings.Index(firstLine, "alpha beta")
	if idxSep < 0 || idxInput < 0 || idxSep >= idxInput {
		t.Errorf("expected separator before input in first line, got sep=%d input=%d in %q",
			idxSep, idxInput, firstLine)
	}
}
