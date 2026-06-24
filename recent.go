package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// recentFile records which sessions the user has most recently chosen
// via Enter, and how often. Used by the TUI to bubble frequently-used
// entries to the top of the list via a zoxide-style "frecency" score
// (frequency weighted by how recently the entry was last touched).
//
// Persisted as JSON in the cache directory. The map is small (few
// tens of entries at most), so we use a plain JSON object on every
// write without worrying about partial-write safety — the file is
// best-effort, and any corruption just resets the ordering.
//
// The on-disk type uses exported fields (cachedRecentFile) so that
// encoding/json actually serializes them; the in-memory recentFile
// keeps unexported fields for encapsulation.
type recentFile struct {
	// entries is session name -> access record (count + last-touch
	// unix timestamp).
	entries map[string]recentEntry
}

// recentEntry is one frecency record: how many times the entry has been
// chosen and the unix timestamp of the most recent choice.
type recentEntry struct {
	Count int   `json:"count"`
	Last  int64 `json:"last"`
}

// cachedRecentFile is the JSON-serializable mirror of recentFile.
type cachedRecentFile struct {
	Entries map[string]recentEntry `json:"entries"`
}

const recentFileName = "recent.json"

func recentPath() string {
	return xdgCachePath(recentFileName)
}

// loadRecent reads the recent file. Missing/corrupt → empty.
func loadRecent() recentFile {
	path := recentPath()
	if path == "" {
		return recentFile{entries: map[string]recentEntry{}}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return recentFile{entries: map[string]recentEntry{}}
	}
	var c cachedRecentFile
	if err := json.Unmarshal(data, &c); err != nil {
		return recentFile{entries: map[string]recentEntry{}}
	}
	if c.Entries == nil {
		return recentFile{entries: map[string]recentEntry{}}
	}
	return recentFile{entries: c.Entries}
}

// save writes the recent file. Best-effort: errors are silently
// dropped (logging would be noisy on every Enter).
func (f recentFile) save() {
	path := recentPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	c := cachedRecentFile{Entries: f.entries}
	if c.Entries == nil {
		c.Entries = map[string]recentEntry{}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// recordTouch bumps the access count for name and sets its last-touch
// timestamp to now.
func (f recentFile) recordTouch(name string) recentFile {
	if f.entries == nil {
		f.entries = map[string]recentEntry{}
	}
	e := f.entries[name]
	e.Count++
	e.Last = now().Unix()
	f.entries[name] = e
	return f
}

// frecencyOf returns the frecency score for name and whether name is
// known. The score follows zoxide's aging buckets: the base weight is
// the access count, multiplied by a recency factor that decays as the
// last touch grows older. Higher scores rank earlier.
func (f recentFile) frecencyOf(name string) (float64, bool) {
	e, ok := f.entries[name]
	if !ok {
		return 0, false
	}
	return frecencyScore(e, now().Unix()), true
}

// frecencyScore computes the zoxide-style frecency for one entry given
// the current unix time. The recency multiplier matches zoxide's
// defaults: <1h ×4, <1d ×2, <1w ×0.5, older ×0.25.
func frecencyScore(e recentEntry, nowUnix int64) float64 {
	count := float64(e.Count)
	if count <= 0 {
		count = 1
	}
	age := nowUnix - e.Last
	switch {
	case age < 3600:
		return count * 4
	case age < 86400:
		return count * 2
	case age < 604800:
		return count * 0.5
	default:
		return count * 0.25
	}
}

// orderedByFrecency returns the given entries sorted by frecency score
// (highest first). Entries not in the recency map sink to the bottom in
// their original relative order. The returned slice is a fresh copy.
func (f recentFile) orderedByFrecency(entries []string) []string {
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, len(entries))
	copy(out, entries)
	nowUnix := now().Unix()
	score := func(name string) (float64, bool) {
		e, ok := f.entries[name]
		if !ok {
			return 0, false
		}
		return frecencyScore(e, nowUnix), true
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, oki := score(out[i])
		sj, okj := score(out[j])
		if oki != okj {
			return oki // known entries before unknown ones
		}
		if oki {
			if si != sj {
				return si > sj // higher frecency first
			}
			// Same frecency bucket: break ties by most-recent touch.
			return f.entries[out[i]].Last > f.entries[out[j]].Last
		}
		// Both unknown: keep original (stable) order.
		return false
	})
	return out
}

// recentFromRecent returns just the recent.json record for a list
// entry. Used by the directory-group sort when the entry has no
// zoxide score: configured sessions and zoxide paths not currently
// in the zoxide list both fall through to the picker-driven record.
// Returns (last, true) when name has a non-zero Last; otherwise
// (0, false) so the caller can sink the entry to the bottom of its
// group. Unlike recencyOf this does NOT consult m.sessionInfo — the
// directory group never contains a running tmux session, so going
// straight to recent.json is the right shape.
func recentFromRecent(name string, recent recentFile) (int64, bool) {
	e, ok := recent.entries[name]
	if !ok || e.Last <= 0 {
		return 0, false
	}
	return e.Last, true
}

// recencyOf returns the most recent "use" timestamp for a list entry,
// in unix seconds, plus a boolean indicating whether the entry has
// any recency signal at all. Higher value means more recent.
//
// Resolution order (best to fallback):
//
//  1. For tmux sessions: the in-memory sessionInfo has session_meta
//     populated from #{session_activity}. This timestamp tracks the
//     last time any pane in the session was active (typing, command
//     execution, pane focus changes) — so a session the user has
//     been actively using inside tmux is treated as "recent" even
//     when the picker was never opened for it. This is the primary
//     fix for "currently-active sessions sink to the bottom" when
//     the user hasn't pressed Enter in this picker for them.
//
//  2. For all entries (including configured sessions, zoxide paths,
//     and tmux sessions that have no activity yet — e.g. freshly
//     created and never touched): the recent.json record updated on
//     every picker Enter. Covers the cross-source fallback so a
//     configured workspace the user frequently picks wins over one
//     they have never opened.
//
//  3. Entry has no recency signal at all: returns (0, false) so the
//     caller can drop it to the bottom in stable order.
//
// info is m.sessionInfo; in all-servers mode the entry has the
// "[server] name" prefix, so the bare name is extracted via
// sessionServer before lookup.
func recencyOf(entry string, info map[string]sessionInfo, recent recentFile) (int64, bool) {
	// In all-servers mode, entries carry a server prefix that
	// doesn't appear in sessionInfo (which is keyed by bare name).
	bare := entry
	if _, b := sessionServer(entry); b != "" {
		bare = b
	}
	if si, ok := info[bare]; ok && si.meta.hasLastAct && !si.meta.lastActive.IsZero() {
		return si.meta.lastActive.Unix(), true
	}
	if e, ok := recent.entries[entry]; ok && e.Last > 0 {
		return e.Last, true
	}
	// For tmux sessions with no activity (fresh, untouched) we
	// have no useful signal — fall through. For non-tmux entries
	// the recent.json check above already covered it.
	return 0, false
}
