//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Runs the built binary in a real tmux-owned PTY; send-keys traverses the
// modal pane's input path and capture-pane observes the rendered screen.
func TestPTYModalSnippetNavigationAndToggle(t *testing.T) {
	withTestTmuxServer(t)
	requireTmux38Capability(t, getTmuxCapabilities(getTmuxServer()).FloatingModal, "modal floating panes")
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
	pidText, err := tmuxRunOut("display-message", "-p", "-t", id, "#{pane_pid}")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatal(err)
	}
	out, err := runOut(binary, "--server="+getTmuxServer().value, "--popup", "--toggle")
	if err != nil || tmuxPaneExists(getTmuxServer(), id) {
		t.Fatalf("toggle failed %s %v", out, err)
	}
	waitProcessExit(t, pid)
}

func TestPTYModalSelectsDifferentPaneInSameWindow(t *testing.T) {
	withTestTmuxServer(t)
	requireTmux38Capability(t, getTmuxCapabilities(getTmuxServer()).FloatingModal, "modal floating panes")
	binary := buildTestBinary(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(config, []byte("[ui]\nbackend = 'floating'\nfloating_modal = true\n[waiting]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_QS_CONFIG", config)
	t.Setenv("XDG_CACHE_HOME", dir)
	for _, name := range []string{"TMUX_QS_CONFIG", "XDG_CACHE_HOME"} {
		if err := tmuxRun("set-environment", "-t", "qs-test-root", name, os.Getenv(name)); err != nil {
			t.Fatal(err)
		}
	}
	// The caller may retain ordinary exited panes; the modal picker must close.
	if err := tmuxRun("set-option", "-w", "-t", "qs-test-root", "remain-on-exit", "on"); err != nil {
		t.Fatal(err)
	}
	first, err := tmuxRunOut("display-message", "-p", "-t", "qs-test-root", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	second, err := tmuxRunOut("split-window", "-d", "-t", first, "-P", "-F", "#{pane_id}", "sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	targetRow, err := tmuxRunOut("display-message", "-p", "-t", second, "#{session_name}:#{window_index}.#{pane_index}")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "attach")
	script := "#!/bin/sh\nexec tmux -L " + shellQuote(getTmuxServer().value) + " attach-session -t qs-test-root\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	clientPTY := startPTY(t, wrapper, append(os.Environ(), "TMUX=", "TERM=xterm-256color"), 30, 100)
	_ = clientPTY
	deadline := time.Now().Add(5 * time.Second)
	client := ""
	for time.Now().Before(deadline) {
		client, _ = tmuxRunOut("list-clients", "-F", "#{client_name}")
		if client != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if client == "" {
		t.Fatal("client did not attach")
	}
	t.Setenv("TMUX_QS_CLIENT", client)
	t.Setenv("TMUX_QS_CALLER_PANE", first)
	t.Setenv("TMUX_QS_CALLER_CWD", dir)
	for _, zoom := range []bool{false, true} {
		if err := tmuxRun("select-pane", "-t", first); err != nil {
			t.Fatal(err)
		}
		if zoom {
			if err := tmuxRun("resize-pane", "-Z", "-t", first); err != nil {
				t.Fatal(err)
			}
		}
		out, err := runOut(binary, "--server="+getTmuxServer().value, "--popup=center,90%,90%")
		if err != nil {
			t.Fatalf("launch: %s %v", out, err)
		}
		picker, _ := tmuxRunOut("display-message", "-p", "-t", "qs-test-root", "#{pane_id}")
		if picker == first || picker == second {
			t.Fatal("no modal picker")
		}
		if err := tmuxRun("send-keys", "-t", picker, "C-e"); err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(5 * time.Second)
		screen := ""
		for time.Now().Before(deadline) {
			screen, _ = tmuxRunOut("capture-pane", "-p", "-t", picker)
			if strings.Contains(screen, targetRow) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(screen, targetRow) {
			t.Fatalf("panes not loaded: %s", screen)
		}
		if err := tmuxRun("send-keys", "-t", picker, "Down", "Enter"); err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(5 * time.Second)
		active := ""
		for time.Now().Before(deadline) {
			active, _ = tmuxRunOut("display-message", "-p", "-t", second, "#{pane_active}")
			if active == "1" && !tmuxPaneExists(getTmuxServer(), picker) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if active != "1" || tmuxPaneExists(getTmuxServer(), picker) {
			t.Fatalf("zoom=%v selected pane not active: %q", zoom, active)
		}
	}
}
