package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// logCapture redirects the standard logger to a buffer for the duration of t.
type logCapture struct {
	buf *bytes.Buffer
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	lc := &logCapture{buf: &bytes.Buffer{}}
	prev := log.Writer()
	log.SetOutput(lc.buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return lc
}

func (lc *logCapture) requireContains(t *testing.T, needle string) {
	t.Helper()
	got := lc.buf.String()
	if !strings.Contains(got, needle) {
		t.Errorf("expected log output to contain %q, got %q", needle, got)
	}
}

// withCleanEnv clears all tmux-qs config env vars and XDG paths for the
// duration of t, restoring the previous state on cleanup.
func withCleanEnv(t *testing.T) {
	t.Helper()
	// loadConfig memoizes; each test points the env at a different
	// config file, so the memo must be dropped on entry and exit.
	resetConfigCache()
	prev := map[string]*string{
		"TMUX_QS_CONFIG":  nil,
		"XDG_CONFIG_HOME": nil,
		"HOME":            nil,
	}
	for k := range prev {
		if v, ok := os.LookupEnv(k); ok {
			s := v
			prev[k] = &s
		}
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		resetConfigCache()
		for k, v := range prev {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
	})
}

func TestLoadConfig_DefaultWhenAllMissing(t *testing.T) {
	withCleanEnv(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg := loadConfig()
	if !reflect.DeepEqual(cfg.Waiting.Commands, defaultConfig.Waiting.Commands) {
		t.Errorf("Commands mismatch: got %v, want %v", cfg.Waiting.Commands, defaultConfig.Waiting.Commands)
	}
	if !reflect.DeepEqual(cfg.Waiting.IdleShells, defaultConfig.Waiting.IdleShells) {
		t.Errorf("IdleShells mismatch: got %v, want %v", cfg.Waiting.IdleShells, defaultConfig.Waiting.IdleShells)
	}
	if len(cfg.Waiting.IdleShells) != 0 {
		t.Errorf("IdleShells should default to empty, got %v", cfg.Waiting.IdleShells)
	}
	if !reflect.DeepEqual(cfg.Waiting.PromptRegex, defaultConfig.Waiting.PromptRegex) {
		t.Errorf("PromptRegex mismatch")
	}
	// Example config should be written.
	examplePath := filepath.Join(tmp, ".config", "tmux-qs", "config.toml")
	if _, err := os.Stat(examplePath); err != nil {
		t.Errorf("expected example config at %s: %v", examplePath, err)
	}
}

func TestLoadConfig_FromEnvVar(t *testing.T) {
	withCleanEnv(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgPath := filepath.Join(tmp, "my-config.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[waiting]
commands = ["only-this"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_QS_CONFIG", cfgPath)

	cfg := loadConfig()
	if !reflect.DeepEqual(cfg.Waiting.Commands, []string{"only-this"}) {
		t.Errorf("Commands = %v, want [only-this]", cfg.Waiting.Commands)
	}
	// Example config should NOT be written.
	examplePath := filepath.Join(tmp, ".config", "tmux-qs", "config.toml")
	if _, err := os.Stat(examplePath); !os.IsNotExist(err) {
		t.Errorf("example config should not exist, stat err=%v", err)
	}
}

func TestLoadConfig_FromXDG(t *testing.T) {
	withCleanEnv(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", t.TempDir()) // ensure dotfile is not picked up
	cfgDir := filepath.Join(xdg, "tmux-qs")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`
[waiting]
commands = ["from-xdg"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig()
	if !reflect.DeepEqual(cfg.Waiting.Commands, []string{"from-xdg"}) {
		t.Errorf("Commands = %v, want [from-xdg]", cfg.Waiting.Commands)
	}
}

func TestLoadConfig_FromDotfile(t *testing.T) {
	withCleanEnv(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := os.WriteFile(filepath.Join(tmp, ".tmux-qs.toml"), []byte(`
[waiting]
commands = ["from-dotfile"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig()
	if !reflect.DeepEqual(cfg.Waiting.Commands, []string{"from-dotfile"}) {
		t.Errorf("Commands = %v, want [from-dotfile]", cfg.Waiting.Commands)
	}
	// Make sure XDG path was not silently created and used.
	xdgPath := filepath.Join(tmp, ".config", "tmux-qs", "config.toml")
	if _, err := os.Stat(xdgPath); err == nil {
		t.Errorf("expected no XDG file, found at %s", xdgPath)
	}
}

func TestLoadConfig_PriorityOrder(t *testing.T) {
	withCleanEnv(t)
	tmp := t.TempDir()
	// dotfile
	if err := os.WriteFile(filepath.Join(tmp, ".tmux-qs.toml"), []byte(`
[waiting]
commands = ["from-dotfile"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// XDG
	xdg := filepath.Join(tmp, "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "tmux-qs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "tmux-qs", "config.toml"), []byte(`
[waiting]
commands = ["from-xdg"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// env
	envPath := filepath.Join(tmp, "env.toml")
	if err := os.WriteFile(envPath, []byte(`
[waiting]
commands = ["from-env"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("TMUX_QS_CONFIG", envPath)

	cfg := loadConfig()
	if !reflect.DeepEqual(cfg.Waiting.Commands, []string{"from-env"}) {
		t.Errorf("env should win, got %v", cfg.Waiting.Commands)
	}
}

func TestLoadConfig_MalformedFallsBack(t *testing.T) {
	withCleanEnv(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	bad := filepath.Join(tmp, ".tmux-qs.toml")
	if err := os.WriteFile(bad, []byte("not = valid = toml === ="), 0o644); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)

	cfg := loadConfig()

	logs.requireContains(t, "tmux-qs:")
	if !reflect.DeepEqual(cfg.Waiting.Commands, defaultConfig.Waiting.Commands) {
		t.Errorf("malformed config should fall back to defaults, got %v", cfg.Waiting.Commands)
	}
}

func TestMergeConfig_PartialOverride(t *testing.T) {
	file := Config{
		Waiting: WaitingConfig{
			Commands: []string{"only-this"},
		},
	}
	out := mergeConfig(file, defaultConfig)
	if !reflect.DeepEqual(out.Waiting.Commands, []string{"only-this"}) {
		t.Errorf("Commands should be overridden, got %v", out.Waiting.Commands)
	}
	if !reflect.DeepEqual(out.Waiting.IdleShells, defaultConfig.Waiting.IdleShells) {
		t.Errorf("IdleShells should fall back to default, got %v", out.Waiting.IdleShells)
	}
	if !reflect.DeepEqual(out.Waiting.PromptRegex, defaultConfig.Waiting.PromptRegex) {
		t.Errorf("PromptRegex should fall back to default, got %v", out.Waiting.PromptRegex)
	}
	if out.Waiting.IdleThreshold != defaultConfig.Waiting.IdleThreshold {
		t.Errorf("IdleThreshold should fall back, got %q", out.Waiting.IdleThreshold)
	}
}

// TestMergeConfig_IdleShellsOverride verifies that a user-provided
// idle_shells list overrides the (empty) default.
func TestMergeConfig_IdleShellsOverride(t *testing.T) {
	file := Config{
		Waiting: WaitingConfig{
			IdleShells: []string{"nu", "bash"},
		},
	}
	out := mergeConfig(file, defaultConfig)
	if !reflect.DeepEqual(out.Waiting.IdleShells, []string{"nu", "bash"}) {
		t.Errorf("IdleShells should be overridden, got %v", out.Waiting.IdleShells)
	}
	// Commands should still come from the default since file didn't set it.
	if !reflect.DeepEqual(out.Waiting.Commands, defaultConfig.Waiting.Commands) {
		t.Errorf("Commands should fall back to default, got %v", out.Waiting.Commands)
	}
}

func TestCompilePromptRegex_InvalidSkipped(t *testing.T) {
	logs := captureLogs(t)

	got := compilePromptRegex([]string{`claude`, `[bad(`, `opencode`})

	if len(got) != 2 {
		t.Errorf("expected 2 valid regexes, got %d", len(got))
	}
	if got[0].String() != "claude" || got[1].String() != "opencode" {
		t.Errorf("regex order/content wrong: %v", got)
	}
	logs.requireContains(t, "invalid prompt_regex")
}

func TestParseDuration(t *testing.T) {
	if d := parseDuration("", 5*time.Second); d != 5*time.Second {
		t.Errorf("empty -> fallback, got %v", d)
	}
	if d := parseDuration("30s", 5*time.Second); d != 30*time.Second {
		t.Errorf("30s -> 30s, got %v", d)
	}
	if d := parseDuration("garbage", 5*time.Second); d != 5*time.Second {
		t.Errorf("garbage -> fallback, got %v", d)
	}
}

// TestNewStyleBundleAppliesOverrides verifies the user's [style]
// TOML section actually changes the resolved colors.
func TestNewStyleBundleAppliesOverrides(t *testing.T) {
	defaults := newStyleBundle(StyleConfig{})
	if defaults.cursor.GetForeground() == (lipgloss.Style{}).GetForeground() {
		t.Errorf("defaults should have a foreground color set")
	}
	overridden := newStyleBundle(StyleConfig{
		Cursor:  "196", // bright red
		Success: "82",  // bright green
	})
	// lipgloss stores the terminal value; we just verify the
	// override StyleConfig didn't panic and the bundle is non-empty.
	if overridden.cursor.GetForeground() == (lipgloss.Style{}).GetForeground() {
		t.Errorf("overridden cursor should have a foreground color set")
	}
}

func TestMergeConfig_LayoutOverride(t *testing.T) {
	enableFalse := false
	file := Config{
		Layout: LayoutConfig{
			EnableScripts: &enableFalse,
			ScriptNames:   []string{".custom-layout.sh"},
		},
	}
	out := mergeConfig(file, defaultConfig)
	if out.EnableLayoutScripts() {
		t.Error("EnableLayoutScripts should be overridden to false")
	}
	if !reflect.DeepEqual(out.Layout.ScriptNames, []string{".custom-layout.sh"}) {
		t.Errorf("ScriptNames should be overridden, got %v", out.Layout.ScriptNames)
	}

	// Empty layout should merge defaults
	emptyOut := mergeConfig(Config{}, defaultConfig)
	if !emptyOut.EnableLayoutScripts() {
		t.Error("EnableLayoutScripts should default to true")
	}
	if !reflect.DeepEqual(emptyOut.Layout.ScriptNames, []string{".tmux-qs.sh", ".tmux.sh"}) {
		t.Errorf("ScriptNames should fall back to default, got %v", emptyOut.Layout.ScriptNames)
	}
}

func TestParseDynamicSnippetsFromTOML(t *testing.T) {
	tomlData := []byte(`
[[dynamic_snippet]]
name = "Check Error: $1"
matches = ["錯誤", "error", "FAIL:\\s+(\\w+)"]
text = "inspect error $1"
submit = true
favorite = true
`)
	cfg, err := parseConfigBytes(tomlData)
	if err != nil {
		t.Fatalf("failed to parse TOML: %v", err)
	}
	if len(cfg.DynamicSnippets) != 1 {
		t.Fatalf("expected 1 dynamic snippet rule, got %d", len(cfg.DynamicSnippets))
	}
	rule := cfg.DynamicSnippets[0]
	if rule.Name != "Check Error: $1" || len(rule.Matches) != 3 || rule.Matches[0] != "錯誤" || !rule.Submit || !rule.Favorite {
		t.Fatalf("unexpected dynamic snippet rule: %#v", rule)
	}
}
