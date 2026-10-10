package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Waiting            WaitingConfig          `toml:"waiting"`
	Tags               TagsConfig             `toml:"tags"`
	Style              StyleConfig            `toml:"style"`
	Layout             LayoutConfig           `toml:"layout"`
	Preview            PreviewConfig          `toml:"preview"`
	Resurrect          ResurrectConfig        `toml:"resurrect"`
	Naming             NamingConfig           `toml:"naming"`
	Clipboard          ClipboardConfig        `toml:"clipboard"`
	UI                 UIConfig               `toml:"ui"`
	Sessions           []SessionEntry         `toml:"session"`
	Templates          []TemplateConfig       `toml:"template"`
	Commands           []UserCommandConfig    `toml:"command"`
	Snippets           []SnippetConfig        `toml:"snippet"`
	DynamicSnippets    []DynamicSnippetConfig `toml:"dynamic_snippet"`
	SnippetCommandSets map[string][]string    `toml:"snippet_command_sets"`
	// Keybindings maps an action name (see defaultActionKeys) to a key,
	// rebinding that action. Unknown actions are ignored with a warning.
	Keybindings map[string]string `toml:"keybindings"`
}

type ClipboardConfig struct {
	Source   string `toml:"source"`
	Timeout  string `toml:"timeout"`
	MaxBytes int    `toml:"max_bytes"`
}

func (c ClipboardConfig) ParsedSource() string {
	switch c.Source {
	case "off", "tmux-buffer", "terminal":
		return c.Source
	case "":
		return "tmux-buffer"
	default:
		log.Printf("tmux-qs: unknown clipboard source %q (using tmux-buffer)", c.Source)
		return "tmux-buffer"
	}
}

func (c ClipboardConfig) ParsedTimeout() time.Duration {
	if c.Timeout == "" {
		return 250 * time.Millisecond
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		log.Printf("tmux-qs: invalid clipboard timeout %q: %v (using 250ms)", c.Timeout, err)
		return 250 * time.Millisecond
	}
	if d < 50*time.Millisecond {
		return 50 * time.Millisecond
	}
	if d > 2*time.Second {
		return 2 * time.Second
	}
	return d
}

func (c ClipboardConfig) ValidatedMaxBytes() int {
	if c.MaxBytes <= 0 {
		return 4096
	}
	if c.MaxBytes > 65536 {
		return 65536
	}
	return c.MaxBytes
}

type UIConfig struct {
	Backend string `toml:"backend"`
}

// TagsConfig controls how tags are shown in the session list. Tag filtering
// continues to use auto-detected tags regardless of this display setting.
type TagsConfig struct {
	ShowAutoDetected bool `toml:"show_auto_detected"`
}

// PreviewConfig controls repository details shown in the preview panel.
// Pointer fields distinguish an omitted value (default on) from false.
type PreviewConfig struct {
	GitStatus *bool `toml:"git_status"`
	GitLog    *bool `toml:"git_log"`
}

func (p PreviewConfig) ShowGitStatus() bool {
	return p.GitStatus == nil || *p.GitStatus
}

func (p PreviewConfig) ShowGitLog() bool {
	return p.GitLog == nil || *p.GitLog
}

func (u UIConfig) ParsedBackend() string {
	switch u.Backend {
	case "popup", "floating":
		return u.Backend
	case "":
		return "popup"
	default:
		log.Printf("tmux-qs: unknown UI backend %q (using popup)", u.Backend)
		return "popup"
	}
}

