package main

import (
	"strings"
	"testing"
)

func TestSanitizePanePreviewRemovesTerminalControls(t *testing.T) {
	raw := "before\r\n\x1b[2J\x1b[Hafter\tcolumn\x00\x1b]0;tmux-qs\x07done\x1b["
	got := sanitizePanePreview(raw)
	if got != "before\nafter    columndone" {
		t.Fatalf("sanitizePanePreview(%q) = %q", raw, got)
	}
	if strings.ContainsAny(got, "\x1b\r\x00") {
		t.Fatalf("unsafe control byte remained in %q", got)
	}
}

func TestSanitizePanePreviewPreservesPrintableUnicode(t *testing.T) {
	got := sanitizePanePreview("🤖 tmux-qs\n台灣")
	if got != "🤖 tmux-qs\n台灣" {
		t.Fatalf("sanitizePanePreview changed printable text: %q", got)
	}
}
