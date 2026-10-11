//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPTYWatchEventsQuitRemovesMonitor(t *testing.T) {
	withTestTmuxServer(t)
	requireTmux38Capability(t, getTmuxCapabilities(getTmuxServer()).EventMonitors, "event monitors")
	binary := buildTestBinary(t)
	for _, action := range []string{"Esc", "TERM", "HUP", "kill-pane", "kill-window", "kill-session", "TERM-then-HUP"} {
		t.Run(action, func(t *testing.T) {
			withTestTmuxServer(t)
			if action == "kill-window" || action == "kill-session" {
				// Keep the server alive so a leaked global hook remains observable.
				if err := tmuxRun("new-session", "-d", "-s", "sentinel", "/bin/sh"); err != nil {
					t.Fatal(err)
				}
			}
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
				if strings.Contains(hooks, "@tmux_qs_watch_") && strings.Contains(screen, "Sessions") {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !strings.Contains(hooks, "@tmux_qs_watch_") {
				t.Fatal("binary did not install event monitor")
			}
			pidText, err := tmuxRunOut("display-message", "-p", "-t", pane, "#{pane_pid}")
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(pidText)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "Esc":
				err = tmuxRun("send-keys", "-t", pane, "Escape")
			case "TERM":
				err = syscall.Kill(pid, syscall.SIGTERM)
			case "HUP":
				err = syscall.Kill(pid, syscall.SIGHUP)
			case "kill-pane":
				err = tmuxRun("kill-pane", "-t", pane)
			case "kill-window":
				err = tmuxRun("kill-window", "-t", pane)
			case "kill-session":
				err = tmuxRun("kill-session", "-t", "qs-test-root")
			case "TERM-then-HUP":
				err = syscall.Kill(pid, syscall.SIGTERM)
				// Exercise HUP arriving while normal shutdown is removing the hook.
				_ = syscall.Kill(pid, syscall.SIGHUP)
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline = time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				hooks, _ = tmuxRunOut("show-hooks", "-g", "-B")
				if !tmuxPaneExists(getTmuxServer(), pane) && !strings.Contains(hooks, "@tmux_qs_watch_") {
					waitProcessExit(t, pid)
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatalf("%s left picker or event monitor: %s", action, hooks)
		})
	}
}
