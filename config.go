package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Waiting   WaitingConfig       `toml:"waiting"`
	Style     StyleConfig         `toml:"style"`
	Layout    LayoutConfig        `toml:"layout"`
	Sessions  []SessionEntry      `toml:"session"`
	Templates []TemplateConfig    `toml:"template"`
	Commands  []UserCommandConfig `toml:"command"`
}

type UserCommandConfig struct {
	Name string `toml:"name"`
	Cmd  string `toml:"cmd"`
}

type TemplateConfig struct {
	Name        string         `toml:"name"`
	DetectFiles []string       `toml:"detect_files"`
	Commands    []string       `toml:"commands"`
	Windows     []TemplateWindow `toml:"windows"`
}

type TemplateWindow struct {
	Name    string `toml:"name"`
	Command string `toml:"command"`
	Split   string `toml:"split"` // "vertical", "horizontal", or "" for first window
}

// SessionEntry is one user-defined session in the [[session]] config
// table. Used by loadConfig() to feed srcConfigs without depending on
// an external tool. Tags are optional labels for filtering in the TUI.
type SessionEntry struct {
	Name string   `toml:"name"`
	Path string   `toml:"path"`
	Tags []string `toml:"tags"`
}

// ExpandedPath returns the session's path with leading "~" replaced
// by the user's home directory. Returns "" if the home directory
// lookup fails.
func (s SessionEntry) ExpandedPath() string {
	return expandPath(s.Path)
}

type WaitingConfig struct {
	Commands      []string `toml:"commands"`
	IdleShells    []string `toml:"idle_shells"`
	PromptRegex   []string `toml:"prompt_regex"`
	IdleThreshold string   `toml:"idle_threshold"`
	PollInterval  string   `toml:"poll_interval"`
}

// LayoutConfig defines settings for workspace layout script execution.
type LayoutConfig struct {
	EnableScripts *bool    `toml:"enable_scripts"`
	ScriptNames   []string `toml:"script_names"`
	PaneShells    []string `toml:"pane_shells"`
	Agent         string   `toml:"agent"`
}

// StyleConfig lets the user override the lipgloss colors used by the
// TUI. Field names match the TOML keys; values are ANSI 16-color
// numbers ("212", "214", "36", ...). Empty values fall back to the
// built-in defaults.
type StyleConfig struct {
	Cursor   string `toml:"cursor"`
	Selected string `toml:"selected"`
	Branch   string `toml:"branch"`
	Dim      string `toml:"dim"`
	Error    string `toml:"error"`
	Warn     string `toml:"warn"`
	Success  string `toml:"success"`
}

var defaultEnableScripts = true

var defaultConfig = Config{
	Waiting: WaitingConfig{
		Commands: []string{
			"claude", "opencode", "aider", "ollama", "codex",
			"cursor", "cody", "continue", "gpt", "copilot", "crush",
		},
		IdleShells: []string{},
		PromptRegex: []string{
			`continue\?`, "yes.*no", `\[Y/n\]|\[y/N\]`, `human:|assistant:`,
		},
		IdleThreshold: "30s",
		PollInterval:  "5s",
	},
	Layout: LayoutConfig{
		EnableScripts: &defaultEnableScripts,
		ScriptNames:   []string{".tmux-qs.sh", ".tmux.sh"},
		PaneShells:    []string{"nu", "nvim", "fish", "bash", "zsh"},
	},
}

// EnableLayoutScripts returns whether layout script execution is enabled.
func (c Config) EnableLayoutScripts() bool {
	if c.Layout.EnableScripts == nil {
		return true
	}
	return *c.Layout.EnableScripts
}

// loadConfig returns the resolved config, parsing it from disk only on
// the first call — the file cannot change mid-session in any way the
// running TUI would react to, and several hot paths (connect, sources)
// call this repeatedly. Tests reset the memo via resetConfigCache.
func loadConfig() Config {
	configMu.Lock()
	defer configMu.Unlock()
	if cachedConfig == nil {
		c := loadConfigFromDisk()
		cachedConfig = &c
	}
	return *cachedConfig
}

var (
	configMu     sync.Mutex
	cachedConfig *Config
)

// resetConfigCache clears the loadConfig memo. Only tests need this —
// they point env vars at different config files between calls.
func resetConfigCache() {
	configMu.Lock()
	defer configMu.Unlock()
	cachedConfig = nil
}

func loadConfigFromDisk() Config {
	if path := os.Getenv("TMUX_QS_CONFIG"); path != "" {
		if c, ok := readConfigFile(path); ok {
			return c
		}
	}
	if c, ok := readConfigFile(xdgConfigPath("config.toml")); ok {
		return c
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if c, ok := readConfigFile(filepath.Join(home, ".tmux-qs.toml")); ok {
			return c
		}
		if err := writeExampleConfig(xdgConfigPath("config.toml")); err != nil {
			log.Printf("tmux-qs: cannot write example config: %v", err)
		}
	} else {
		log.Printf("tmux-qs: cannot determine home directory: %v", err)
	}
	return defaultConfig
}

func readConfigFile(path string) (Config, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, false
	}
	var file Config
	if err := toml.Unmarshal(data, &file); err != nil {
		log.Printf("tmux-qs: cannot parse %s: %v (using defaults)", path, err)
		return Config{}, false
	}
	return mergeConfig(file, defaultConfig), true
}

