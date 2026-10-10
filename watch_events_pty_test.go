//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPTYWatchEventsQuitRemovesMonitor(t *testing.T) {
	withTestTmuxServer(t)
	if !getTmuxCapabilities(getTmuxServer()).EventMonitors {
		t.Skip("requires native monitors")
	}
	binary := buildTestBinary(t)
	home := t.TempDir()
	cfg := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfg, []byte("[waiting]\npoll_interval = '30s'\nevent_updates = true\npinned_only = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pane, err := tmuxRunOut("new-pane", "-t", "qs-test-root", "-x", "70", "-y", "20", "-P", "-F", "#{pane_id}", "-e", "HOME="+home, "-e", "XDG_CONFIG_HOME="+home, "-e", "XDG_CACHE_HOME="+home, "-e", "TMUX_QS_CONFIG="+cfg, binary, "--no-popup", "--server="+getTmuxServer().value)
	if err != nil {
		t.Fatalf("start real picker PTY %s: %v", pane, err)
	}
	if err := tmuxRun("set-option", "-p", "-t", pane, "@tmux_qs", "1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	var hooks, screen string
	for time.Now().Before(deadline) {
		hooks, _ = tmuxRunOut("show-hooks", "-g", "-B")
		screen, _ = tmuxRunOut("capture-pane", "-p", "-t", pane)
		if strings.Contains(hooks, "@tmux_qs_watch_") && strings.Contains(screen, "help") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(hooks, "@tmux_qs_watch_") {
		t.Fatal("binary did not install event monitor")
	}
	if err := tmuxRun("send-keys", "-t", pane, "Escape"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for tmuxPaneExists(getTmuxServer(), pane) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if tmuxPaneExists(getTmuxServer(), pane) {
		t.Fatal("Esc did not exit real picker")
	}
	hooks, _ = tmuxRunOut("show-hooks", "-g", "-B")
	if strings.Contains(hooks, "@tmux_qs_watch_") {
		t.Fatalf("picker left event monitor: %s", hooks)
	}
}
