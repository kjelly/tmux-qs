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
	Waiting         WaitingConfig          `toml:"waiting"`
	Style           StyleConfig            `toml:"style"`
	Layout          LayoutConfig           `toml:"layout"`
	Resurrect       ResurrectConfig        `toml:"resurrect"`
	Naming          NamingConfig           `toml:"naming"`
	Sessions        []SessionEntry         `toml:"session"`
	Templates       []TemplateConfig       `toml:"template"`
	Commands        []UserCommandConfig    `toml:"command"`
	Snippets        []SnippetConfig        `toml:"snippet"`
	DynamicSnippets []DynamicSnippetConfig `toml:"dynamic_snippet"`
	// Keybindings maps an action name (see defaultActionKeys) to a key,
	// rebinding that action. Unknown actions are ignored with a warning.
	Keybindings map[string]string `toml:"keybindings"`
}

type DynamicSnippetConfig struct {
	Name     string   `toml:"name"`
	Matches  []string `toml:"matches"`
	Text     string   `toml:"text"`
	Commands []string `toml:"commands"`
	Submit   bool     `toml:"submit"`
	Favorite bool     `toml:"favorite"`
	Disabled bool     `toml:"disabled"`
}

// ResurrectConfig controls workspace save/restore (Command Palette
// "Resurrect: …"). RestorePrograms is the allowlist of foreground
// programs that may be re-launched on restore — anything not listed is
// left as a bare shell, so restore never re-runs an arbitrary (possibly
// destructive) command that happened to be running at save time.
// AutoSaveInterval, when set to a non-zero Go duration, makes the TUI
// snapshot the workspace state on that interval while it is open.
type ResurrectConfig struct {
	RestorePrograms  []string `toml:"restore_programs"`
	AutoSaveInterval string   `toml:"auto_save_interval"`
}

type UserCommandConfig struct {
	Name string `toml:"name"`
	Cmd  string `toml:"cmd"`
}

// SnippetConfig maps one reusable input to the foreground commands that can
// receive it. An empty Commands list makes the snippet available for every
// pane. Exactly one of Text or Keys should be set: Text is inserted literally,
// while Keys uses tmux key notation (for example "C-c" or "M-j"). Submit
// controls whether tmux-qs appends an Enter key after the chosen input.
type SnippetConfig struct {
	Name     string   `toml:"name"`
	Commands []string `toml:"commands"`
	Text     string   `toml:"text"`
	Keys     []string `toml:"keys"`
	Submit   bool     `toml:"submit"`
	// Favorite snippets are placed first in the picker. This keeps the
	// gamepad path usable without requiring text filtering.
	Favorite bool `toml:"favorite"`
}

type TemplateConfig struct {
	Name        string           `toml:"name"`
	DetectFiles []string         `toml:"detect_files"`
	Commands    []string         `toml:"commands"`
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
// Group is an optional label that groups related sessions under a
// shared header in the list view (e.g. "work", "personal"). Sessions
// with the same group appear contiguously; sessions with no group
// appear at the top in source order.
type SessionEntry struct {
	Name  string   `toml:"name"`
	Path  string   `toml:"path"`
	Tags  []string `toml:"tags"`
	Group string   `toml:"group"`
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
	// FloatToTop, when true, bubbles sessions that currently have a
	// waiting agent to the top of the default/all list (just under
	// pinned entries) so they're easy to jump back to.
	FloatToTop bool `toml:"float_to_top"`
	// PinnedOnly, when true (default), restricts waiting detection
	// to panes whose session is in the user's pinned list. Pin
	// (Alt-I) is the way the user signals "I care about this
	// workspace enough to monitor its agents" — the rest of the
	// tmux server is ignored to keep the waiting indicator scoped
	// to the user's working set. Set to false to restore the old
	// "watch every session" behavior.
	PinnedOnly *bool `toml:"pinned_only"`
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
	// Highlight is the color used to mark fuzzy-matched characters in
	// the entry list. When empty, falls back to Warn (legacy behavior
	// preserved for backward compatibility), and finally to the
	// built-in AdaptiveColor default (dark gray on light backgrounds,
	// bright yellow on dark).
	Highlight string `toml:"highlight"`
}

var defaultEnableScripts = true

// defaultPinnedOnly is the out-of-the-box default for the [waiting]
// pinned_only knob. True means "only monitor agents in pinned
// sessions" — see WaitingConfig.PinnedOnly for the rationale.
var defaultPinnedOnly = true

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
		FloatToTop:    true,
		PinnedOnly:    &defaultPinnedOnly,
	},
	Layout: LayoutConfig{
		EnableScripts: &defaultEnableScripts,
		ScriptNames:   []string{".tmux-qs.sh", ".tmux.sh"},
		PaneShells:    []string{"nu", "nvim", "fish", "bash", "zsh"},
	},
	Resurrect: ResurrectConfig{
		RestorePrograms: []string{
			"nvim", "vim", "vi", "emacs", "nano", "hx", "helix",
			"less", "tail", "watch", "htop", "btop", "top",
			"lazygit", "gitui", "k9s", "ssh",
		},
	},
	Snippets: defaultSnippets(),
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
		cachedConfig = &cachedConfigEntry{cfg: c, path: configFilePath()}
	}
	return cachedConfig.cfg
}