// mergeConfig fills in any missing/empty fields in file with values from def.
// A field is considered "missing" when it is a nil slice, a zero-length slice,
// or (for string fields) an empty string.
func mergeConfig(file, def Config) Config {
	out := file
	if len(out.Waiting.Commands) == 0 {
		out.Waiting.Commands = def.Waiting.Commands
	}
	if len(out.Waiting.IdleShells) == 0 {
		out.Waiting.IdleShells = def.Waiting.IdleShells
	}
	if len(out.Waiting.PromptRegex) == 0 {
		out.Waiting.PromptRegex = def.Waiting.PromptRegex
	}
	if out.Waiting.IdleThreshold == "" {
		out.Waiting.IdleThreshold = def.Waiting.IdleThreshold
	}
	if out.Waiting.PollInterval == "" {
		out.Waiting.PollInterval = def.Waiting.PollInterval
	}
	if out.Layout.EnableScripts == nil {
		out.Layout.EnableScripts = def.Layout.EnableScripts
	}
	if len(out.Layout.ScriptNames) == 0 {
		out.Layout.ScriptNames = def.Layout.ScriptNames
	}
	if len(out.Layout.PaneShells) == 0 {
		out.Layout.PaneShells = def.Layout.PaneShells
	}
	if out.Layout.Agent == "" {
		if len(out.Waiting.Commands) > 0 {
			out.Layout.Agent = out.Waiting.Commands[0]
		} else {
			out.Layout.Agent = "claude"
		}
	}
	if len(out.Templates) == 0 {
		out.Templates = def.Templates
	}
	// Sessions is a user-defined list; no built-in defaults to fill
	// in. Leave it as-is (potentially nil) so srcConfigs can
	// distinguish "no entries" from "entries but empty list".
	return out
}

const exampleConfig = `# tmux-qs configuration
# This file is auto-generated the first time tmux-qs runs and cannot find
# any existing config. Delete it to regenerate the defaults; manual edits
# are preserved on subsequent runs.
# Any field omitted below falls back to the built-in default.

[waiting]
commands = [
  "claude",
  "opencode",
  "aider",
  "ollama",
  "codex",
  "cursor",
  "cody",
  "continue",
  "gpt",
  "copilot",
  "crush",
]

# Add interactive shells/REPLs here if you want them flagged as waiting
# when their tty has been idle for idle_threshold. Leave empty (default)
# to keep detection scoped to AI agents only.
idle_shells = []

prompt_regex = [
  "continue\\?",
  "yes.*no",
  "\\[Y/n\\]|\\[y/N\\]",
  "human:|assistant:",
]

idle_threshold = "30s"
poll_interval  = "5s"

# Optional: override TUI colors. Values are ANSI 16-color numbers.
# Leave a field empty to use the built-in default.
[style]
# cursor   = "212"  # selected row marker
# selected = "212"  # selected entry text
# branch   = "36"   # git branch annotation
# dim      = ""     # headers / status line (no color, just faint)
# error    = "203"  # error messages
# warn     = "214"  # waiting warnings
# success  = "42"   # copy confirmations

# Optional: run layout initialization scripts on new sessions
[layout]
# enable_scripts = true
# script_names = [".tmux-qs.sh", ".tmux.sh"]

# Foreground commands that count as "an interactive shell/editor
# already in this directory" when picking a path entry. tmux-qs
# prefers to select an existing matching pane in the connected
# session over opening a new window. Override this list to match
# your shell of choice.
# pane_shells = ["nu", "nvim", "fish", "bash", "zsh"]

# Optional: user-defined named sessions, surfaced by Ctrl-g. Each
# entry has a name (display label) and a path (directory the
# session is rooted at; "~" is expanded). Entries whose target
# directory doesn't exist are silently skipped.
#
# [[session]]
# name = "docs"
# path = "~/projects/docs"
#
# [[session]]
# name = "scratch"
# path = "/tmp/scratch"

# Optional: templates to auto-split windows or run commands on session creation.
# {session} and {path} placeholders are replaced by the session name and path.
# [[template]]
# name = "node-template"
# detect_files = ["package.json"]
# commands = [
#   "tmux split-window -h -c {path} -t {session}",
#   "tmux send-keys -t {session}:0.1 'npm run dev' Enter"
# ]

# Optional: user-defined commands for the Command Palette (Ctrl-o).
# {session} and {path} placeholders are replaced by the current attached session name and path.
# [[command]]
# name = "Git: Pull current session"
# cmd = "tmux send-keys -t {session} 'git pull' Enter"
`

func writeExampleConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(exampleConfig), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// compilePromptRegex compiles the regex strings, skipping any that fail to
// compile (with a warning to stderr). The returned slice preserves input
// order but may be shorter than the input.
func compilePromptRegex(src []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(src))
	for _, s := range src {
		re, err := regexp.Compile(s)
		if err != nil {
			log.Printf("tmux-qs: invalid prompt_regex %q: %v", s, err)
			continue
		}
		out = append(out, re)
	}
	return out
}

// parseDuration parses a Go duration string ("30s", "5m", etc.) and returns
// fallback when the string is empty or unparsable.
func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Printf("tmux-qs: invalid duration %q: %v (using %s)", s, err, fallback)
		return fallback
	}
	return d
}
