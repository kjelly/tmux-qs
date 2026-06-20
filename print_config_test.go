package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrintResolvedConfigDefaults verifies that a merged Config
// (the form the TUI actually uses on first launch) serializes without
// error and contains the canonical agent list. If a refactor drops
// one of the defaults, --print-config will stop being a reliable
// "what does the picker actually do?" reference.
func TestPrintResolvedConfigDefaults(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "out.toml")
	orig := os.Stdout
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tmp: %v", err)
	}
	os.Stdout = f
	defer func() { os.Stdout = orig }()

	// --print-config goes through mergeConfig so it shows what the
	// TUI would actually see (Layout.Agent falls back to the first
	// Waiting.Commands entry, etc.).
	cfg := mergeConfig(Config{}, defaultConfig)
	if err := printResolvedConfig(cfg); err != nil {
		f.Close()
		t.Fatalf("printResolvedConfig: %v", err)
	}
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tmp: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"[waiting]",
		"[layout]",
		"[style]",
		"[resurrect]",
		"[naming]",
		"claude",
		"opencode",
		"pinned_only = true",
		"agent = 'claude'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, s)
		}
	}
}

// TestParseConfigBytesOK covers the happy path: a syntactically valid
// TOML with one custom field decodes into a Config and is merged with
// the defaults.
func TestParseConfigBytesOK(t *testing.T) {
	src := []byte("[waiting]\nidle_threshold = \"10s\"\n")
	cfg, err := parseConfigBytes(src)
	if err != nil {
		t.Fatalf("parseConfigBytes: %v", err)
	}
	if cfg.Waiting.IdleThreshold != "10s" {
		t.Errorf("IdleThreshold = %q, want %q", cfg.Waiting.IdleThreshold, "10s")
	}
	// Defaults should still be present.
	if len(cfg.Waiting.Commands) == 0 {
		t.Errorf("default agent commands missing after merge")
	}
}

// TestParseConfigBytesSyntaxError surfaces the underlying TOML parse
// error instead of swallowing it like readConfigFile does. This is
// what --print-config relies on for non-zero exit on bad input.
func TestParseConfigBytesSyntaxError(t *testing.T) {
	src := []byte("[waiting\n") // unterminated table
	_, err := parseConfigBytes(src)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

// TestLoadConfigForPrintNoFile returns the built-in defaults when no
// config file exists anywhere in the search path. This is the typical
// "fresh install" state.
func TestLoadConfigForPrintNoFile(t *testing.T) {
	t.Setenv("TMUX_QS_CONFIG", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := loadConfigForPrint()
	if err != nil {
		t.Fatalf("loadConfigForPrint: %v", err)
	}
	// Default agent list should be present.
	if len(cfg.Waiting.Commands) == 0 {
		t.Errorf("default Waiting.Commands empty; defaults not applied")
	}
}

// TestLoadConfigForPrintBadFile returns the underlying parse error
// rather than falling through to defaults. This is the CI use case:
// feed a freshly-edited config.toml, get a non-zero exit if the
// syntax is broken.
func TestLoadConfigForPrintBadFile(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(bad, []byte("[waiting\n"), 0o644); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	t.Setenv("TMUX_QS_CONFIG", bad)
	if _, err := loadConfigForPrint(); err == nil {
		t.Fatal("expected parse error from bad file")
	}
}