type cachedConfigEntry struct {
	cfg   Config
	mtime time.Time
	path  string
}

var (
	configMu     sync.Mutex
	cachedConfig *cachedConfigEntry
)

// resetConfigCache clears the loadConfig memo. Only tests need this —
// they point env vars at different config files between calls.
func resetConfigCache() {
	configMu.Lock()
	defer configMu.Unlock()
	cachedConfig = nil
}

// reloadConfigIfStale checks whether the file backing cachedConfig has
// been modified since it was last loaded. If so, parses the new content
// and replaces the cache atomically. The returned values are the
// resolved config (possibly unchanged) and the file mtime the cache
// is now keyed on. Returns an empty time.Time when the file isn't
// tracked (e.g. defaults were used).
//
// Safe to call from a tea.Cmd goroutine: takes the package configMu
// and never mutates *cachedConfig in place (replaces the pointer).
func reloadConfigIfStale() (Config, time.Time) {
	configMu.Lock()
	path := configFilePath()
	var mtime time.Time
	if path != "" {
		if st, err := os.Stat(path); err == nil {
			mtime = st.ModTime()
		}
	}
	if cachedConfig != nil && path == cachedConfig.path && mtime.Equal(cachedConfig.mtime) {
		// No change — return the cached value.
		c := cachedConfig.cfg
		configMu.Unlock()
		return c, mtime
	}
	// Cache is stale or absent — reload from disk.
	c := loadConfigFromDisk()
	cachedConfig = &cachedConfigEntry{cfg: c, mtime: mtime, path: path}
	configMu.Unlock()
	return c, mtime
}

// configFilePath returns the path to the config file that
// loadConfigFromDisk would read, or "" if no file is tracked
// (e.g. defaults path with no file present). Computed in the same
// order as loadConfigFromDisk so the two stay in sync.
func configFilePath() string {
	if path := os.Getenv("TMUX_QS_CONFIG"); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	xdg := xdgConfigPath("config.toml")
	if _, err := os.Stat(xdg); err == nil {
		return xdg
	}
	home, err := os.UserHomeDir()
	if err == nil {
		dotfile := filepath.Join(home, ".tmux-qs.toml")
		if _, err := os.Stat(dotfile); err == nil {
			return dotfile
		}
	}
	return ""
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

// loadConfigForPrint is the --print-config entry point: it resolves the
// config the same way the TUI does (env override, then XDG, then dotfile,
// then defaults) and returns a parse error from the underlying TOML
// decoder rather than swallowing it. Returned Config is the merged
// value the TUI would actually consume.
func loadConfigForPrint() (Config, error) {
	if path := os.Getenv("TMUX_QS_CONFIG"); path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			return parseConfigBytes(data)
		}
	}
	if path := xdgConfigPath("config.toml"); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			return parseConfigBytes(data)
		}
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		if data, err := os.ReadFile(filepath.Join(home, ".tmux-qs.toml")); err == nil {
			return parseConfigBytes(data)
		}
	}
	return defaultConfig, nil
}

// parseConfigBytes decodes raw TOML into a Config, returning the
// underlying parse error so --print-config can report it to the user
// with a non-zero exit code (loadConfig's readConfigFile logs the
// error and falls through to defaults, which is wrong for a validator).
func parseConfigBytes(data []byte) (Config, error) {
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return mergeConfig(c, defaultConfig), nil
}

