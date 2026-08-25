package main

import "testing"

func TestParseThemeClientUsesExactWidth(t *testing.T) {
	widths := parseEinkWidths("167,165")
	light, err := parseThemeClient("/dev/pts/8\t167\twork", widths)
	if err != nil || !light.light || light.width != 167 || light.session != "work" {
		t.Fatalf("light client = %#v, %v", light, err)
	}
	dark, err := parseThemeClient("/dev/pts/9\t166\twork", widths)
	if err != nil || dark.light {
		t.Fatalf("dark client = %#v, %v", dark, err)
	}
}

func TestRunThemeSubcommandRecognition(t *testing.T) {
	withTestTmuxServer(t)
	for _, tc := range []struct {
		args    []string
		handled bool
	}{
		{[]string{"theme", "apply"}, true},
		{[]string{"theme"}, true},
		{[]string{"eink"}, true},
		{[]string{"work"}, false},
		{nil, false},
	} {
		handled, _ := runThemeSubcommand(tc.args)
		if handled != tc.handled {
			t.Errorf("runThemeSubcommand(%v) handled=%v, want %v", tc.args, handled, tc.handled)
		}
	}
}

func TestApplyTmuxThemeInitializesDefaultWidths(t *testing.T) {
	withTestTmuxServer(t)
	_ = tmuxRun("set-option", "-gu", "@eink-widths")

	if err := applyTmuxTheme(); err != nil {
		t.Fatal(err)
	}
	got, err := tmuxRunOut("show-options", "-gv", "@eink-widths")
	if err != nil || got != defaultEinkWidths {
		t.Fatalf("@eink-widths=%q, %v; want %q", got, err, defaultEinkWidths)
	}
}

func TestEinkWidthsCommandManagesSelectedServer(t *testing.T) {
	withTestTmuxServer(t)
	if err := tmuxRun("set-option", "-g", "@eink-widths", "167,165"); err != nil {
		t.Fatal(err)
	}

	if err := runEinkWidthsCommand([]string{"set", "220, 165,220"}); err != nil {
		t.Fatal(err)
	}
	if err := runEinkWidthsCommand([]string{"add", "140"}); err != nil {
		t.Fatal(err)
	}
	if err := runEinkWidthsCommand([]string{"remove", "220"}); err != nil {
		t.Fatal(err)
	}
	got, err := tmuxRunOut("show-options", "-gv", "@eink-widths")
	if err != nil || got != "140,165" {
		t.Fatalf("@eink-widths=%q, %v; want 140,165", got, err)
	}

	if err := runEinkWidthsCommand([]string{"reset"}); err != nil {
		t.Fatal(err)
	}
	got, err = tmuxRunOut("show-options", "-gv", "@eink-widths")
	if err != nil || got != defaultEinkWidths {
		t.Fatalf("reset @eink-widths=%q, %v; want %q", got, err, defaultEinkWidths)
	}
}

func TestEinkWidthSettingRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"", "167,bad", "0", "-1", "167,,165"} {
		if _, err := parseEinkWidthSetting(raw); err == nil {
			t.Errorf("parseEinkWidthSetting(%q) accepted invalid value", raw)
		}
	}
}
