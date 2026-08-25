package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestParseEinkWidths(t *testing.T) {
	got := parseEinkWidths("167, 165,garbage,0,-1,167")
	for _, width := range []int{165, 167} {
		if !isEinkWidth(width, got) {
			t.Errorf("width %d should match %#v", width, got)
		}
	}
	for _, width := range []int{-1, 0, 166, 168, 200, 220} {
		if isEinkWidth(width, got) {
			t.Errorf("width %d should not match %#v", width, got)
		}
	}
}

func TestEinkBaseSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"tmux-qs-eink", "tmux-qs"},
		{" work-eink ", "work"},
		{"work", ""},
	} {
		if got := einkBaseSession(tc.name); got != tc.want {
			t.Errorf("einkBaseSession(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEinkClientOverrideRoundTrip(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TMUX_QS_CLIENT", "client-eink-test")
	t.Setenv("TMUX", "")

	if einkClientForced() {
		t.Fatal("new client should not be forced")
	}
	if err := setEinkClientForced(true); err != nil {
		t.Fatalf("setEinkClientForced(true): %v", err)
	}
	if !einkClientForced() {
		t.Fatal("client override was not persisted")
	}
	if err := setEinkClientForced(false); err != nil {
		t.Fatalf("setEinkClientForced(false): %v", err)
	}
	if einkClientForced() {
		t.Fatal("client override was not cleared")
	}
}

func TestAutoForceEinkClientUsesCurrentClientOnly(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LC_IS_EINK", "1")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_QS_CLIENT", "client-eink-test")

	if err := autoForceEinkClient(); err != nil {
		t.Fatalf("autoForceEinkClient(): %v", err)
	}
	if !einkClientForced() {
		t.Fatal("LC_IS_EINK=1 did not persist the current client override")
	}

	t.Setenv("TMUX_QS_CLIENT", "client-normal-test")
	if einkClientForced() {
		t.Fatal("e-ink override leaked to another client")
	}
}

func TestAutoForceEinkClientIgnoresOtherValues(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LC_IS_EINK", "0")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_QS_CLIENT", "client-normal-test")

	if err := autoForceEinkClient(); err != nil {
		t.Fatalf("autoForceEinkClient(): %v", err)
	}
	if einkClientForced() {
		t.Fatal("LC_IS_EINK=0 unexpectedly forced the current client")
	}
}

func TestTmuxThemeUsesForcedClientAsLight(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TMUX", "test-server,1,0")
	t.Setenv("TMUX_QS_CLIENT", "client-eink-test")
	if err := setEinkClientForced(true); err != nil {
		t.Fatalf("setEinkClientForced(true): %v", err)
	}

	dark, ok := tmuxHasDarkBackground()
	if !ok || dark {
		t.Fatalf("tmuxHasDarkBackground() = dark:%v ok:%v, want light forced client", dark, ok)
	}
}

func TestParseEinkWidthsFallsBackToDefaults(t *testing.T) {
	for _, raw := range []string{"", " , ", "bad,0,-4"} {
		got := parseEinkWidths(raw)
		if !isEinkWidth(167, got) || !isEinkWidth(165, got) {
			t.Fatalf("parseEinkWidths(%q) = %#v, want default 167,165", raw, got)
		}
	}
}

func TestInitThemeDefaultsToDarkOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_QS_THEME", "")
	previous := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(previous) })
	lipgloss.SetHasDarkBackground(false)

	if watch := initTheme(); watch {
		t.Fatal("outside tmux should not start the theme watcher")
	}
	if !lipgloss.HasDarkBackground() {
		t.Fatal("outside tmux should fall back to dark")
	}
}

func TestInitThemeOverrideStillWins(t *testing.T) {
	t.Setenv("TMUX_QS_THEME", "light")
	previous := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(previous) })

	if watch := initTheme(); watch {
		t.Fatal("explicit override should disable watcher")
	}
	if lipgloss.HasDarkBackground() {
		t.Fatal("light override did not apply")
	}
}