type DynamicSnippetConfig struct {
	// Name is an optional display label. When omitted, the expanded Text is
	// used as the label without changing the text sent to the pane.
	Name        string   `toml:"name"`
	Matches     []string `toml:"matches"`
	Text        string   `toml:"text"`
	Commands    []string `toml:"commands"`
	CommandSets []string `toml:"command_sets"`
	Submit      bool     `toml:"submit"`
	Favorite    bool     `toml:"favorite"`
	Disabled    bool     `toml:"disabled"`
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

// SnippetConfig is one canned snippet that can be inserted into a
// pane. Exactly one of Text or Keys should be set: Text is inserted literally,
// while Keys uses tmux key notation (for example "C-c" or "M-j"). Submit
// controls whether tmux-qs appends an Enter key after the chosen input.
type SnippetConfig struct {
	// Name is an optional display label. When omitted, Text or Keys supplies
	// the label without changing the action sent to the pane.
	Name string `toml:"name"`
	// Paths limits this snippet to panes whose working directory is inside
	// one of these directories. An empty list leaves it available everywhere.
	Paths       []string `toml:"paths"`
	Commands    []string `toml:"commands"`
	CommandSets []string `toml:"command_sets"`
	Text        string   `toml:"text"`
	Keys        []string `toml:"keys"`
	Submit      bool     `toml:"submit"`
	// Favorite snippets are placed first in the picker. This keeps the
	// gamepad path usable without requiring text filtering.
	Favorite  bool `toml:"favorite"`
	NoHistory bool `toml:"-"`
	// Acknowledge is an internal pane action; it never sends keys.
	Acknowledge bool `toml:"-"`
	// Snooze is an internal pane action that defers the waiting hint.
	Snooze bool `toml:"-"`
	// Note is an internal picker action used to open the selected session's
	// note editor. It is never read from config.toml or sent to a pane.
	Note bool `toml:"-"`
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
	Commands           []string `toml:"commands"`
	IdleShells         []string `toml:"idle_shells"`
	PromptRegex        []string `toml:"prompt_regex"`
	IdleThreshold      string   `toml:"idle_threshold"`
	PollInterval       string   `toml:"poll_interval"`
	ProgressStaleAfter string   `toml:"progress_stale_after"`
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
var defaultPreviewGitStatus = true
var defaultPreviewGitLog = true

// defaultPinnedOnly is the out-of-the-box default for the [waiting]
// pinned_only knob. True means "only monitor agents in pinned
// sessions" — see WaitingConfig.PinnedOnly for the rationale.
var defaultPinnedOnly = true

var defaultConfig = Config{
	Preview: PreviewConfig{
		GitStatus: &defaultPreviewGitStatus,
		GitLog:    &defaultPreviewGitLog,
	},
	Waiting: WaitingConfig{
		Commands: []string{
			"claude", "opencode", "aider", "ollama", "codex",
			"cursor", "cody", "continue", "gpt", "copilot", "crush",
			"agy", "goose", "cline", "gemini",
		},
		IdleShells: []string{},
		PromptRegex: []string{
			`continue\?`, "yes.*no", `\[Y/n\]|\[y/N\]`, `human:|assistant:`,
		},
		IdleThreshold:      "30s",
		PollInterval:       "5s",
		ProgressStaleAfter: "5m",
		FloatToTop:         true,
		PinnedOnly:         &defaultPinnedOnly,
	},
	Clipboard: ClipboardConfig{
		Source:   "tmux-buffer",
		Timeout:  "250ms",
		MaxBytes: 4096,
	},
	UI: UIConfig{
		Backend: "popup",
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
		cachedConfig = &cachedConfigEntry{cfg: c, path: configFilePath(), mtime: newestConfigMtime(), fingerprint: configFingerprint()}
	}
	return cachedConfig.cfg
}

type cachedConfigEntry struct {
	cfg         Config
	mtime       time.Time
	path        string
	fingerprint string
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

// reloadConfigIfStale checks whether the main config or any snippet file
// changed since the cache was loaded. If so, it replaces the cache atomically.
// The returned time is the newest tracked file mtime, or a non-zero sentinel
// when tracked files were removed and no tracked files remain.
//
// Safe to call from a tea.Cmd goroutine: takes the package configMu
// and never mutates *cachedConfig in place (replaces the pointer).
func reloadConfigIfStale() (Config, time.Time) {
	configMu.Lock()
	path := configFilePath()
	mtime := newestConfigMtime()
	fingerprint := configFingerprint()
	if cachedConfig != nil && path == cachedConfig.path && fingerprint == cachedConfig.fingerprint {
		// No change — return the cached value.
		c := cachedConfig.cfg
		configMu.Unlock()
		return c, mtime
	}
	if mtime.IsZero() && cachedConfig != nil && cachedConfig.fingerprint != "" {
		// Keep the update observable to the UI when the last tracked config
		// file or snippets directory has just been removed.
		mtime = time.Unix(0, 1)
	}
	// Cache is stale or absent — reload from disk.
	c := loadConfigFromDisk()
	cachedConfig = &cachedConfigEntry{cfg: c, mtime: mtime, path: path, fingerprint: fingerprint}
	configMu.Unlock()
	return c, mtime
}

// configFingerprint includes the main config and every snippet file so
// additions, edits, and removals all participate in hot reload.
func configFingerprint() string {
	paths := make([]string, 0)
	if path := configFilePath(); path != "" {
		paths = append(paths, path)
	}
	entries, err := os.ReadDir(xdgConfigPath("snippets"))
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".toml") {
				continue
			}
			paths = append(paths, filepath.Join(xdgConfigPath("snippets"), entry.Name()))
		}
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, path := range paths {
		st, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&b, "%s:missing;", path)
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d;", path, st.Size(), st.ModTime().UnixNano())
	}
	return b.String()
}

