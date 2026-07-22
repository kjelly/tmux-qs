package main

import (
	"reflect"
	"testing"
)

// Expected values follow fzf's parseTmuxOptions + runTmux mapping
// (~/github/fzf/src/options.go, src/tmux.go).
func TestPopupArgs(t *testing.T) {
	cases := []struct {
		spec string
		want []string
	}{
		{"top,70%", []string{"-xC", "-y0", "-w100%", "-h70%"}},
		{"bottom,40%", []string{"-xC", "-y9999", "-w100%", "-h40%"}},
		{"left,30%", []string{"-x0", "-yC", "-w30%", "-h100%"}},
		{"right,30%", []string{"-xR", "-yC", "-w30%", "-h100%"}},
		{"center,80%,60%", []string{"-xC", "-yC", "-w80%", "-h60%"}},
		{"top,70%,90%", []string{"-xC", "-y0", "-w70%", "-h90%"}}, // two sizes = WIDTH,HEIGHT
		{"80%", []string{"-xC", "-yC", "-w80%", "-h80%"}},
		{"", []string{"-xC", "-yC", "-w50%", "-h50%"}},
		{"up,70%", []string{"-xC", "-y0", "-w100%", "-h70%"}},
		{"top,70%,border-native", []string{"-xC", "-y0", "-w100%", "-h70%"}},
		{"center,border-native", []string{"-xC", "-yC", "-w50%", "-h50%"}},
		{"border-native", []string{"-xC", "-yC", "-w50%", "-h50%"}},
	}
	for _, c := range cases {
		if got := popupArgs(c.spec); !reflect.DeepEqual(got, c.want) {
			t.Errorf("popupArgs(%q) = %v, want %v", c.spec, got, c.want)
		}
	}
}

func TestParsePopupContext(t *testing.T) {
	got := parsePopupContext("client-1\t%42\t/home/me/a project")
	want := popupContext{client: "client-1", pane: "%42", cwd: "/home/me/a project"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePopupContext() = %+v, want %+v", got, want)
	}
	if got := parsePopupContext("incomplete"); got != (popupContext{}) {
		t.Fatalf("malformed context = %+v, want empty", got)
	}
}

func TestPopupCommandArgsCarriesCallerContext(t *testing.T) {
	previousServer := getTmuxServer()
	previousVim := vimEnabled
	t.Cleanup(func() {
		setTmuxServer(previousServer)
		vimEnabled = previousVim
	})
	setTmuxServer(tmuxServerSpec{flag: "-S", value: "/tmp/custom.sock"})
	vimEnabled = true

	ctx := popupContext{client: "client-1", pane: "%42", cwd: "/work/a project"}
	got := popupCommandArgs(ctx, "center,80%,60%,border-native", "/bin/tmux-qs", true)
	want := []string{
		"display-popup", "-E", "-e", "TMUX_QS_POPUP=1",
		"-c", "client-1", "-e", "TMUX_QS_CLIENT=client-1",
		"-t", "%42", "-e", "TMUX_QS_CALLER_PANE=%42",
		"-d", "/work/a project", "-e", "TMUX_QS_CALLER_CWD=/work/a project",
		"-e", "TMUX_QS_SERVER=-S=/tmp/custom.sock",
		"-T", " tmux-qs ", "-xC", "-yC", "-w80%", "-h60%",
		"/bin/tmux-qs", "--no-popup", "--vim", "--snippets",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("popupCommandArgs() =\n%q\nwant\n%q", got, want)
	}
}

func TestCurrentPopupClientPrefersChildEnvironment(t *testing.T) {
	t.Setenv(popupClientEnv, "client-from-parent")
	if got := currentPopupClient(); got != "client-from-parent" {
		t.Fatalf("currentPopupClient() = %q, want client-from-parent", got)
	}
}

func TestCurrentPopupContextPrefersBindingEnvironment(t *testing.T) {
	t.Setenv(popupClientEnv, "client-from-binding")
	t.Setenv(popupPaneEnv, "%99")
	t.Setenv(popupCwdEnv, "/work/a project")
	want := popupContext{client: "client-from-binding", pane: "%99", cwd: "/work/a project"}
	if got := currentPopupContext(); got != want {
		t.Fatalf("currentPopupContext() = %+v, want %+v", got, want)
	}
}
