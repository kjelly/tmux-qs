package main

import (
	"os"
	"testing"
	"time"
)

// TestIsGitDirtyCache verifies isGitDirty memoizes within the TTL and
// refreshes once it lapses (driven via the `now` test seam).
func TestIsGitDirtyCache(t *testing.T) {
	dir := t.TempDir()
	if err := run("git", "-C", dir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	base := time.Now()
	now = func() time.Time { return base }
	defer func() { now = time.Now }()

	dirtyCacheMu.Lock()
	delete(dirtyCache, dir)
	dirtyCacheMu.Unlock()

	if isGitDirty(dir) {
		t.Fatal("fresh repo should be clean")
	}
	// Create an untracked file but stay within the TTL window: the
	// cached "clean" result should still be served.
	if err := os.WriteFile(dir+"/f.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isGitDirty(dir) {
		t.Error("within TTL the cached clean result should be reused")
	}
	// Advance past the TTL: the probe should re-run and see the file.
	now = func() time.Time { return base.Add(dirtyCacheTTL + time.Second) }
	if !isGitDirty(dir) {
		t.Error("after TTL lapse the repo should be reported dirty")
	}
}

func TestIsGitDirty(t *testing.T) {
	// 1. Create a temp directory
	dir := t.TempDir()

	// 2. Not a git repository, should return false
	if gitStatusDirty(dir) {
		t.Error("expected non-git directory to be reported as clean")
	}

	// 3. Init git repository
	if err := run("git", "-C", dir, "init"); err != nil {
		t.Fatalf("failed to init git: %v", err)
	}

	// 4. Empty git repo with no files should be clean
	if gitStatusDirty(dir) {
		t.Error("expected fresh git repository with no files to be clean")
	}

	// 5. Create an untracked file
	file := dir + "/temp.txt"
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	// 6. Should be dirty because of the untracked file
	if !gitStatusDirty(dir) {
		t.Error("expected git repository with untracked file to be dirty")
	}

	// 7. Track it (git add)
	if err := run("git", "-C", dir, "add", "temp.txt"); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}

	// 8. Should still be dirty because it has uncommitted staged changes
	if !gitStatusDirty(dir) {
		t.Error("expected git repository with staged uncommitted changes to be dirty")
	}

	// 9. Commit it
	_ = run("git", "-C", dir, "config", "user.name", "test")
	_ = run("git", "-C", dir, "config", "user.email", "test@example.com")
	if err := run("git", "-C", dir, "commit", "-m", "initial commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// 10. Should be clean now
	if gitStatusDirty(dir) {
		t.Error("expected committed repository to be clean")
	}

	// 11. Modify the file
	if err := os.WriteFile(file, []byte("world"), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	// 12. Should be dirty again
	if !gitStatusDirty(dir) {
		t.Error("expected modified repository to be dirty")
	}
}