// printResolvedConfig serializes cfg as TOML and writes it to stdout.
// Uses go-toml/v2's Marshaler for stable, deterministic output so
// --print-config output is suitable as a "canonical" config for diffing
// and committing.
func printResolvedConfig(cfg Config) error {
	out, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
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
	if out.Waiting.PinnedOnly == nil {
		out.Waiting.PinnedOnly = def.Waiting.PinnedOnly
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
	if len(out.Resurrect.RestorePrograms) == 0 {
		out.Resurrect.RestorePrograms = def.Resurrect.RestorePrograms
	}
	if out.Naming.Strategy == "" {
		out.Naming.Strategy = defaultNamingStrategy
	}
	if out.Naming.ShowPathWhenDuplicate == nil {
		out.Naming.ShowPathWhenDuplicate = &defaultShowPathWhenDuplicate
	}
	if len(out.Templates) == 0 {
		out.Templates = def.Templates
	}
	if len(out.Snippets) == 0 {
		out.Snippets = def.Snippets
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

# Float sessions that currently have a waiting agent to the top of the
# default/all list (just under pinned entries). Default: true.
# float_to_top = true

# Optional: workspace save/restore (Command Palette "Resurrect: …").
[resurrect]
# restore_programs is the allowlist of foreground programs that may be
# re-launched on restore. Anything not listed is left as a bare shell, so
# restore never re-runs an arbitrary command that happened to be running.
# restore_programs = ["nvim", "vim", "less", "htop", "lazygit", "ssh"]
#
# auto_save_interval, when set to a non-zero Go duration, snapshots the
# workspace on that interval while the TUI is open (silent). Empty = off.
# auto_save_interval = "15m"

# Optional: pane-aware reusable snippets. Space targets the current session;
# Ctrl-s targets the selected session or pane. Commands include the target
# pane's foreground command. Empty commands =
# available for every pane. Set text for literal input, or keys for tmux key
# notation such as C-c / M-j. In the snippet list, Enter sends then closes;
# Space sends while keeping the list open for repeated inputs.
# [[snippet]]
# name = "Claude: continue"
# commands = ["claude", "codex"]
# text = "/continue"
# submit = true
# favorite = true  # put this action at the top for gamepad use

# Optional: rebind keys. Map an action name to a key. Freed default keys
# stop triggering their old action. Key syntax matches Bubble Tea
# ("ctrl+w", "alt+enter", "ctrl+1"); the prefixes c-/m-/a-/s- also work.
# Actions: all, tmux, configs, zoxide, zoxide-root, find, panes, windows,
# ssh, commands, waiting, cleanup, copy, rename, kill, branch, pin, agent,
# template, files, new-session, open-remote, send, snippets, toggle-close,
# tag-filter, group-filter, detail, jump-next, jump-prev, visit-back,
# visit-fwd, preview-up, preview-down, undo.
# [keybindings]
# waiting = "ctrl+1"
# kill    = "ctrl+k"

# Optional: control how new tmux session names are derived from the
# target directory. Only kicks in when the chosen basename would
# collide with an existing session, so the common case is unchanged.
[naming]
# strategy: what to do on a basename collision.
#   "parent-basename" (default): prefix with the immediate parent,
#                                e.g. "~/work/foo" → "work-foo".
#   "suffix":  legacy behavior, append "-1", "-2", …
#   "hash6":   append a 6-character hash of the path, e.g. "foo-a3f2c1".
# strategy = "parent-basename"
#
# show_path_when_duplicate: when more than one session shares a
# basename, append a short path fragment (e.g. "~/work/foo") to the
# picker row so the user can tell them apart at a glance. Default true.
# show_path_when_duplicate = true

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

# Foreground command names that count as "an interactive shell/editor
# already rooted at the target directory". When you connect to a path
# that is *inside* the currently attached tmux session, tmux-qs walks
# the session's panes and picks the first one whose:
#   1. cwd matches the target path (or sits under it), AND
#   2. foreground command is in this list
# If found, it switches the tmux client to that pane. If no match is
# found, a new window is opened in the attached session at the path
# instead. Override this list to match your shell / editor of choice.
# pane_shells = ["nu", "nvim", "fish", "bash", "zsh"]

# Optional: user-defined named sessions, surfaced by Ctrl-g. Each
# entry has a name (display label) and a path (directory the
# session is rooted at; "~" is expanded). Entries whose target
# directory doesn't exist are silently skipped.
#
# The "group" field is an optional label that groups related
# sessions under a shared header in the list view (e.g. "work",
# "personal"). Sessions with the same group are also filterable
# via Ctrl-;.
#
# [[session]]
# name = "docs"
# path = "~/projects/docs"
# group = "work"
#
# [[session]]
# name = "scratch"
# path = "/tmp/scratch"
# group = "personal"

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
