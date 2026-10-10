//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs the built binary in a real tmux-owned PTY; send-keys traverses the
// modal pane's input path and capture-pane observes the rendered screen.
func TestPTYModalSnippetNavigationAndToggle(t *testing.T) {
	withTestTmuxServer(t)
	if !getTmuxCapabilities(getTmuxServer()).FloatingModal {
		t.Skip("requires modal support")
	}
	binary := buildTestBinary(t)
	cfgDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfgDir, "tmux-qs"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := `[ui]
backend = "floating"
floating_modal = true
[waiting]
enabled = false
[[snippet]]
name = "Modal test"
text = ":"
submit = true
`
	if err := os.WriteFile(filepath.Join(cfgDir, "tmux-qs", "config.toml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("TMUX_QS_CONFIG", filepath.Join(cfgDir, "tmux-qs", "config.toml"))
	if err := tmuxRun("set-environment", "-t", "qs-test-root", "TMUX_QS_CONFIG", os.Getenv("TMUX_QS_CONFIG")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TMUX_QS_CLIENT", "test-modal-client")
	pane, _ := tmuxRunOut("display-message", "-p", "-t", "qs-test-root", "#{pane_id}")
	t.Setenv("TMUX_QS_CALLER_PANE", pane)
	t.Setenv("TMUX_QS_CALLER_CWD", "/tmp")
	// The child also needs the isolated configuration.
	if err := tmuxRun("set-environment", "-t", "qs-test-root", "XDG_CONFIG_HOME", cfgDir); err != nil {
		t.Fatal(err)
	}
	if err := tmuxRun("set-environment", "-t", "qs-test-root", "XDG_CACHE_HOME", os.Getenv("XDG_CACHE_HOME")); err != nil {
		t.Fatal(err)
	}
	launch := func() string {
		t.Helper()
		out, err := runOut(binary, "--server="+getTmuxServer().value, "--popup=center,90%,90%", "--toggle", "--snippets")
		if err != nil {
			t.Fatalf("launch %s: %v", out, err)
		}
		id, err := tmuxRunOut("display-message", "-p", "-t", "qs-test-root", "#{pane_id}")
		if err != nil || id == pane {
			t.Fatalf("no picker: %q %v", id, err)
		}
		return id
	}
	waitScreen := func(id, needle string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		var screen string
		for time.Now().Before(deadline) {
			screen, _ = tmuxRunOut("capture-pane", "-p", "-t", id)
			if strings.Contains(screen, needle) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("missing %q: %s", needle, screen)
	}
	id := launch()
	waitScreen(id, "Modal test")
	if err := tmuxRun("send-keys", "-t", id, "Down", "Space"); err != nil {
		t.Fatal(err)
	}
	waitScreen(id, "sent Modal test")
	if !tmuxPaneExists(getTmuxServer(), id) {
		t.Fatal("Space closed picker")
	}
	if err := tmuxRun("send-keys", "-t", id, "Escape"); err != nil {
		t.Fatal(err)
	}
	waitScreen(id, "Sessions")
	if !tmuxPaneExists(getTmuxServer(), id) {
		t.Fatal("Esc closed pane instead of returning")
	}
	if err := tmuxRun("send-keys", "-t", id, "Space"); err != nil {
		t.Fatal(err)
	}
	waitScreen(id, "Modal test")
	if err := tmuxRun("send-keys", "-t", id, "Down", "Enter"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for tmuxPaneExists(getTmuxServer(), id) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if tmuxPaneExists(getTmuxServer(), id) {
		t.Fatal("Enter failed to close picker")
	}
	id = launch()
	waitScreen(id, "Modal test")
	out, err := runOut(binary, "--server="+getTmuxServer().value, "--popup", "--toggle")
	if err != nil || tmuxPaneExists(getTmuxServer(), id) {
		t.Fatalf("toggle failed %s %v", out, err)
	}
}
