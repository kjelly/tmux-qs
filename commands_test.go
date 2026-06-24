package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStateFilePath(t *testing.T) {
	got := stateFilePath()
	// It should be under $HOME/.local/state/tmux-qs/resurrect.json
	// and the parent directory should exist (stateFilePath calls
	// MkdirAll).
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "state", "tmux-qs", "resurrect.json")
	if got != want {
		t.Errorf("stateFilePath() = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Dir(got)); err != nil {
		t.Errorf("stateFilePath parent dir not created: %v", err)
	}
}

func TestExecuteCommand_UnknownIsNoOp(t *testing.T) {
	// Unknown command names should be a no-op (not an error).
	if err := executeCommand("Definitely Not A Real Command Name"); err != nil {
		t.Errorf("executeCommand on unknown = %v, want nil", err)
	}
}

func TestExecuteCommand_BuiltinTmuxCommands(t *testing.T) {
	// "Detach Client" runs `tmux detach-client`. Against the user's real
	// server (i.e. when `go test` runs inside their tmux) that would
	// detach them. Pin the command to a private throwaway server so it
	// only ever detaches that server's (nonexistent) client.
	//
	// "Tmux: Kill Server (Danger)" is still skipped entirely — even on a
	// private server there's no value in exercising it.
	withTestTmuxServer(t)
	_ = executeCommand("Tmux: Detach Client") // any error is fine
}

func TestExecuteCommand_UserDefined(t *testing.T) {
	// Save and restore the original config.
	orig := loadConfig()
	defer func() {
		configMu.Lock()
		cachedConfig = &cachedConfigEntry{cfg: orig, path: configFilePath()}
		configMu.Unlock()
	}()

	// Install a test config with a user-defined command.
	configMu.Lock()
	cachedConfig = &cachedConfigEntry{
		cfg: Config{
			Commands: []UserCommandConfig{
				{Name: "Echo Test", Cmd: "echo hello"},
			},
		},
	}
	configMu.Unlock()

	// Should run `echo hello` via sh -c.
	if err := executeCommand("Echo Test"); err != nil {
		t.Errorf("executeCommand(Echo Test) = %v, want nil", err)
	}
}

func TestExecuteCommand_UserDefinedWithPlaceholders(t *testing.T) {
	orig := loadConfig()
	defer func() {
		configMu.Lock()
		cachedConfig = &cachedConfigEntry{cfg: orig, path: configFilePath()}
		configMu.Unlock()
	}()

	// The {session} and {path} placeholders are replaced from
	// currentSessionName() and currentSessionPath() — both of which
	// call tmux and may fail in the test env. Use a command that
	// doesn't actually depend on them.
	configMu.Lock()
	cachedConfig = &cachedConfigEntry{
		cfg: Config{
			Commands: []UserCommandConfig{
				{Name: "P", Cmd: "echo {path}"},
			},
		},
	}
	configMu.Unlock()
	if err := executeCommand("P"); err != nil {
		t.Errorf("executeCommand(P) = %v, want nil", err)
	}
}

func TestResurrectStateJSON(t *testing.T) {
	// Verify the rich on-disk format round-trips through JSON.
	state := ResurrectState{Sessions: []ResurrectSession{
		{
			Name: "work",
			Path: "/home/u/work",
			Windows: []ResurrectWindow{
				{Index: 0, Name: "edit", Layout: "abcd,80x24,0,0,0", Panes: []ResurrectPane{
					{Path: "/home/u/work", Command: "nvim"},
					{Path: "/home/u/work/sub", Command: "bash"},
				}},
			},
		},
	}}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var got ResurrectState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Sessions, state.Sessions) {
		t.Errorf("round-trip = %v, want %v", got.Sessions, state.Sessions)
	}
}

// TestBuildResurrectSessions verifies pane lines group into ordered
// sessions/windows/panes (the core of both save and kill-undo capture).
func TestBuildResurrectSessions(t *testing.T) {
	lines := []string{
		"work\t/w\t0\tedit\tlay0\t0\t/w\tnvim",
		"work\t/w\t0\tedit\tlay0\t1\t/w/sub\tbash",
		"work\t/w\t1\tshell\tlay1\t0\t/w\tzsh",
		"play\t/p\t0\tmain\tlayp\t0\t/p\thtop",
	}
	got := buildResurrectSessions(lines)
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got))
	}
	work := got[0]
	if work.Name != "work" || len(work.Windows) != 2 {
		t.Fatalf("work session wrong: %+v", work)
	}
	if len(work.Windows[0].Panes) != 2 || work.Windows[0].Panes[0].Command != "nvim" {
		t.Errorf("window 0 panes wrong: %+v", work.Windows[0])
	}
	if work.Windows[0].Layout != "lay0" {
		t.Errorf("layout not captured: %q", work.Windows[0].Layout)
	}
	if got[1].Name != "play" || got[1].Windows[0].Panes[0].Command != "htop" {
		t.Errorf("play session wrong: %+v", got[1])
	}
}

// TestLoadResurrectLegacy verifies the old map[name]path schema still
// loads (best-effort migration).
func TestLoadResurrectLegacy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	legacy := `{"sessions":{"work":"/home/u/work"}}`
	if err := os.WriteFile(stateFilePath(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := loadResurrectState()
	if err != nil {
		t.Fatalf("loadResurrectState: %v", err)
	}
	if len(state.Sessions) != 1 || state.Sessions[0].Name != "work" || state.Sessions[0].Path != "/home/u/work" {
		t.Errorf("legacy migration failed: %+v", state.Sessions)
	}
}
