package main

import (
	"os"
	"path/filepath"
)

// xdgCachePath returns the absolute path for a file under the user's
// XDG cache directory ("$XDG_CACHE_HOME/<suffix>"), falling back to
// "$HOME/.cache/<suffix>" when XDG_CACHE_HOME is unset. Returns ""
// when no usable location can be determined (e.g. $HOME is empty or
// the user-home lookup fails).
//
// Centralized here so cache.go and recent.go don't each have their
// own copy of the XDG-vs-$HOME resolution logic.
func xdgCachePath(suffix string) string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "tmux-qs", suffix)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "tmux-qs", suffix)
}

// xdgConfigPath returns the absolute path for a file under the user's
// XDG config directory ("$XDG_CONFIG_HOME/<suffix>"), falling back to
// "$HOME/.config/<suffix>" when XDG_CONFIG_HOME is unset. Returns ""
// when no usable location can be determined.
func xdgConfigPath(suffix string) string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "tmux-qs", suffix)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "tmux-qs", suffix)
}