func newestConfigMtime() time.Time {
	var newest time.Time
	paths := make([]string, 0)
	if path := configFilePath(); path != "" {
		paths = append(paths, path)
	}
	entries, err := os.ReadDir(xdgConfigPath("snippets"))
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".toml") {
				continue
			}
			paths = append(paths, filepath.Join(xdgConfigPath("snippets"), entry.Name()))
		}
	}
	for _, path := range paths {
		if st, err := os.Stat(path); err == nil && st.ModTime().After(newest) {
			newest = st.ModTime()
		}
	}
	return newest
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
		if c, ok := readConfigFileRaw(path); ok {
			resolved, _ := resolveConfigWithSnippetFiles(c, false)
			return resolved
		}
	}
	if c, ok := readConfigFileRaw(xdgConfigPath("config.toml")); ok {
		resolved, _ := resolveConfigWithSnippetFiles(c, false)
		return resolved
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if c, ok := readConfigFileRaw(filepath.Join(home, ".tmux-qs.toml")); ok {
			resolved, _ := resolveConfigWithSnippetFiles(c, false)
			return resolved
		}
		if err := writeExampleConfig(xdgConfigPath("config.toml")); err != nil {
			log.Printf("tmux-qs: cannot write example config: %v", err)
		}
	} else {
		log.Printf("tmux-qs: cannot determine home directory: %v", err)
	}
	resolved, _ := resolveConfigWithSnippetFiles(Config{}, false)
	return resolved
}

func readConfigFileRaw(path string) (Config, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, false
	}
	var file Config
	if err := toml.Unmarshal(data, &file); err != nil {
		log.Printf("tmux-qs: cannot parse %s: %v (using defaults)", path, err)
		return Config{}, false
	}
	return file, true
}

func resolveConfigWithSnippetFiles(file Config, strict bool) (Config, error) {
	snippets, err := loadSnippetFiles(xdgConfigPath("snippets"))
	if err != nil {
		if strict {
			return Config{}, err
		}
		log.Printf("tmux-qs: %v (skipping invalid snippet file)", err)
	}
	file.Snippets = append(file.Snippets, snippets...)
	return mergeConfig(file, defaultConfig), nil
}

func loadSnippetFiles(dir string) ([]SnippetConfig, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read snippet directory %s: %w", dir, err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".toml") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	var snippets []SnippetConfig
	var firstErr error
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read snippet file %s: %w", path, err)
			}
			continue
		}
		var file struct {
			Snippets []SnippetConfig `toml:"snippet"`
		}
		if err := toml.Unmarshal(data, &file); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("parse snippet file %s: %w", path, err)
			}
			continue
		}
		snippets = append(snippets, file.Snippets...)
	}
	return snippets, firstErr
}

// loadConfigForPrint is the --print-config entry point: it resolves the
// config the same way the TUI does (env override, then XDG, then dotfile,
// then defaults) and returns a parse error from the underlying TOML
// decoder rather than swallowing it. Returned Config includes snippets from
// the XDG snippets directory and is merged with built-in defaults.
func loadConfigForPrint() (Config, error) {
	if path := os.Getenv("TMUX_QS_CONFIG"); path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			file, parseErr := parseConfigBytesRaw(data)
			if parseErr != nil {
				return Config{}, parseErr
			}
			return resolveConfigWithSnippetFiles(file, true)
		}
	}
	if path := xdgConfigPath("config.toml"); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			file, parseErr := parseConfigBytesRaw(data)
			if parseErr != nil {
				return Config{}, parseErr
			}
			return resolveConfigWithSnippetFiles(file, true)
		}
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		if data, err := os.ReadFile(filepath.Join(home, ".tmux-qs.toml")); err == nil {
			file, parseErr := parseConfigBytesRaw(data)
			if parseErr != nil {
				return Config{}, parseErr
			}
			return resolveConfigWithSnippetFiles(file, true)
		}
	}
	return resolveConfigWithSnippetFiles(Config{}, true)
}

