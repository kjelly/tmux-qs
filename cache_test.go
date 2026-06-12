package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWaitingCacheWriteAndRead(t *testing.T) {
	withCleanCacheEnv(t)

	info := waitingInfo{
		bySession: map[string][]procCount{
			"work": {{name: "claude", count: 1}},
		},
		byPath:       map[string][]procCount{"/home/u/work": {{name: "claude", count: 1}}},
		paths:        map[string]string{"work": "/home/u/work"},
		totalWaiting: 1,
		version:      7,
		lastUpdated:  time.Now(),
	}
	if err := writeWaitingCache(info); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := loadWaitingCache()
	if !ok {
		t.Fatal("expected cache hit after fresh write")
	}
	if got.totalWaiting != 1 {
		t.Errorf("totalWaiting = %d, want 1", got.totalWaiting)
	}
	if !reflect.DeepEqual(got.bySession, info.bySession) {
		t.Errorf("bySession = %+v, want %+v", got.bySession, info.bySession)
	}
	if !reflect.DeepEqual(got.byPath, info.byPath) {
		t.Errorf("byPath = %+v, want %+v", got.byPath, info.byPath)
	}
	if !reflect.DeepEqual(got.paths, info.paths) {
		t.Errorf("paths = %+v, want %+v", got.paths, info.paths)
	}
	if got.version != 7 {
		t.Errorf("version = %d, want 7", got.version)
	}
	// lastUpdated is intentionally NOT preserved across cache load.
	if !got.lastUpdated.IsZero() {
		t.Errorf("lastUpdated should be zero after cache load, got %v", got.lastUpdated)
	}
}

func TestWaitingCacheExpired(t *testing.T) {
	withCleanCacheEnv(t)

	// 10s-old entry is well past the 5s TTL.
	info := waitingInfo{
		bySession:    map[string][]procCount{"work": {{name: "claude", count: 1}}},
		totalWaiting: 1,
		lastUpdated:  time.Now().Add(-10 * time.Second),
	}
	if err := writeWaitingCache(info); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := loadWaitingCache(); ok {
		t.Errorf("expected cache miss for 10s-old entry")
	}
}

func TestWaitingCacheZeroLastUpdatedRejected(t *testing.T) {
	withCleanCacheEnv(t)

	// Hand-craft a JSON file with last_updated omitted (zero value).
	// This must be rejected — a missing timestamp means we can't trust
	// the freshness check, so treat as cache miss.
	if err := writeRawCache(t, `{"by_session":{},"by_path":{},"paths":{},"total_waiting":0,"version":0}`); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	if _, ok := loadWaitingCache(); ok {
		t.Errorf("expected cache miss when lastUpdated is zero")
	}
}

func TestNewModelLoadsCache(t *testing.T) {
	withCleanCacheEnv(t)

	info := waitingInfo{
		bySession:    map[string][]procCount{"work": {{name: "claude", count: 1}}},
		totalWaiting: 1,
		version:      5,
		lastUpdated:  time.Now(),
	}
	if err := writeWaitingCache(info); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := newModel()
	if m.waiting.totalWaiting != 1 {
		t.Errorf("newModel should load cache, got totalWaiting=%d", m.waiting.totalWaiting)
	}
	if _, ok := m.waiting.bySession["work"]; !ok {
		t.Errorf("newModel should populate bySession from cache, got %+v", m.waiting.bySession)
	}
}

func TestNewModelCacheMissLeavesWaitingZero(t *testing.T) {
	withCleanCacheEnv(t)

	m := newModel()
	if m.waiting.totalWaiting != 0 {
		t.Errorf("without cache, newModel should leave waiting zero, got %+v", m.waiting)
	}
	if m.waiting.bySession != nil {
		t.Errorf("without cache, newModel should leave bySession nil, got %+v", m.waiting.bySession)
	}
}

// withCleanCacheEnv redirects XDG_CACHE_HOME and HOME to a temp dir so
// loadWaitingCache / writeWaitingCache operate on a scratch location.
// t.Cleanup restores both env vars.
func withCleanCacheEnv(t *testing.T) {
	t.Helper()
	prevXDG, hadXDG := os.LookupEnv("XDG_CACHE_HOME")
	prevHome, hadHome := os.LookupEnv("HOME")
	tmp := t.TempDir()
	os.Setenv("XDG_CACHE_HOME", tmp)
	os.Setenv("HOME", tmp)
	t.Cleanup(func() {
		if hadXDG {
			os.Setenv("XDG_CACHE_HOME", prevXDG)
		} else {
			os.Unsetenv("XDG_CACHE_HOME")
		}
		if hadHome {
			os.Setenv("HOME", prevHome)
		} else {
			os.Unsetenv("HOME")
		}
	})
}

// writeRawCache writes arbitrary JSON to the cache file path (bypasses
// the typed writeWaitingCache). Used to inject malformed/edge-case
// payloads in tests.
func writeRawCache(t *testing.T, content string) error {
	t.Helper()
	path := cachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
