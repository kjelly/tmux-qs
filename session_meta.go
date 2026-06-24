package main

import (
	"strconv"
	"strings"
	"time"
)

// sessionMeta holds supplementary info about a tmux session, sourced
// from `tmux list-sessions` format variables. Used to display
// "created Nh ago" and "last active Nm ago" hints in the TUI list.
type sessionMeta struct {
	created    time.Time
	lastActive time.Time
	hasLastAct bool // tmux 3.6+ removed pane_last_activity; only true when we can read it
}

// sessionInfo combines a tmux session's working directory, window/pane
// counts, and metadata into one struct. Returned by tmuxSessionInfo()
// so the UI only needs a single `tmux list-sessions` fork for all.
type sessionInfo struct {
	path    string
	windows int
	panes   int
	meta    sessionMeta
}

// tmuxSessionInfo returns a single map from session name to its
// sessionInfo (path, window/pane counts, created/last-active). This
// replaces the two separate calls (tmuxSessionPaths + tmuxSessionMeta)
// previously used in the itemsMsg hot path — one tmux fork instead of
// two. A second lightweight fork counts panes per session.
//
// The tmux format includes session_path, session_created,
// session_activity, and session_windows; pane counts are aggregated
// from a separate list-panes pass.
func tmuxSessionInfo() map[string]sessionInfo {
	out := make(map[string]sessionInfo)
	lines, err := tmuxRunLines("list-sessions", "-F",
		"#{session_name}\t#{session_path}\t#{session_created}\t#{session_activity}\t#{session_windows}")
	if err != nil {
		return out
	}
	for _, l := range lines {
		parts := strings.Split(l, "\t")
		if len(parts) < 5 {
			continue
		}
		name, path := parts[0], parts[1]
		created, _ := strconv.ParseInt(parts[2], 10, 64)
		windows, _ := strconv.Atoi(parts[4])
		si := sessionInfo{
			path:    path,
			windows: windows,
			meta:    sessionMeta{created: time.Unix(created, 0)},
		}
		if parts[3] != "" {
			if act, err := strconv.ParseInt(parts[3], 10, 64); err == nil && act > 0 {
				si.meta.lastActive = time.Unix(act, 0)
				si.meta.hasLastAct = true
			}
		}
		out[name] = si
	}
	// Count panes per session in a single lightweight fork.
	if paneLines, err := tmuxRunLines("list-panes", "-a", "-F", "#{session_name}"); err == nil {
		for _, name := range paneLines {
			if si, ok := out[name]; ok {
				si.panes++
				out[name] = si
			}
		}
	}
	return out
}

// tmuxSessionPaths maps tmux session name -> session working
// directory. Thin wrapper around tmuxSessionInfo() that discards the
// meta half; kept for callers that only need paths.
func tmuxSessionPaths() map[string]string {
	out := make(map[string]string)
	for name, si := range tmuxSessionInfo() {
		out[name] = si.path
	}
	return out
}

// tmuxSessionMeta returns a map from session name to its metadata.
// Thin wrapper around tmuxSessionInfo() that discards the path half;
// kept for callers that only need meta. Prefer tmuxSessionInfo() in
// hot paths so both halves come from one tmux fork.
func tmuxSessionMeta() map[string]sessionMeta {
	out := make(map[string]sessionMeta)
	for name, si := range tmuxSessionInfo() {
		out[name] = si.meta
	}
	return out
}

// formatAge renders a duration as a short human-readable string suitable
// for inline display: "now", "3m", "2h", "1d", "8mo", "2y".
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	case d < 24*time.Hour:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d < 30*24*time.Hour:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d < 365*24*time.Hour:
		return strconv.FormatInt(int64(d/(30*24*time.Hour)), 10) + "mo"
	default:
		return strconv.FormatInt(int64(d/(365*24*time.Hour)), 10) + "y"
	}
}
