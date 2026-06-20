package main

import (
	"reflect"
	"testing"
)

// TestFromChangeIdentical returns -1 when prev and curr are byte-for-byte
// equal. This is the "stuck" case: the agent has produced no new output
// since the last tick, so the watcher should flag the pane as stuck.
func TestFromChangeIdentical(t *testing.T) {
	const buf = "thinking...\nmore thinking\nprompt here\n"
	startIdx, text := fromChange(buf, buf)
	if startIdx != -1 {
		t.Errorf("identical buffers: startIdx = %d, want -1", startIdx)
	}
	if text != "" {
		t.Errorf("identical buffers: text = %q, want \"\"", text)
	}
}

// TestFromChangeEmptyPrev treats an empty prev as "first tick" â every
// line in curr counts as new content, so startIdx should be 0 and text
// the full curr.
func TestFromChangeEmptyPrev(t *testing.T) {
	curr := "line1\nline2\nline3"
	startIdx, text := fromChange("", curr)
	if startIdx != 0 {
		t.Errorf("empty prev: startIdx = %d, want 0", startIdx)
	}
	if text != curr {
		t.Errorf("empty prev: text = %q, want %q", text, curr)
	}
}

// TestFromChangeEmptyBoth returns -1 (no change) when both sides are
// empty. This is a degenerate "stuck on nothing" case.
func TestFromChangeEmptyBoth(t *testing.T) {
	startIdx, text := fromChange("", "")
	if startIdx != -1 {
		t.Errorf("both empty: startIdx = %d, want -1", startIdx)
	}
	if text != "" {
		t.Errorf("both empty: text = %q, want \"\"", text)
	}
}

// TestFromChangeAppendedLines covers the common "agent printed more
// output" case. Lines 0..k are unchanged; everything from k onward is
// new. startIdx is the first new line, text is the joined tail.
func TestFromChangeAppendedLines(t *testing.T) {
	prev := "l1\nl2\nl3"
	curr := "l1\nl2\nl3\nl4\nl5"
	startIdx, text := fromChange(prev, curr)
	if startIdx != 3 {
		t.Errorf("appended: startIdx = %d, want 3", startIdx)
	}
	if text != "l4\nl5" {
		t.Errorf("appended: text = %q, want %q", text, "l4\nl5")
	}
}

// TestFromChangeModifiedMiddle returns the index of the first changed
// line and the joined curr tail. The prefix is identical, so a caller
// can rely on startIdx as the offset into curr.
func TestFromChangeModifiedMiddle(t *testing.T) {
	prev := "a\nb\nc\nd"
	curr := "a\nB\nc\nd"
	startIdx, text := fromChange(prev, curr)
	if startIdx != 1 {
		t.Errorf("modified middle: startIdx = %d, want 1", startIdx)
	}
	if text != "B\nc\nd" {
		t.Errorf("modified middle: text = %q, want %q", text, "B\nc\nd")
	}
}

// TestFromChangeFirstLineDiffers is the edge where the very first line
// changed. startIdx must be 0.
func TestFromChangeFirstLineDiffers(t *testing.T) {
	prev := "old first\nrest"
	curr := "new first\nrest"
	startIdx, _ := fromChange(prev, curr)
	if startIdx != 0 {
		t.Errorf("first line diff: startIdx = %d, want 0", startIdx)
	}
}

// TestFromChangeLastLineDiffers reports the last index when the
// trailing line changed but no new lines were appended.
func TestFromChangeLastLineDiffers(t *testing.T) {
	prev := "a\nb\nc"
	curr := "a\nb\nC"
	startIdx, text := fromChange(prev, curr)
	if startIdx != 2 {
		t.Errorf("last line diff: startIdx = %d, want 2", startIdx)
	}
	if text != "C" {
		t.Errorf("last line diff: text = %q, want %q", text, "C")
	}
}

// TestFromChangeTruncatedCurr documents the current contract: when
// curr is shorter than prev and the shared prefix matches, fromChange
// returns (-1, "") â i.e. the function is not "deletion aware".
// In practice this is fine: the watcher only calls fromChange when
// the tty has been idle long enough, and a buffer that shrank between
// ticks will still trigger the prompt / stuck / idle path on its own
// merits. Pinning this behavior in a test guards against an accidental
// "len(curr) > len(prev)" -> -1 swap in the future.
func TestFromChangeTruncatedCurr(t *testing.T) {
	prev := "a\nb\nc\nd"
	curr := "a\nb"
	startIdx, text := fromChange(prev, curr)
	if startIdx != -1 {
		t.Errorf("truncated curr: startIdx = %d, want -1 (not deletion-aware)", startIdx)
	}
	if text != "" {
		t.Errorf("truncated curr: text = %q, want \"\"", text)
	}
}