// parseConfigBytes decodes raw TOML into a Config, returning the
// underlying parse error so --print-config can report it to the user
// with a non-zero exit code (loadConfig's readConfigFileRaw logs the
// error and falls through to defaults, which is wrong for a validator).
func parseConfigBytes(data []byte) (Config, error) {
	file, err := parseConfigBytesRaw(data)
	if err != nil {
		return Config{}, err
	}
	return mergeConfig(file, defaultConfig), nil
}

func parseConfigBytesRaw(data []byte) (Config, error) {
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
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
	if out.Preview.GitStatus == nil {
		out.Preview.GitStatus = def.Preview.GitStatus
	}
	if out.Preview.GitLog == nil {
		out.Preview.GitLog = def.Preview.GitLog
	}
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
	if out.Waiting.ProgressStaleAfter == "" {
		out.Waiting.ProgressStaleAfter = def.Waiting.ProgressStaleAfter
	}
	if out.Waiting.PinnedOnly == nil {
		out.Waiting.PinnedOnly = def.Waiting.PinnedOnly
	}
	if out.Clipboard.Source == "" {
		out.Clipboard.Source = def.Clipboard.Source
	}
	if out.Clipboard.Timeout == "" {
		out.Clipboard.Timeout = def.Clipboard.Timeout
	}
	if out.Clipboard.MaxBytes == 0 {
		out.Clipboard.MaxBytes = def.Clipboard.MaxBytes
	}
	if out.UI.Backend == "" {
		out.UI.Backend = def.UI.Backend
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
	warnUnknownSnippetCommandSets(out.Snippets, out.DynamicSnippets, out.SnippetCommandSets)
	// Sessions is a user-defined list; no built-in defaults to fill
	// in. Leave it as-is (potentially nil) so srcConfigs can
	// distinguish "no entries" from "entries but empty list".
	return out
}

func warnUnknownSnippetCommandSets(snippets []SnippetConfig, dynamic []DynamicSnippetConfig, sets map[string][]string) {
	unknown := make(map[string]bool)
	warn := func(names []string) {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if _, ok := sets[name]; ok || unknown[name] {
				continue
			}
			unknown[name] = true
			log.Printf("tmux-qs: unknown snippet command set %q (referencing snippets disabled)", name)
		}
	}
	for _, snippet := range snippets {
		warn(snippet.CommandSets)
	}
	for _, rule := range dynamic {
		warn(rule.CommandSets)
	}
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

# Automatically detected project tags (for example [go, make, ci]) are
# hidden in session rows by default. Set true to show them. Ctrl-, tag
# filtering still includes these tags when they are hidden here.
[tags]
# show_auto_detected = true

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
# notation such as C-c / M-j. Name is optional and defaults to text, or to the
# joined key sequence. In the snippet list, Enter sends then closes; Space
# sends while keeping the list open for repeated inputs.
# To keep this file compact, put more [[snippet]] entries in separate files
# under $XDG_CONFIG_HOME/tmux-qs/snippets/ (default: ~/.config/tmux-qs/snippets/).
# All *.toml files there load in filename order and hot-reload when changed.
# Reuse command lists by declaring [snippet_command_sets], then reference one
# or more names with command_sets. Literal commands and referenced sets form a
# union. An unknown set disables that snippet instead of making it global.
# [snippet_command_sets]
# nvim = ["nvim"]
# agent = ["opencode", "ollama", "codex", "claude", "crush", "agy"]
#
# [[snippet]]
# command_sets = ["agent"]
# text = "/continue"
# submit = true
# favorite = true  # put this action at the top for gamepad use
# Add name = "Continue" only when the displayed label should differ from text.

# Optional: rebind keys. Map an action name to a key. Freed default keys
# stop triggering their old action. Key syntax matches Bubble Tea
# ("ctrl+w", "alt+enter", "ctrl+1"); the prefixes c-/m-/a-/s- also work.
# Actions: all, tmux, configs, zoxide, zoxide-root, find, panes, windows,
# ssh, commands, waiting, cleanup, copy, rename, kill, branch, pin, agent,
# template, files, new-session, open-remote, toggle-eink, send, snippets, toggle-close,
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

# Optional: repository details shown in the preview panel (default: both on).
[preview]
# git_status = true
# git_log = true

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
