//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestPTYStartupCachedSession(t *testing.T) {
	binary := buildTestBinary(t)
	for _, tc := range []struct {
		name       string
		session    string
		closed     bool
		lastWindow bool
		prefix     bool
	}{
		{name: "closed session", session: "closed", closed: true},
		{name: "last window closes server", session: "closed", closed: true, lastWindow: true},
		{name: "closed name matches live prefix", session: "closed", closed: true, prefix: true},
		{name: "detached session", session: "work"},
		{name: "detached eink session without base", session: "work-eink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTestTmuxServer(t)
			if err := tmuxRun("new-session", "-d", "-s", tc.session); err != nil {
				t.Fatal(err)
			}
			if tc.prefix {
				if err := tmuxRun("new-session", "-d", "-s", tc.session+"-extra"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.lastWindow {
				if err := tmuxRun("kill-session", "-t", "=qs-test-root"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(xdgCachePath(lastSessionFile)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(xdgCachePath(lastSessionFile), []byte(tc.session), 0644); err != nil {
				t.Fatal(err)
			}
			if tc.closed {
				if err := tmuxRun("kill-window", "-t", "="+tc.session+":"); err != nil {
					t.Fatal(err)
				}
			}

			// A plain invocation has no server flag. Route its tmux executable
			// to the private server while retaining the real startup path.
			tmuxBin, err := exec.LookPath("tmux")
			if err != nil {
				t.Fatal(err)
			}
			binDir := t.TempDir()
			wrapper := "#!/bin/sh\nexec " + shellQuote(tmuxBin) + " -L " + shellQuote(getTmuxServer().value) + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(wrapper), 0755); err != nil {
				t.Fatal(err)
			}
			env := append(isolatedPTYEnv(t),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+os.Getenv("TMPDIR"),
				"TMUX_TMPDIR="+os.Getenv("TMUX_TMPDIR"),
				"XDG_CACHE_HOME="+os.Getenv("XDG_CACHE_HOME"))
			proc := startPTY(t, binary, env, 30, 100)
			if tc.closed {
				waitForPTYOutput(t, proc, 10*time.Second, func(s string) bool {
					return containsVisible(s, "[Sessions]")
				})
				if err := tmuxRun("has-session", "-t", "="+tc.session); err == nil {
					t.Fatal("startup recreated the closed session")
				}
				writePTY(t, proc, "\x03")
			} else {
				want := fmt.Sprintf("%d\t%s", proc.cmd.Process.Pid, tc.session)
				deadline := time.Now().Add(10 * time.Second)
				for {
					clients, err := tmuxRunLines("list-clients", "-F", "#{client_pid}\t#{client_session}")
					if err == nil && slices.Contains(clients, want) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("startup did not attach to %q: clients=%q, err=%v\n%s", tc.session, clients, err, sanitizeTranscript(proc.output.String()))
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err := tmuxRun("detach-client", "-s", "="+tc.session); err != nil {
					t.Fatal(err)
				}
			}
			state, err := waitProcess(proc.cmd, 5*time.Second)
			if err != nil || state == nil || !state.Success() {
				t.Fatalf("startup process did not exit cleanly: state=%v, err=%v\n%s", state, err, sanitizeTranscript(proc.output.String()))
			}
		})
	}
}
