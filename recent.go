package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// recentFile records which sessions the user has most recently chosen
// via Enter. Used by the TUI to bubble frequently-used entries to the
// top of the list (when recency sort is enabled).
//
// Persisted as JSON in the cache directory. The map is small (few
// tens of entries at most), so we use a plain JSON object on every
// write without worrying about partial-write safety — the file is
// best-effort, and any corruption just resets the recency ordering.
//
// The on-disk type uses exported fields (cachedRecentFile) so that
// encoding/json actually serializes them; the in-memory recentFile
// keeps unexported fields for encapsulation.
type recentFile struct {
	// entries is session name -> last-selected unix timestamp.
	entries map[string]int64
}

// cachedRecentFile is the JSON-serializable mirror of recentFile.
type cachedRecentFile struct {
	Entries map[string]int64 `json:"entries"`
}

const recentFileName = "recent.json"

func recentPath() string {
	return xdgCachePath(recentFileName)
}

// loadRecent reads the recent file. Missing/corrupt → empty.
func loadRecent() recentFile {
	path := recentPath()
	if path == "" {
		return recentFile{entries: map[string]int64{}}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return recentFile{entries: map[string]int64{}}
	}
	var c cachedRecentFile
	if err := json.Unmarshal(data, &c); err != nil {
		return recentFile{entries: map[string]int64{}}
	}
	if c.Entries == nil {
		return recentFile{entries: map[string]int64{}}
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
		c.Entries = map[string]int64{}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// recordTouch bumps the timestamp for name to now.
func (f recentFile) recordTouch(name string) recentFile {
	if f.entries == nil {
		f.entries = map[string]int64{}
	}
	f.entries[name] = now().Unix()
	return f
}

// orderedByRecency returns the given entries sorted by recency
// (most-recent-first). Entries not in the recency map sink to the
// bottom in their original relative order. The returned slice is a
// fresh copy.
func (f recentFile) orderedByRecency(entries []string) []string {
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, len(entries))
	copy(out, entries)
	sort.SliceStable(out, func(i, j int) bool {
		ti, oki := f.entries[out[i]]
		tj, okj := f.entries[out[j]]
		if oki != okj {
			return oki // known entries before unknown ones
		}
		if oki {
			return ti > tj // newer first
		}
		// Both unknown: keep original (stable) order.
		return false
	})
	return out
}
