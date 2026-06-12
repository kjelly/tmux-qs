package main

import (
	"os"
	"testing"
)

func TestIsGitDirty(t *testing.T) {
	// 1. Create a temp directory
	dir := t.TempDir()

	// 2. Not a git repository, should return false
	if isGitDirty(dir) {
		t.Error("expected non-git directory to be reported as clean")
	}

	// 3. Init git repository
	if err := run("git", "-C", dir, "init"); err != nil {
		t.Fatalf("failed to init git: %v", err)
	}

	// 4. Empty git repo with no files should be clean
	if isGitDirty(dir) {
		t.Error("expected fresh git repository with no files to be clean")
	}

	// 5. Create an untracked file
	file := dir + "/temp.txt"
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	// 6. Should be dirty because of the untracked file
	if !isGitDirty(dir) {
		t.Error("expected git repository with untracked file to be dirty")
	}

	// 7. Track it (git add)
	if err := run("git", "-C", dir, "add", "temp.txt"); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}

	// 8. Should still be dirty because it has uncommitted staged changes
	if !isGitDirty(dir) {
		t.Error("expected git repository with staged uncommitted changes to be dirty")
	}

	// 9. Commit it
	_ = run("git", "-C", dir, "config", "user.name", "test")
	_ = run("git", "-C", dir, "config", "user.email", "test@example.com")
	if err := run("git", "-C", dir, "commit", "-m", "initial commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// 10. Should be clean now
	if isGitDirty(dir) {
		t.Error("expected committed repository to be clean")
	}

	// 11. Modify the file
	if err := os.WriteFile(file, []byte("world"), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	// 12. Should be dirty again
	if !isGitDirty(dir) {
		t.Error("expected modified repository to be dirty")
	}
}
