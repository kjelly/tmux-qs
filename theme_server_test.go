package main

import "testing"

func TestLoadEinkWidthsUsesSelectedServer(t *testing.T) {
	withTestTmuxServer(t)
	if err := tmuxRun("set-option", "-g", "@eink-widths", "171,173"); err != nil {
		t.Fatal(err)
	}
	got := loadEinkWidths()
	if !isEinkWidth(171, got) || isEinkWidth(167, got) {
		t.Fatalf("loadEinkWidths() = %#v, want selected server option", got)
	}
}

func TestThemeSelectionIgnoresLegacySignals(t *testing.T) {
	t.Setenv("LC_IS_EINK", "1")
	t.Setenv("EINK_WIDTH", "220")
	for _, session := range []string{"work", "work-eink"} {
		if got := themeIsDarkForWidth(220, parseEinkWidths("167,165")); !got {
			t.Errorf("session %q / legacy env made width 220 light", session)
		}
	}
}

func TestCreateEinkSessionDoesNotSetThemeState(t *testing.T) {
	withTestTmuxServer(t)
	t.Setenv("TMUX", "private-test,1,0")
	if err := tmuxRun("set-option", "-g", "status-style", "fg=#112233,bg=#445566"); err != nil {
		t.Fatal(err)
	}

	_ = createEinkSession("qs-test-root")

	got, err := tmuxRunOut("show-options", "-t", "qs-test-root-eink", "-v", "status-style")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("grouped session status-style = %q, want no local theme override", got)
	}
	if value, err := tmuxRunOut("show-environment", "-t", "qs-test-root-eink", "LC_IS_EINK"); err == nil {
		t.Fatalf("grouped session unexpectedly sets %q", value)
	}
}
