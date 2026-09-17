package main

import (
	"os/exec"
	"testing"
)

func TestShouldAutoAttachLastSession(t *testing.T) {
	tests := []struct {
		name       string
		tmuxEnv    string
		tmuxUsable bool
		args       []string
		want       bool
	}{
		{name: "plain invocation outside tmux", want: true},
		{name: "inside tmux", tmuxEnv: "/tmp/tmux-1000/default,123,0"},
		{name: "stale TMUX environment", tmuxEnv: "/tmp/stale,123,0"},
		{name: "usable tmux without environment", tmuxUsable: true},
		{name: "explicit option", args: []string{"--no-popup"}},
		{name: "explicit target", args: []string{"work"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAutoAttachLastSession(tt.tmuxEnv, tt.tmuxUsable, tt.args); got != tt.want {
				t.Errorf("shouldAutoAttachLastSession() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestTMUXEnvironmentUsableRejectsStaleSocket(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-qs-does-not-exist,12345,0")

	if tmuxEnvironmentUsable() {
		t.Fatal("stale TMUX socket should not be treated as a usable tmux client")
	}
}

func TestPopupLaunchErrorIsRecoverable(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 1").Run()
	if !popupLaunchErrorIsRecoverable(err) {
		t.Fatal("popup command failure should fall back to the inline picker")
	}
}
