package main

import "testing"

func TestTmuxHasDarkBackgroundUsesSelectedServer(t *testing.T) {
	withTestTmuxServer(t)
	t.Setenv("TMUX", "test-socket,1,0")

	if err := tmuxRun("set-option", "-g", "window-style", "fg=#111111,bg=#ffffff"); err != nil {
		t.Fatal(err)
	}
	if dark, ok := tmuxHasDarkBackground(); !ok || dark {
		t.Fatalf("light selected-server background = dark:%v ok:%v, want false,true", dark, ok)
	}

	if err := tmuxRun("set-option", "-g", "window-style", "fg=#eeeeee,bg=#171421"); err != nil {
		t.Fatal(err)
	}
	if dark, ok := tmuxHasDarkBackground(); !ok || !dark {
		t.Fatalf("dark selected-server background = dark:%v ok:%v, want true,true", dark, ok)
	}
}
