package main

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// sanitizePanePreview makes text obtained from tmux capture-pane safe to put
// back into Bubble Tea's output. A captured pane can contain an application's
// raw terminal escape sequences (notably when it is another tmux-qs popup).
// Rendering those sequences verbatim lets the preview clear, move, or scroll
// the picker terminal. Keep printable text and newlines only.
func sanitizePanePreview(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipTerminalEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// A malformed byte is not useful preview content, and must not
			// accidentally become a terminal control byte.
			i++
			continue
		}
		i += size
		switch r {
		case '\n':
			b.WriteRune(r)
		case '\t':
			b.WriteString("    ")
		case '\r':
			// capture-pane occasionally preserves CR. Dropping it prevents a
			// preview line from returning the cursor to column zero.
		default:
			if !unicode.IsControl(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// skipTerminalEscape advances past one common ECMA-48 escape sequence. It is
// deliberately conservative: an incomplete sequence consumes the rest of the
// captured string, which is safer than leaking a partial control sequence to
// the real terminal.
func skipTerminalEscape(s string, start int) int {
	i := start + 1
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: ESC [ parameters/intermediates final
		i++
		for i < len(s) {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
			i++
		}
		return i
	case ']', 'P', '^', '_': // OSC/DCS/PM/APC, terminated by BEL or ST
		i++
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return i
	default:
		// Single-character and charset-designation escapes.
		return i + 1
	}
}
