package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func previewTestRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{"HEAD": "ref: refs/heads/main\n", "refs/heads/main": "commit-one\n"} {
		if err := os.WriteFile(filepath.Join(gitDir, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGitPreviewCacheTTLHeadAndSettings(t *testing.T) {
	root := previewTestRepository(t)
	c := newGitPreviewCache()
	var statuses, logs atomic.Int32
	clock := time.Now()
	c.clock = func() time.Time { return clock }
	c.run = func(_ string, args ...string) ([]string, error) {
		if args[2] == "status" {
			statuses.Add(1)
			return []string{" M file"}, nil
		}
		logs.Add(1)
		return []string{"commit message"}, nil
	}
	for range 3 {
		appendCachedGitPreview(nil, root, PreviewConfig{}, c)
	}
	if statuses.Load() != 1 || logs.Load() != 1 {
		t.Fatal("pane refresh repeated Git queries")
	}
	clock = clock.Add(gitPreviewStatusTTL)
	appendCachedGitPreview(nil, root, PreviewConfig{}, c)
	if statuses.Load() != 2 || logs.Load() != 1 {
		t.Fatal("status/log expiries were coupled")
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "refs", "heads", "main"), []byte("commit-two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	appendCachedGitPreview(nil, root, PreviewConfig{}, c)
	if statuses.Load() != 3 || logs.Load() != 2 {
		t.Fatal("commit on same branch did not invalidate cache")
	}
	no := false
	if out := appendCachedGitPreview([]string{"Pane output"}, root, PreviewConfig{GitStatus: &no, GitLog: &no}, c); len(out) != 1 {
		t.Fatal("disabled Git details remained visible")
	}
	clock = clock.Add(gitPreviewLogTTL)
	appendCachedGitPreview(nil, root, PreviewConfig{GitStatus: &no}, c)
	if statuses.Load() != 3 || logs.Load() != 3 {
		t.Fatal("disabled status was queried")
	}
	appendCachedGitPreview(nil, root, PreviewConfig{}, c)
	if statuses.Load() != 4 || logs.Load() != 3 {
		t.Fatal("re-enabled status was not refreshed")
	}
}

func TestGitPreviewCacheErrorRetryAndCoalescing(t *testing.T) {
	root := previewTestRepository(t)
	c := newGitPreviewCache()
	var statuses, logs atomic.Int32
	c.run = func(_ string, args ...string) ([]string, error) {
		if args[2] == "status" {
			if statuses.Add(1) == 1 {
				return nil, errors.New("temporary failure")
			}
			return []string{" M recovered"}, nil
		}
		logs.Add(1)
		return []string{"commit message"}, nil
	}
	out := strings.Join(appendCachedGitPreview(nil, root, PreviewConfig{}, c), "\n")
	if !strings.Contains(out, "unavailable: temporary failure") {
		t.Fatal("query failure was hidden")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); appendCachedGitPreview(nil, root, PreviewConfig{}, c) }()
	}
	wg.Wait()
	if statuses.Load() != 2 || logs.Load() != 1 {
		t.Fatal("concurrent refreshes repeated successful queries")
	}
	out = strings.Join(appendCachedGitPreview(nil, root, PreviewConfig{}, c), "\n")
	if strings.Contains(out, "unavailable") || !strings.Contains(out, "recovered") {
		t.Fatal("error survived successful retry")
	}
}

func TestGitPreviewCacheRetainsOnlyDisplayedStatus(t *testing.T) {
	root := previewTestRepository(t)
	c := newGitPreviewCache()
	c.run = func(_ string, args ...string) ([]string, error) {
		if args[2] != "status" {
			return nil, nil
		}
		lines := make([]string, 1000)
		for i := range lines {
			lines[i] = fmt.Sprintf(" M file-%d", i)
		}
		return lines, nil
	}
	out := strings.Join(appendCachedGitPreview(nil, root, PreviewConfig{}, c), "\n")
	if !strings.Contains(out, "file-4") || !strings.Contains(out, "...") || strings.Contains(out, "file-5") {
		t.Fatal("bounded cache changed status summary")
	}
	status, _ := c.load(root, PreviewConfig{})
	if len(status.lines) != 6 {
		t.Fatal("cache retained hidden status rows")
	}
}

func TestGitPreviewCacheWorktreeIdentity(t *testing.T) {
	root := previewTestRepository(t)
	worktree := t.TempDir()
	gitDir := filepath.Join(root, ".git", "worktrees", "second")
	if err := os.MkdirAll(gitDir, 0700); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{
		filepath.Join(worktree, ".git"):    "gitdir: " + gitDir + "\n",
		filepath.Join(gitDir, "HEAD"):      "ref: refs/heads/main\n",
		filepath.Join(gitDir, "commondir"): "../..\n",
	} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := newGitPreviewCache()
	var calls atomic.Int32
	c.run = func(_ string, args ...string) ([]string, error) { calls.Add(1); return []string{args[1]}, nil }
	for _, dir := range []string{root, worktree, root, worktree} {
		appendCachedGitPreview(nil, dir, PreviewConfig{}, c)
	}
	if calls.Load() != 4 {
		t.Fatal("worktree caches were mixed or not reused")
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "refs", "heads", "main"), []byte("new-commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := appendCachedGitPreview(nil, worktree, PreviewConfig{}, c)
	if calls.Load() != 6 || !strings.Contains(strings.Join(out, "\n"), worktree) {
		t.Fatal("shared ref change did not refresh worktree")
	}
}

func TestGitPreviewCacheBoundAndRecreatedRepository(t *testing.T) {
	c := newGitPreviewCache()
	for i := range gitPreviewCacheLimit + 10 {
		c.repository(fmt.Sprint(i))
	}
	if len(c.repos) != gitPreviewCacheLimit {
		t.Fatal("cache retained unlimited repositories")
	}
	root := previewTestRepository(t)
	var calls atomic.Int32
	c.run = func(_ string, _ ...string) ([]string, error) { calls.Add(1); return nil, nil }
	appendCachedGitPreview(nil, root, PreviewConfig{}, c)
	old := filepath.Join(root, ".git")
	if err := os.Rename(old, old+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "HEAD"), []byte("detached-head\n"), 0600); err != nil {
		t.Fatal(err)
	}
	appendCachedGitPreview(nil, root, PreviewConfig{}, c)
	if calls.Load() != 4 {
		t.Fatal("recreated repository reused old Git data")
	}
}