// TestFromChangeTrailingNewline is the realistic case: capture-pane
// output always ends with a newline, so the "extra empty element"
// from strings.Split needs to be normalized before comparison. Two
// buffers that differ only in trailing whitespace should be reported
// as identical.
func TestFromChangeTrailingNewline(t *testing.T) {
	a := "line1\nline2\n"
	b := "line1\nline2\n"
	if startIdx, _ := fromChange(a, b); startIdx != -1 {
		t.Errorf("same with trailing newline: startIdx = %d, want -1", startIdx)
	}
}

// TestFromChangeNoTrailingNewline mirrors the above but for buffers
// without a final newline. They should also be reported as identical.
func TestFromChangeNoTrailingNewline(t *testing.T) {
	a := "line1\nline2"
	b := "line1\nline2"
	if startIdx, _ := fromChange(a, b); startIdx != -1 {
		t.Errorf("same no trailing newline: startIdx = %d, want -1", startIdx)
	}
}

// TestFromChangeSingleLine covers the smallest meaningful input: a
// one-line buffer with and without content.
func TestFromChangeSingleLine(t *testing.T) {
	if startIdx, _ := fromChange("x", "x"); startIdx != -1 {
		t.Errorf("single line same: startIdx = %d, want -1", startIdx)
	}
	if startIdx, text := fromChange("x", "y"); startIdx != 0 || text != "y" {
		t.Errorf("single line diff: got (%d, %q), want (0, \"y\")", startIdx, text)
	}
}

// TestSplitLinesBasic verifies that splitLines drops the trailing
// empty element produced by a newline-terminated string.
func TestSplitLinesBasic(t *testing.T) {
	got := splitLines("a\nb\nc\n")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitLines(\"a\\nb\\nc\\n\") = %v, want %v", got, want)
	}
}

// TestSplitLinesNoTrailingNewline preserves the last element when
// the string is not newline-terminated.
func TestSplitLinesNoTrailingNewline(t *testing.T) {
	got := splitLines("a\nb\nc")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitLines(\"a\\nb\\nc\") = %v, want %v", got, want)
	}
}

// TestSplitLinesEmpty returns an empty slice (not a slice with a
// single empty string) for an empty input. This is what callers
// expect when there is no buffer content at all.
func TestSplitLinesEmpty(t *testing.T) {
	got := splitLines("")
	if len(got) != 0 {
		t.Errorf("splitLines(\"\") = %v, want []", got)
	}
}

// TestSplitLinesSingleLine covers the one-line case both with and
// without a trailing newline.
func TestSplitLinesSingleLine(t *testing.T) {
	if got := splitLines("only"); !reflect.DeepEqual(got, []string{"only"}) {
		t.Errorf("splitLines(\"only\") = %v, want [only]", got)
	}
	if got := splitLines("only\n"); !reflect.DeepEqual(got, []string{"only"}) {
		t.Errorf("splitLines(\"only\\n\") = %v, want [only]", got)
	}
}

// TestSplitLinesOnlyNewlines documents the actual behavior: a string
// of bare newlines yields one empty string per newline segment. The
// function only strips the trailing empty element from a
// newline-terminated string; it does not normalize leading or interior
// empties. Callers that want to skip blank lines should filter
// post-split, not rely on splitLines to do it.
func TestSplitLinesOnlyNewlines(t *testing.T) {
	got := splitLines("\n\n\n")
	want := []string{"", "", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitLines(\"\\n\\n\\n\") = %v, want %v", got, want)
	}
}

// TestSplitLinesLeadingNewline covers the leading-newline case: the
// first split element is "", which is kept (only the trailing empty
// is dropped). Useful for capture-pane output that begins with a
// blank row.
func TestSplitLinesLeadingNewline(t *testing.T) {
	got := splitLines("\nrest")
	want := []string{"", "rest"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitLines(\"\\nrest\") = %v, want %v", got, want)
	}
}
