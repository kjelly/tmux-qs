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

func TestApplyTmuxThemeInheritsClientTerminalColours(t *testing.T) {
	withTestTmuxServer(t)
	// tmux 3.8 defaults to a fixed dark popup. Explicit colours also
	// override OSC 10/11 when light and dark clients share a window.
	options := []string{"window-style", "window-active-style", "popup-style", "popup-border-style"}
	for _, option := range options {
		if err := tmuxRun("set-option", "-g", option, "fg=#dddddd,bg=#222222"); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyTmuxTheme(); err != nil {
		t.Fatal(err)
	}
	for _, option := range options {
		got, err := tmuxRunOut("show-options", "-gv", option)
		if err != nil || got != "default" {
			t.Errorf("%s = %q, %v; want default to inherit each client's terminal colours", option, got, err)
		}
	}
}

func TestEinkWidthsReadOnlyCommands(t *testing.T) {
	withTestTmuxServer(t)
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "configured", raw: "167, 165"},
		{name: "unset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.raw == "" {
				if err := tmuxRun("set-option", "-gu", "@eink-widths"); err != nil {
					t.Fatal(err)
				}
			} else if err := tmuxRun("set-option", "-g", "@eink-widths", tc.raw); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{nil, {"list"}} {
				var commandErr error
				out := captureStdout(t, func() {
					commandErr = runEinkWidthsCommand(args)
				})
				if commandErr != nil {
					t.Fatalf("eink %v without an attached client: %v", args, commandErr)
				}
				if out != "165,167\n" {
					t.Errorf("eink %v output=%q, want 165,167", args, out)
				}
				got, err := tmuxRunOut("show-options", "-gv", "@eink-widths")
				if tc.raw == "" {
					if err == nil {
						t.Fatalf("eink %v created @eink-widths=%q", args, got)
					}
				} else if err != nil || got != tc.raw {
					t.Fatalf("eink %v changed @eink-widths: got %q, %v; want %q", args, got, err, tc.raw)
				}
			}
		})
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
