package main

import (
	"os/exec"
	"testing"
)

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
