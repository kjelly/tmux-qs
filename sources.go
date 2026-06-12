package main

import (
	"os"
	"path/filepath"
	"strings"
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
	default:
		return "⚡  "
	}
}

func loadSource(kind sourceKind) ([]string, error) {
	switch kind {
	case srcDefault, srcAll, srcTmux:
		// All three load exactly the same thing: the names of
		// currently-running tmux sessions. The "Default" / "All"
		// distinction used to be about sesh's group-header
		// filtering; without sesh, there's nothing to filter.
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
	lines, err := runLines("zoxide", "query", "--list", "--score")
	if err != nil {
		// zoxide not installed / no database yet — treat as
		// "no entries", not a hard error. The picker just shows
		// an empty list and the user picks another source.
		return nil, nil
	}
	home, _ := os.UserHomeDir()
	rootClean := ""
	if root != "" {
		rootClean = filepath.Clean(root)
	}
	var out []string
	for _, l := range lines {
		// zoxide output format: "<score> <path>". Strip the
		// leading score field.
		idx := strings.IndexByte(l, ' ')
		if idx < 0 {
			continue
		}
		path := l[idx+1:]
		// Match root itself or true descendants only — a plain
		// prefix check would let /foo match /foobar.
		if rootClean != "" && path != rootClean && !strings.HasPrefix(path, rootClean+"/") {
			continue
		}
		// Shorten the home prefix to "~" so the picker shows
		// "~/projects/foo" instead of "/home/kjelly/projects/foo".
		if home != "" && strings.HasPrefix(path, home) {
			path = "~" + strings.TrimPrefix(path, home)
		}
		out = append(out, path)
	}
	return out, nil
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
