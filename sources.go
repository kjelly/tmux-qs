package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sourceKind identifies which backing store a TUI list is loaded
// from. Each kind is loaded on demand via loadSource(). See the
// keybinding handler for the user-facing binding of each kind.
type sourceKind int

const (
	// srcDefault is the default list shown on TUI open: existing
	// tmux sessions sorted by recency. Previously the first line
	// of `sesh list` (a group header) and any literal "popup"
	// session were filtered out — neither of those exist when
	// we ask tmux directly, so this is now just an alias for
	// srcAll recency-sorted.
	srcDefault sourceKind = iota
	// srcAll is the same as srcDefault at the load level, but the
	// recency sort is handled in the itemsMsg handler. We keep the
	// distinction so future per-source behavior can branch on it.
	srcAll
	// srcTmux lists only running tmux sessions. Bound to Ctrl-t.
	srcTmux
	// srcConfigs lists the user's [[session]] entries from
	// tmux-qs's own config. Bound to Ctrl-g.
	srcConfigs
	// srcZoxide lists all zoxide-tracked directories. Bound to
	// Ctrl-x. Runs `zoxide query --list --score` directly.
	srcZoxide
	// srcZoxideRoot lists zoxide entries that contain the
	// attached tmux session's working directory — i.e. subdirs of
	// the current workspace. Bound to Alt-r.
	srcZoxideRoot
	// srcFind lists directories under $HOME up to depth 2 (matches
	// `fd -H -d 2 -t d -E .Trash . ~`). Bound to Ctrl-f. Doesn't
	// need any external tool.
	srcFind
	// srcWaiting lists only the sessions that currently have waiting
	// agents. Bound to Ctrl-w. It is built from the in-memory watcher
	// snapshot (see model.showWaiting), not via loadSource — there is
	// no external store to load it from.
	srcWaiting
	srcFiles
	srcPanes
	srcSSH
	srcCleanup
	srcCommands
)

func (s sourceKind) prompt() string {
	switch s {
	case srcTmux:
		return "🪟  "
	case srcConfigs:
		return "⚙️  "
	case srcZoxide, srcZoxideRoot:
		return "📁  "
	case srcFind:
		return "🔎  "
	case srcWaiting:
		return "⏳  "
	case srcFiles:
		return "📄  "
	case srcPanes:
		return "🔍  "
	case srcSSH:
		return "🌐  "
	case srcCleanup:
		return "🧹  "
	case srcCommands:
		return "🛠️  "
	default:
		return "⚡  "
	}
}

func loadSource(kind sourceKind) ([]string, error) {
	switch kind {
	case srcDefault, srcAll:
		return loadAllSources()
	case srcTmux:
		return runLines("tmux", "list-sessions", "-F", "#{session_name}")
	case srcConfigs:
		return loadConfigSessions()
	case srcZoxide:
		return loadZoxide("")
	case srcZoxideRoot:
		root := attachedSessionPath()
		return loadZoxide(root)
	case srcFind:
		return findDirs()
	case srcFiles:
		return nil, nil
	case srcPanes:
		return loadPanes()
	case srcSSH:
		return loadSSHHosts()
	case srcCleanup:
		return loadCleanup()
	case srcCommands:
		return loadCommands()
	}
	return nil, nil
}

// loadConfigSessions returns the names of the user-defined session
// entries from tmux-qs's own config. Entries whose path doesn't
// resolve to an existing directory are silently skipped — there's
// no point listing a session for a directory that has been deleted.
// Always returns a non-nil slice so callers can rely on ranging
// over the result without a nil check.
func loadConfigSessions() ([]string, error) {
	cfg := loadConfig()
	out := []string{}
	for _, s := range cfg.Sessions {
		if s.Name == "" {
			continue
		}
		path := s.ExpandedPath()
		if path == "" {
			continue
		}
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, s.Name)
	}
	return out, nil
}

