package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// withXDGConfigDir points XDG_CONFIG_HOME at a temp dir for the
// duration of the test. Pinned entries are stored in the XDG
// config dir, so this is what the tests need to be isolated.
func withXDGConfigDir(t *testing.T) func() {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return func() {}
}

func TestLoadPinned_NoFile(t *testing.T) {
	defer withXDGConfigDir(t)()
	got := loadPinned()
	if got == nil {
		t.Error("loadPinned() returned nil, want empty map")
	}
	if len(got) != 0 {
		t.Errorf("loadPinned() = %v, want empty", got)
	}
}

func TestSaveAndLoadPinned(t *testing.T) {
	defer withXDGConfigDir(t)()
	pinned := map[string]bool{
		"work-1":   true,
		"work-2":   true,
		"unpinned": false, // should NOT be written
	}
	if err := savePinned(pinned); err != nil {
		t.Fatalf("savePinned: %v", err)
	}
	// Verify the on-disk format is one entry per line.
	data, err := os.ReadFile(xdgConfigPath("pinned.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := splitLines(string(data))
	if len(lines) != 2 {
		t.Errorf("expected 2 lines (unpinned excluded), got %d: %v", len(lines), lines)
	}
	sort.Strings(lines)
	if lines[0] != "work-1" || lines[1] != "work-2" {
		t.Errorf("lines = %v, want [work-1 work-2]", lines)
	}

	// Reload and verify.
	loaded := loadPinned()
	if !loaded["work-1"] || !loaded["work-2"] {
		t.Errorf("loaded = %v, want both work-1 and work-2 set", loaded)
	}
	if loaded["unpinned"] {
		t.Error("unpinned should not be set in loaded map")
	}
}

func TestSavePinned_EmptyMap(t *testing.T) {
	defer withXDGConfigDir(t)()
	if err := savePinned(map[string]bool{}); err != nil {
		t.Fatalf("savePinned: %v", err)
	}
	loaded := loadPinned()
	if len(loaded) != 0 {
		t.Errorf("after save+load of empty: %v, want empty", loaded)
	}
}

func TestSavePinned_CreatesDir(t *testing.T) {
	defer withXDGConfigDir(t)()
	// The XDG config dir should be created if it doesn't exist.
	parent := filepath.Dir(xdgConfigPath("pinned.txt"))
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Skipf("parent dir already exists: %s", parent)
	}
	if err := savePinned(map[string]bool{"x": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Errorf("savePinned did not create parent dir: %v", err)
	}
}

func TestLoadPinned_SkipsBlankLines(t *testing.T) {
	defer withXDGConfigDir(t)()
	// Write a file with blank lines and whitespace-only lines.
	path := xdgConfigPath("pinned.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "a\n\n   \nb\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded := loadPinned()
	if len(loaded) != 2 || !loaded["a"] || !loaded["b"] {
		t.Errorf("loaded = %v, want {a, b}", loaded)
	}
}
