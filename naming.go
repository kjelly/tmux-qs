package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// NamingConfig controls how new tmux session names are derived from
// the target directory. See deriveSessionName for the per-strategy
// behavior.
type NamingConfig struct {
	// Strategy picks the collision-resolution rule. One of:
	//   "parent-basename" (default): prefix with parent dir
	//   "suffix":                    legacy "-1", "-2" suffix
	//   "hash6":                     append a 6-char path hash
	Strategy string `toml:"strategy"`

	// ShowPathWhenDuplicate controls whether the picker appends the
	// session's cwd to its display name when more than one session
	// shares the same basename. Default true.
	ShowPathWhenDuplicate *bool `toml:"show_path_when_duplicate"`
}

// defaultNamingStrategy is the out-of-the-box collision strategy.
// "parent-basename" keeps names short and recognizable for the common
// case (e.g. ~/work/foo, ~/personal/foo → "work-foo") and only kicks
// in when the basename alone would collide.
const defaultNamingStrategy = "parent-basename"

// defaultShowPathWhenDuplicate is the out-of-the-box default for
// NamingConfig.ShowPathWhenDuplicate. When true, the picker suffixes
// disambiguating path fragments to sessions whose basename appears
// more than once, so the user can tell them apart at a glance.
var defaultShowPathWhenDuplicate = true

// deriveSessionName returns a tmux session name for the given path.
// It takes the existing session names so it can pick a non-colliding
// name, and the strategy from cfg.Naming.Strategy.
//
// Strategies:
//
//   - "parent-basename" (default): if filepath.Base(path) is unique,
//     return it. Otherwise prefix with the immediate parent dir,
//     joining with "-" (e.g. "work-foo"). If that still collides,
//     fall through to the numeric suffix below. If the path has no
//     usable parent (e.g. "/foo"), the numeric suffix is the first
//     fallback.
//   - "suffix": on collision, append "-1", "-2", ...
//   - "hash6": on collision, append "-<6-char sha256 prefix>"
//
// The returned name is always sanitizeSessionName'd.
func deriveSessionName(path string, existing map[string]bool, strategy string) string {
	if path == "" {
		return ""
	}
	if strategy == "" {
		strategy = defaultNamingStrategy
	}
	base := filepath.Base(path)
	candidate := base
	if !existing[candidate] {
		return sanitizeSessionName(candidate)
	}

	// Build collision-resolved candidates in order of preference.
	var candidates []string
	switch strategy {
	case "suffix":
		// legacy: skip — handled by numeric fallback below
	case "hash6":
		sum := sha256.Sum256([]byte(path))
		candidates = []string{base + "-" + hex.EncodeToString(sum[:])[:6]}
	default:
		// "parent-basename" (and unknown values): prefix the basename
		// with the immediate parent directory. If that still collides
		// (rare — only happens when the user already has a session
		// called "<parent>-<basename>"), fall through to the numeric
		// suffix below.
		parent := filepath.Base(filepath.Dir(path))
		if parent != "" && parent != "." && parent != "/" {
			candidates = []string{parent + "-" + base}
		}
	}

	// Try collected candidates first.
	for _, c := range candidates {
		if !existing[c] {
			return sanitizeSessionName(c)
		}
	}

	// Final fallback: numeric suffix.
	for i := 1; i < 10000; i++ {
		c := base + "-" + itoa(i)
		if !existing[c] {
			return sanitizeSessionName(c)
		}
	}
	return sanitizeSessionName(base + "-overflow")
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}

// findDuplicateBasenames returns the set of directory basenames
// that appear as the cwd of more than one tmux session. The picker
// uses this to decide which rows need a path disambiguator suffix
// appended to their display name, so the user can tell sessions
// rooted in different directories apart when their session names
// collide (the classic "foo" vs "foo-1" situation).
//
// Returned keys are the *cwd* basenames (e.g. "foo"), not the
// session names. This matches the user's mental model: "I have two
// `foo` directories open in different places."
func findDuplicateBasenames(info map[string]sessionInfo) map[string]bool {
	counts := make(map[string]int)
	for _, si := range info {
		if si.path == "" {
			continue
		}
		counts[filepath.Base(si.path)]++
	}
	out := make(map[string]bool)
	for b, c := range counts {
		if c > 1 {
			out[b] = true
		}
	}
	return out
}

// entryDisplayName strips any "\t<path>" hint suffix from an entry
// string. In the current picker the hint lives in a separate map
// (model.entryHintPath) instead of being concatenated onto the
// entry, but this helper is still useful as a public utility and
// mirrors the same tab convention used by srcPanes.
func entryDisplayName(entry string) string {
	if i := strings.IndexByte(entry, '\t'); i >= 0 {
		return entry[:i]
	}
	return entry
}

// splitEntryWithPath splits a "displayName\thintPath" picker entry,
// returning (name, hintPath). hintPath is "" when no hint is
// present. As with entryDisplayName, production paths now use
// model.entryHintPath; this helper remains as a public utility
// for callers that prefer the tab-suffix convention.
func splitEntryWithPath(entry string) (name, hintPath string) {
	if i := strings.IndexByte(entry, '\t'); i >= 0 {
		return entry[:i], entry[i+1:]
	}
	return entry, ""
}

// showPathWhenDuplicate reports whether the picker should append a
// path disambiguator to rows whose directory basename appears in
// more than one tmux session. Honors the [naming]
// show_path_when_duplicate knob; defaults to true.
func showPathWhenDuplicate(cfg Config) bool {
	if cfg.Naming.ShowPathWhenDuplicate == nil {
		return defaultShowPathWhenDuplicate
	}
	return *cfg.Naming.ShowPathWhenDuplicate
}

// shortDisambiguator renders a session cwd as a short, picker-
// friendly string suitable for appending to a row whose basename
// collides with another session's basename. It shortens the user's
// home prefix to "~" and is deliberately compact (the trailing
// basename is already shown as the row's primary text, so we just
// show the parent context).
func shortDisambiguator(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	// Show the last two path segments — the basename is already
	// visible as the row's primary text, so the disambiguator only
	// needs to convey "which parent" to be useful.
	dir := filepath.Dir(path)
	if dir == "" || dir == "." || dir == "/" {
		return path
	}
	parent := filepath.Base(dir)
	if parent == "" || parent == "." || parent == "/" {
		return path
	}
	return parent + "/" + filepath.Base(path)
}