// loadZoxide runs `zoxide query --list --score` and returns the
// directory paths (with the home prefix shortened to "~") suitable
// for display. If root is non-empty, only entries that contain root
// as a path prefix are returned; this is the "subdirs of the current
// workspace" filter used by srcZoxideRoot.
//
// Returns an empty list (no error) if zoxide is not installed or
// has no entries — callers fall back to an empty picker.
func loadZoxide(root string) ([]string, error) {
	lines, err := runLines("zoxide", "query", "--list")
	if err != nil {
		return nil, nil
	}
	home, _ := os.UserHomeDir()
	rootClean := ""
	if root != "" {
		rootClean = filepath.Clean(root)
	}
	var out []string
	for _, l := range lines {
		path := strings.TrimSpace(l)
		if path == "" {
			continue
		}
		if rootClean != "" && path != rootClean && !strings.HasPrefix(path, rootClean+"/") {
			continue
		}
		if home != "" && strings.HasPrefix(path, home) {
			path = "~" + strings.TrimPrefix(path, home)
		}
		out = append(out, path)
	}
	return out, nil
}

func loadAllSources() ([]string, error) {
	var all []string

	// 1. Load tmux sessions
	tmuxSessions, _ := runLines("tmux", "list-sessions", "-F", "#{session_name}")
	all = append(all, tmuxSessions...)

	// 2. Load configured sessions
	configs, _ := loadConfigSessions()
	all = append(all, configs...)

	// 3. Load zoxide directories
	zoxides, _ := loadZoxide("")
	all = append(all, zoxides...)

	// Deduplicate items while preserving order
	seen := make(map[string]bool)
	var deduped []string
	for _, item := range all {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			seen[item] = true
			deduped = append(deduped, item)
		}
	}
	return deduped, nil
}

// findDirs mimics `fd -H -d 2 -t d -E .Trash . ~`: directories under the home
// directory up to depth 2, hidden included, .Trash excluded.
func findDirs() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var out []string
	level1, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	for _, e1 := range level1 {
		if !e1.IsDir() || e1.Name() == ".Trash" {
			continue
		}
		p1 := filepath.Join(home, e1.Name())
		out = append(out, p1+"/")
		level2, err := os.ReadDir(p1)
		if err != nil {
			continue
		}
		for _, e2 := range level2 {
			if !e2.IsDir() || e2.Name() == ".Trash" {
				continue
			}
			out = append(out, filepath.Join(p1, e2.Name())+"/")
		}
	}
	return out, nil
}

// expandPath expands a leading "~" to the user's home directory and cleans
// trailing slashes.
func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	if strings.HasPrefix(p, "/") {
		p = filepath.Clean(p)
	}
	return p
}

// looksLikePath reports whether a list entry refers to a directory rather
// than a tmux session or config name.
func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~")
}

