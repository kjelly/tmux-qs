package main

import (
	"reflect"
	"testing"
)

func TestTmuxServerSpec_Args(t *testing.T) {
	cases := []struct {
		name string
		spec tmuxServerSpec
		want []string
	}{
		{"empty returns nil", tmuxServerSpec{}, nil},
		{"-L flag", tmuxServerSpec{flag: "-L", value: "foo"}, []string{"-L", "foo"}},
		{"-S flag with path", tmuxServerSpec{flag: "-S", value: "/tmp/tmux.sock"}, []string{"-S", "/tmp/tmux.sock"}},
		{"-L with empty value", tmuxServerSpec{flag: "-L", value: ""}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.spec.args()
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("args() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSetTmuxServerFromEnv_TMUXQSServer(t *testing.T) {
	clearTmuxServerForTest()
	t.Setenv("TMUX_QS_SERVER", "-L=work")
	setTmuxServerFromEnv()
	if got := tmuxServerForTest(); got.flag != "-L" || got.value != "work" {
		t.Errorf("got %+v, want -L work", got)
	}
}

func TestSetTmuxServerFromEnv_TMUXQSServerSocket(t *testing.T) {
	clearTmuxServerForTest()
	t.Setenv("TMUX_QS_SERVER", "-S=/tmp/foo.sock")
	setTmuxServerFromEnv()
	if got := tmuxServerForTest(); got.flag != "-S" || got.value != "/tmp/foo.sock" {
		t.Errorf("got %+v, want -S /tmp/foo.sock", got)
	}
}

func TestSetTmuxServerFromEnv_TMUXQSServerInvalidIgnored(t *testing.T) {
	clearTmuxServerForTest()
	// Bogus flag — should be ignored, not crash.
	t.Setenv("TMUX_QS_SERVER", "--bad=value")
	setTmuxServerFromEnv()
	if got := tmuxServerForTest(); got.flag != "" {
		t.Errorf("invalid flag should be ignored, got %+v", got)
	}
}

func TestSetTmuxServerFromEnv_TMUXInherited(t *testing.T) {
	clearTmuxServerForTest()
	// TMUX_QS_SERVER takes precedence over TMUX.
	t.Setenv("TMUX_QS_SERVER", "-L=qs")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0")
	setTmuxServerFromEnv()
	if got := tmuxServerForTest(); got.value != "qs" {
		t.Errorf("TMUX_QS_SERVER should take precedence, got value=%q", got.value)
	}

	// Now without TMUX_QS_SERVER — TMUX should be parsed.
	clearTmuxServerForTest()
	t.Setenv("TMUX_QS_SERVER", "")
	t.Setenv("TMUX", "/tmp/tmux-1000/work,1234,0")
	setTmuxServerFromEnv()
	if got := tmuxServerForTest(); got.flag != "-L" || got.value != "work" {
		t.Errorf("got %+v, want -L work", got)
	}
}

func TestSetTmuxServerFromEnv_EmptyTMUX(t *testing.T) {
	clearTmuxServerForTest()
	t.Setenv("TMUX_QS_SERVER", "")
	t.Setenv("TMUX", "")
	setTmuxServerFromEnv()
	if got := tmuxServerForTest(); got.flag != "" {
		t.Errorf("empty TMUX should leave server unset, got %+v", got)
	}
}

func TestSetTmuxServerFromEnv_TMUXWithoutSlashes(t *testing.T) {
	clearTmuxServerForTest()
	t.Setenv("TMUX_QS_SERVER", "")
	// Malformed TMUX (no slashes) — we still try to extract the
	// server segment by stripping at commas.
	t.Setenv("TMUX", "default,1234,0")
	setTmuxServerFromEnv()
	if got := tmuxServerForTest(); got.value != "default" {
		t.Errorf("got value=%q, want \"default\"", got.value)
	}
}

func TestTmuxArgs(t *testing.T) {
	clearTmuxServerForTest()
	if got := tmuxArgs(); got != nil {
		t.Errorf("tmuxArgs() with no spec = %v, want nil", got)
	}

	tmuxServer = tmuxServerSpec{flag: "-L", value: "test"}
	if got := tmuxArgs(); !reflect.DeepEqual(got, []string{"-L", "test"}) {
		t.Errorf("tmuxArgs() with -L = %v, want [-L test]", got)
	}
	clearTmuxServerForTest()
}

func TestSessionServer(t *testing.T) {
	cases := []struct {
		entry       string
		wantServer  string
		wantSession string
	}{
		{"[work] dev", "work", "dev"},
		{"[personal] blog", "personal", "blog"},
		{"[a] b", "a", "b"},
		{"plain-session", "", "plain-session"},
		{"", "", ""},
		{"[", "", "["},
		{"[no-space]rest", "", "[no-space]rest"}, // missing space after ]
	}
	for _, c := range cases {
		gotServer, gotSession := sessionServer(c.entry)
		if gotServer != c.wantServer || gotSession != c.wantSession {
			t.Errorf("sessionServer(%q) = (%q, %q), want (%q, %q)",
				c.entry, gotServer, gotSession, c.wantServer, c.wantSession)
		}
	}
}

func TestDefaultSocketDirs(t *testing.T) {
	// Should not panic, should return a non-nil slice.
	dirs, err := defaultSocketDirs()
	if err != nil {
		t.Fatalf("defaultSocketDirs() error: %v", err)
	}
	if dirs == nil {
		t.Error("defaultSocketDirs() returned nil, want at least /tmp/tmux-UID if it exists")
	}
}

func TestScanRunningTmuxServers(t *testing.T) {
	// Don't assert specific results (depends on the test machine's
	// tmux state). Just verify the function doesn't crash and
	// returns a non-nil slice.
	got := scanRunningTmuxServers()
	if got == nil {
		t.Error("scanRunningTmuxServers() returned nil, want empty slice")
	}
	// Each returned name should be a valid non-empty string.
	for i, name := range got {
		if name == "" {
			t.Errorf("scanRunningTmuxServers()[%d] is empty", i)
		}
	}
}
