package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigFilePath(t *testing.T) {
	// With no TMUX_QS_CONFIG and no files present, should return "".
	t.Setenv("TMUX_QS_CONFIG", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	got := configFilePath()
	if got != "" {
		t.Errorf("configFilePath with no files = %q, want \"\"", got)
	}

	// With TMUX_QS_CONFIG pointing at an existing file, should return that.
	cfgPath := filepath.Join(t.TempDir(), "myconfig.toml")
	if err := os.WriteFile(cfgPath, []byte("[waiting]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_QS_CONFIG", cfgPath)
	if got := configFilePath(); got != cfgPath {
		t.Errorf("configFilePath with TMUX_QS_CONFIG = %q, want %q", got, cfgPath)
	}

	// With TMUX_QS_CONFIG pointing at a non-existent file, should
	// fall through to the XDG path.
	t.Setenv("TMUX_QS_CONFIG", "/nonexistent/path/config.toml")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := configFilePath(); got != "" {
		t.Errorf("configFilePath with missing TMUX_QS_CONFIG = %q, want \"\"", got)
	}
}

func TestReloadConfigIfStale(t *testing.T) {
	// Use a temp file as the config. First, point the env at it.
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[waiting]\npoll_interval = \"3s\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_QS_CONFIG", cfgPath)
	resetConfigCache()

	// First call should load the file.
	cfg, mtime := reloadConfigIfStale()
	if mtime.IsZero() {
		t.Error("expected non-zero mtime on first call")
	}
	if cfg.Waiting.PollInterval != "3s" {
		t.Errorf("first load: PollInterval = %q, want \"3s\"", cfg.Waiting.PollInterval)
	}

	// Second call (no change) should return the same mtime and not
	// re-parse. (We can't easily check "not re-parsed", but we can
	// check that the returned mtime is the same.)
	cfg2, mtime2 := reloadConfigIfStale()
	if !mtime2.Equal(mtime) {
		t.Errorf("second call: mtime = %v, want %v", mtime2, mtime)
	}
	if cfg2.Waiting.PollInterval != "3s" {
		t.Errorf("second call: PollInterval = %q, want \"3s\"", cfg2.Waiting.PollInterval)
	}

	// Modify the file with a delay to ensure a different mtime.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(cfgPath, []byte("[waiting]\npoll_interval = \"7s\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg3, mtime3 := reloadConfigIfStale()
	if mtime3.Equal(mtime) {
		t.Errorf("after write: mtime unchanged (%v), expected different", mtime3)
	}
	if cfg3.Waiting.PollInterval != "7s" {
		t.Errorf("after write: PollInterval = %q, want \"7s\"", cfg3.Waiting.PollInterval)
	}
}

func TestReloadConfigIfSnippetFileChanges(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", t.TempDir())
	cfgPath := filepath.Join(xdg, "tmux-qs", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[waiting]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_QS_CONFIG", "")
	resetConfigCache()
	initial, _ := reloadConfigIfStale()
	if len(initial.Snippets) == 0 {
		t.Fatal("expected built-in snippets before adding snippet file")
	}

	snippetDir := filepath.Join(xdg, "tmux-qs", "snippets")
	if err := os.MkdirAll(snippetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(snippetDir, "project.toml")
	if err := os.WriteFile(path, []byte("[[snippet]]\nname = \"Project\"\ntext = \"first\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ := reloadConfigIfStale()
	if len(cfg.Snippets) != 1 || cfg.Snippets[0].Text != "first" {
		t.Fatalf("snippets after file addition = %#v", cfg.Snippets)
	}
	if err := os.WriteFile(path, []byte("[[snippet]]\nname = \"Project\"\ntext = \"updated\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ = reloadConfigIfStale()
	if len(cfg.Snippets) != 1 || cfg.Snippets[0].Text != "updated" {
		t.Fatalf("snippets after file edit = %#v", cfg.Snippets)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cfg, _ = reloadConfigIfStale()
	if len(cfg.Snippets) == 0 {
		t.Fatal("expected built-in snippets after removing final custom snippet file")
	}
	for _, snippet := range cfg.Snippets {
		if snippet.Name == "Project" {
			t.Fatalf("removed project snippet still loaded: %#v", cfg.Snippets)
		}
	}
}

func TestReloadConfigThreadSafe(t *testing.T) {
	// Sanity check: concurrent calls to reloadConfigIfStale should
	// not race. Run with -race to detect issues.
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[waiting]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_QS_CONFIG", cfgPath)
	resetConfigCache()

	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_, _ = reloadConfigIfStale()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
