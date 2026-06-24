package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// waitingCacheTTL is the maximum age of a cached waitingInfo snapshot that
// we are willing to reuse when seeding a new TUI. Matches the default
// poll_interval (5s) so a quick reopen of the popup within one poll cycle
// can still surface the previously seen waiting state.
const waitingCacheTTL = 5 * time.Second

// cachedProcCount mirrors procCount with exported fields so encoding/json
// can serialize it (the in-memory procCount has unexported fields and
// json ignores them even with tags).
type cachedProcCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func toCachedProcs(in []procCount) []cachedProcCount {
	if in == nil {
		return nil
	}
	out := make([]cachedProcCount, len(in))
	for i, p := range in {
		out[i] = cachedProcCount{Name: p.name, Count: p.count}
	}
	return out
}

func fromCachedProcs(in []cachedProcCount) []procCount {
	if in == nil {
		return nil
	}
	out := make([]procCount, len(in))
	for i, p := range in {
		out[i] = procCount{name: p.Name, count: p.Count}
	}
	return out
}

// cachedWaiting is the on-disk shape of a waitingInfo snapshot. We use
// a separate struct (with exported fields) so the JSON layout is stable
// and decoupled from the in-memory struct.
type cachedWaiting struct {
	BySession    map[string][]cachedProcCount `json:"by_session"`
	ByPath       map[string][]cachedProcCount `json:"by_path"`
	Paths        map[string]string            `json:"paths"`
	TotalWaiting int                          `json:"total_waiting"`
	Version      int64                        `json:"version"`
	LastUpdated  time.Time                    `json:"last_updated"`
}

// cachePath returns the on-disk location for the waiting cache file. It
// honors $XDG_CACHE_HOME (per XDG Base Directory spec) and falls back to
// $HOME/.cache. Returns "" when no usable location can be determined.
func cachePath() string {
	return xdgCachePath("waiting.json")
}

// loadWaitingCache returns the most recent waitingInfo snapshot from disk
// if it exists and is fresher than waitingCacheTTL. The returned snapshot
// has lastUpdated cleared (loading from cache does NOT count as a fresh
// watcher tick — only watchCmd's successful poll bumps that).
func loadWaitingCache() (waitingInfo, bool) {
	path := cachePath()
	if path == "" {
		return waitingInfo{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return waitingInfo{}, false
	}
	var c cachedWaiting
	if err := json.Unmarshal(data, &c); err != nil {
		return waitingInfo{}, false
	}
	if c.LastUpdated.IsZero() {
		return waitingInfo{}, false
	}
	if time.Since(c.LastUpdated) > waitingCacheTTL {
		return waitingInfo{}, false
	}
	return waitingInfo{
		bySession:    fromCachedByMap(c.BySession),
		byPath:       fromCachedByMap(c.ByPath),
		paths:        c.Paths,
		totalWaiting: c.TotalWaiting,
		version:      c.Version,
		// lastUpdated intentionally left zero: cache load is not a tick.
	}, true
}

// writeWaitingCache persists a waitingInfo snapshot to disk so a subse-
// quent TUI invocation can surface the most recent state immediately
// (subject to the 5s TTL). Write errors are returned to the caller, which
// in watchCmd simply ignores them — caching is best-effort.
func writeWaitingCache(info waitingInfo) error {
	path := cachePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	c := cachedWaiting{
		BySession:    toCachedByMap(info.bySession),
		ByPath:       toCachedByMap(info.byPath),
		Paths:        info.paths,
		TotalWaiting: info.totalWaiting,
		Version:      info.version,
		LastUpdated:  info.lastUpdated,
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func toCachedByMap(in map[string][]procCount) map[string][]cachedProcCount {
	if in == nil {
		return nil
	}
	out := make(map[string][]cachedProcCount, len(in))
	for k, v := range in {
		out[k] = toCachedProcs(v)
	}
	return out
}

func fromCachedByMap(in map[string][]cachedProcCount) map[string][]procCount {
	if in == nil {
		return nil
	}
	out := make(map[string][]procCount, len(in))
	for k, v := range in {
		out[k] = fromCachedProcs(v)
	}
	return out
}
