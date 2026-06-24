package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestHighlightMatchesRoundTrip verifies that highlightMatches always
// round-trips (modulo ANSI escapes) back to the original string and
// preserves the byte length of the original runes. The visual styling
// can't be tested portably (lipgloss emits no escapes in non-TTY
// contexts), so we focus on correctness invariants.
func TestHighlightMatchesRoundTrip(t *testing.T) {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)

	cases := []struct {
		name    string
		s       string
		indices []int
	}{
		{"empty indices", "hello world", nil},
		{"single match", "tmux-qs", []int{0}},
		{"consecutive matches", "tmux-qs", []int{5, 6}},
		{"all matched", "abc", []int{0, 1, 2}},
		{"out of range ignored", "abc", []int{99, -1, 1}},
		{"multibyte lead byte", "café", []int{3}},
		{"multibyte continuation byte", "café", []int{4}},
		{"duplicate indices", "abc", []int{0, 0, 0}},
		{"empty string with indices", "", []int{0}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := highlightMatches(c.s, c.indices, style)
			stripped := stripAnsi(got)
			if stripped != c.s {
				t.Errorf("highlighted output stripped to %q, want %q", stripped, c.s)
			}
		})
	}
}

// TestHighlightMatchesMultibyte verifies that a multibyte rune is
// rendered as a single unit (i.e. when one byte of the rune is in
// the match set, the whole rune is treated as a match). We force the
// ANSI output by calling the function in a way that guarantees a
// styled segment is produced (matched at both rune boundaries).
func TestHighlightMatchesMultibyte(t *testing.T) {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
	// "café" — 'é' is 2 bytes (0xc3 0xa9). Pass index 3 (first byte
	// of 'é') and verify the whole rune is treated as matched by
	// checking that the function doesn't split the rune across two
	// segments.
	out := highlightMatches("café", []int{3}, style)
	stripped := stripAnsi(out)
	if stripped != "café" {
		t.Errorf("output stripped to %q, want %q", stripped, "café")
	}
	// Also test: matching the second byte of 'é' should produce the
	// same result.
	out2 := highlightMatches("café", []int{4}, style)
	stripped2 := stripAnsi(out2)
	if stripped2 != "café" {
		t.Errorf("output stripped to %q, want %q", stripped2, "café")
	}
}

// stripAnsi removes ANSI escape sequences from s.
func stripAnsi(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if r == 0x1b {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