func findFiles(dir string) ([]string, error) {
	dir = expandPath(dir)
	var files []string
	maxFiles := 1000
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(files) >= maxFiles {
			return filepath.SkipDir
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && name != "." && name != ".." {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || name == "tmux-qs" {
			return nil
		}
		if isBinaryFile(path) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err == nil {
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

func isBinaryFile(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	buf := make([]byte, 4)
	n, err := file.Read(buf)
	if err != nil || n < 4 {
		return false
	}
	// Check ELF magic (\x7fELF)
	return buf[0] == 0x7f && buf[1] == 'E' && buf[2] == 'L' && buf[3] == 'F'
}

// loadPanes lists all active tmux panes across all sessions.
// It formats them as "session_name:window_index.pane_index  [command]  path"
func loadPanes() ([]string, error) {
	// Format: session:window.pane<TAB>command<TAB>path<TAB>pane_id
	lines, err := runLines("tmux", "list-panes", "-a", "-F", "#{session_name}:#{window_index}.#{pane_index}\t#{pane_current_command}\t#{pane_current_path}\t#{pane_id}")
	if err != nil {
		return nil, err
	}
	var out []string
	home, _ := os.UserHomeDir()
	for _, l := range lines {
		parts := strings.SplitN(l, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		name, cmd, path, paneID := parts[0], parts[1], parts[2], parts[3]
		if home != "" && strings.HasPrefix(path, home) {
			path = "~" + strings.TrimPrefix(path, home)
		}
		// We embed the session name and paneID at the very end after tabs.
		// Format: display_text \t session_name \t paneID
		display := fmt.Sprintf("%-20s %-10s %s", name, "["+cmd+"]", path)
		sessionName := strings.SplitN(name, ":", 2)[0]
		out = append(out, fmt.Sprintf("%s\t%s\t%s", display, sessionName, paneID))
	}
	return out, nil
}

// loadSSHHosts parses ~/.ssh/config for Host entries.
func loadSSHHosts() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil, nil
	}
	var hosts []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "host ") {
			parts := strings.Fields(line)
			for _, h := range parts[1:] {
				if !strings.Contains(h, "*") && !strings.Contains(h, "?") && !seen[h] {
					seen[h] = true
					hosts = append(hosts, "ssh "+h)
				}
			}
		}
	}
	return hosts, nil
}

// loadCleanup returns a list of sessions that are considered "stale".
// A session is stale if its path doesn't exist anymore, or if it hasn't
// been active in 7 days.
func loadCleanup() ([]string, error) {
	info := tmuxSessionInfo()
	var out []string
	for name, si := range info {
		stale := false
		if _, err := os.Stat(si.path); os.IsNotExist(err) {
			stale = true
		} else {
			ref := si.meta.lastActive
			if !si.meta.hasLastAct || ref.IsZero() {
				ref = si.meta.created
			}
			if !ref.IsZero() && time.Since(ref) > 7*24*time.Hour {
				stale = true
			}
		}
		if stale {
			out = append(out, name)
		}
	}
	return out, nil
}

// loadCommands returns a list of global actions for the Command Palette.
func loadCommands() ([]string, error) {
	cmds := []string{
		"Resurrect: Save Workspace State",
		"Resurrect: Restore Workspace State",
		"Tmux-QS: Open Config File",
		"Tmux: Detach Client",
		"Tmux: Reload Tmux Config",
		"Tmux: Kill Server (Danger)",
	}
	cfg := loadConfig()
	for _, c := range cfg.Commands {
		if c.Name != "" && c.Cmd != "" {
			cmds = append(cmds, c.Name)
		}
	}
	return cmds, nil
}

// autoDetectTags returns a set of tags inferred from marker files in
// the given directory. Used to supplement user-defined tags from
// config so sessions without explicit tags still get useful labels.
func autoDetectTags(dir string) []string {
	if dir == "" {
		return nil
	}
	var tags []string
	checks := []struct {
		file string
		tag  string
	}{
		{"go.mod", "go"},
		{"Cargo.toml", "rust"},
		{"package.json", "js"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
		{"setup.py", "python"},
		{"Makefile", "make"},
		{"CMakeLists.txt", "cmake"},
		{"Dockerfile", "docker"},
		{"docker-compose.yml", "docker"},
		{"docker-compose.yaml", "docker"},
		{"terraform.tf", "terraform"},
		{".github", "ci"},
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(dir, c.file)); err == nil {
			tags = append(tags, c.tag)
		}
	}
	return tags
}

// entryTags returns the tags for a list entry. Config-defined sessions
// use their explicit tags; other entries get auto-detected tags from
// their directory. Returns nil if no tags are available.
func (m model) entryTags(entry string) []string {
	entry = strings.TrimSpace(entry)
	// Check config-defined tags first.
	for _, s := range loadConfig().Sessions {
		if s.Name == entry && len(s.Tags) > 0 {
			return s.Tags
		}
	}
	// Auto-detect from directory.
	dir := entryDir(entry, m.sessionPaths)
	if dir == "" {
		return nil
	}
	return autoDetectTags(dir)
}

// allTags returns the union of all tags across config sessions and
// auto-detected tags for all current entries. Used by the tag filter
// mode to populate the selection list.
func (m model) allTags() []string {
	seen := make(map[string]bool)
	// Config-defined tags.
	for _, s := range loadConfig().Sessions {
		for _, t := range s.Tags {
			seen[t] = true
		}
	}
	// Auto-detected tags from current entries.
	for _, item := range m.items {
		dir := entryDir(item, m.sessionPaths)
		for _, t := range autoDetectTags(dir) {
			seen[t] = true
		}
	}
	var tags []string
	for t := range seen {
		tags = append(tags, t)
	}
	sortTags(tags)
	return tags
}

func sortTags(tags []string) {
	// Simple alphabetical sort.
	for i := 0; i < len(tags); i++ {
		for j := i + 1; j < len(tags); j++ {
			if tags[i] > tags[j] {
				tags[i], tags[j] = tags[j], tags[i]
			}
		}
	}
}
