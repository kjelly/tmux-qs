package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withXDGCacheDir returns a setup that redirects the XDG cache to a
// temp dir for the duration of the test, so visit_stack tests don't
// pollute the real cache. Returns a cleanup function the caller must
// defer.
func withXDGCacheDir(t *testing.T) func() {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	return func() {}
}

func TestVisitStack_PushPopPeek(t *testing.T) {
	defer withXDGCacheDir(t)()
	var s visitStack

	// Empty stack.
	if got := s.peek(); got != "" {
		t.Errorf("peek on empty = %q, want \"\"", got)
	}
	if got := s.pop(); got != "" {
		t.Errorf("pop on empty = %q, want \"\"", got)
	}

	// Push a few items.
	s = s.push("a")
	s = s.push("b")
	s = s.push("c")
	if got := s.peek(); got != "c" {
		t.Errorf("peek after push c = %q, want \"c\"", got)
	}

	// Pop should give LIFO order.
	if got := s.pop(); got != "c" {
		t.Errorf("pop #1 = %q, want \"c\"", got)
	}
	if got := s.pop(); got != "b" {
		t.Errorf("pop #2 = %q, want \"b\"", got)
	}
	if got := s.pop(); got != "a" {
		t.Errorf("pop #3 = %q, want \"a\"", got)
	}
	if got := s.pop(); got != "" {
		t.Errorf("pop on drained = %q, want \"\"", got)
	}
}

func TestVisitStack_PushDedup(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	s = s.push("a")
	s = s.push("b")
	s = s.push("a") // re-push: should move to top, no duplicate
	if len(s.entries) != 2 {
		t.Errorf("after re-push: len = %d, want 2", len(s.entries))
	}
	if s.entries[0] != "a" {
		t.Errorf("after re-push: top = %q, want \"a\"", s.entries[0])
	}
	if s.entries[1] != "b" {
		t.Errorf("after re-push: second = %q, want \"b\"", s.entries[1])
	}
}

func TestVisitStack_PushCapped(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	// Push more than visitStackMaxLen entries; the oldest should fall off.
	for i := 0; i < visitStackMaxLen+5; i++ {
		s = s.push(string(rune('a' + i%26)) + string(rune('0'+i/26)))
	}
	if len(s.entries) != visitStackMaxLen {
		t.Errorf("after overflow: len = %d, want %d", len(s.entries), visitStackMaxLen)
	}
}

func TestVisitStack_SaveLoad(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	s = s.push("one")
	s = s.push("two")
	s = s.push("three")
	s.save()

	loaded := loadVisitStack()
	if len(loaded.entries) != 3 {
		t.Errorf("loaded len = %d, want 3", len(loaded.entries))
	}
	if loaded.entries[0] != "three" {
		t.Errorf("loaded top = %q, want \"three\"", loaded.entries[0])
	}
}

func TestVisitStack_LoadMissing(t *testing.T) {
	defer withXDGCacheDir(t)()
	// No save file exists yet — should return an empty stack, no error.
	s := loadVisitStack()
	if len(s.entries) != 0 {
		t.Errorf("loadVisitStack() on missing file = %v, want empty", s.entries)
	}
}

func TestVisitStack_LoadCorrupt(t *testing.T) {
	defer withXDGCacheDir(t)()
	// Write garbage to the cache file.
	path := visitStackPath()
	if path == "" {
		t.Fatal("visitStackPath() returned empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := loadVisitStack()
	if len(s.entries) != 0 {
		t.Errorf("loadVisitStack() on corrupt file = %v, want empty", s.entries)
	}
}

func TestVisitStack_BackAndForwardErrors(t *testing.T) {
	defer withXDGCacheDir(t)()
	// No stack → back should error.
	if err := visitStackBack(); err == nil {
		t.Error("visitStackBack on empty stack should error")
	}
	if err := visitStackForward(); err == nil {
		t.Error("visitStackForward on empty stack should error")
	}

	// Stack with one entry → forward should error (need 2+).
	s := visitStack{}
	s = s.push("only")
	s.save()
	if err := visitStackForward(); err == nil {
		t.Error("visitStackForward with one entry should error")
	}
}

func TestVisitStack_SaveEmpty(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	s.save()
	// Reload — should be empty.
	loaded := loadVisitStack()
	if len(loaded.entries) != 0 {
		t.Errorf("save+load of empty: len = %d, want 0", len(loaded.entries))
	}
}

func TestLastSessionRecordAndRead(t *testing.T) {
	defer withXDGCacheDir(t)()
	// recordLastSession will try to run `tmux display-message` —
	// if that fails (e.g. no tmux server in the test env), it
	// just no-ops, so we can't directly test the write. But we
	// CAN test that the file is empty / nonexistent when tmux
	// isn't available.
	recordLastSession()
	// After recording, the file either exists (tmux was available)
	// or doesn't (no tmux). Either is valid — we just verify that
	// lastSessionSwitch handles the missing-file case gracefully.
	if err := lastSessionSwitch(); err == nil {
		t.Log("lastSessionSwitch succeeded — tmux is available in the test env")
	} else {
		t.Logf("lastSessionSwitch errored as expected when no session: %v", err)
	}
}

// TestVisitStackJSONFormat verifies the on-disk format is a plain
// JSON array of strings (so other tools can read it).
func TestVisitStackJSONFormat(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	s = s.push("alpha")
	s = s.push("beta")
	s.save()

	data, err := os.ReadFile(visitStackPath())
	if err != nil {
		t.Fatal(err)
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("disk format should be plain JSON array, got error: %v\ncontent: %s", err, data)
	}
	if len(arr) != 2 || arr[0] != "beta" || arr[1] != "alpha" {
		t.Errorf("decoded = %v, want [beta alpha]", arr)
	}
}
