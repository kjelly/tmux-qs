package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	gitPreviewStatusTTL  = 4 * time.Second
	gitPreviewLogTTL     = 30 * time.Second
	gitPreviewCacheLimit = 64
)

type gitPreviewPart struct {
	lines []string
	err   error
	at    time.Time
	valid bool
}

type gitPreviewRepository struct {
	mu          sync.Mutex
	marker      os.FileInfo
	head        string
	status, log gitPreviewPart
	used        time.Time // protected by the cache mutex
}

// This cache belongs to one picker. Per-repository locks coalesce concurrent
// refreshes without making a slow Git command block previews of other repos.
type gitPreviewCache struct {
	mu    sync.Mutex
	repos map[string]*gitPreviewRepository
	clock func() time.Time
	run   func(string, ...string) ([]string, error)
}

func newGitPreviewCache() *gitPreviewCache {
	return &gitPreviewCache{repos: make(map[string]*gitPreviewRepository), clock: time.Now, run: runLines}
}

func (c *gitPreviewCache) repository(root string) *gitPreviewRepository {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.repos[root]
	if !ok {
		if len(c.repos) >= gitPreviewCacheLimit {
			var oldest string
			for key, entry := range c.repos {
				if oldest == "" || entry.used.Before(c.repos[oldest].used) {
					oldest = key
				}
			}
			delete(c.repos, oldest)
		}
		r = &gitPreviewRepository{}
		c.repos[root] = r
	}
	r.used = c.clock()
	return r
}

// Include the worktree's own HEAD and its resolved ref. A commondir points
// at shared refs, while each linked worktree still has its own HEAD/index.
func gitPreviewHead(root string, marker os.FileInfo) (string, error) {
	gitDir := filepath.Join(root, ".git")
	if !marker.IsDir() {
		data, err := os.ReadFile(gitDir)
		if err != nil {
			return "", err
		}
		path, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return "", fmt.Errorf("invalid gitdir")
		}
		gitDir = strings.TrimSpace(path)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", err
	}
	token := gitDir + "\x00" + string(head)
	if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: "); ok {
		commonDir := gitDir
		if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
			commonDir = strings.TrimSpace(string(data))
			if !filepath.IsAbs(commonDir) {
				commonDir = filepath.Join(gitDir, commonDir)
			}
		}
		if data, err := os.ReadFile(filepath.Join(commonDir, ref)); err == nil {
			token += "\x00" + string(data)
		} else if st, err := os.Stat(filepath.Join(commonDir, "packed-refs")); err == nil {
			token += fmt.Sprintf("\x00%d:%d", st.ModTime().UnixNano(), st.Size())
		}
	}
	return token, nil
}

func (c *gitPreviewCache) load(root string, cfg PreviewConfig) (gitPreviewPart, gitPreviewPart) {
	r := c.repository(root)
	r.mu.Lock()
	defer r.mu.Unlock()
	marker, err := os.Stat(filepath.Join(root, ".git"))
	if err != nil {
		return gitPreviewPart{err: err}, gitPreviewPart{err: err}
	}
	head, headErr := gitPreviewHead(root, marker)
	if r.marker == nil || !os.SameFile(r.marker, marker) || r.head != head || headErr != nil {
		r.status, r.log = gitPreviewPart{}, gitPreviewPart{}
	}
	r.marker, r.head = marker, head
	now := c.clock()
	var wg sync.WaitGroup
	if cfg.ShowGitStatus() && (!r.status.valid || now.Sub(r.status.at) >= gitPreviewStatusTTL) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines, err := c.run("git", "-C", root, "status", "-s")
			// The panel shows five rows plus an overflow indicator. Avoid
			// retaining a large status buffer for every cached repository.
			lines = slices.Clone(lines[:min(6, len(lines))])
			r.status = gitPreviewPart{lines: lines, err: err, valid: err == nil, at: c.clock()}
		}()
	}
	if cfg.ShowGitLog() && (!r.log.valid || now.Sub(r.log.at) >= gitPreviewLogTTL) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines, err := c.run("git", "-C", root, "log", "-n", "5", "--oneline")
			r.log = gitPreviewPart{lines: lines, err: err, valid: err == nil, at: c.clock()}
		}()
	}
	wg.Wait()
	return r.status, r.log
}

func appendCachedGitPreview(lines []string, dir string, cfg PreviewConfig, cache *gitPreviewCache) []string {
	if cache == nil {
		return appendGitPreview(lines, dir, cfg)
	}
	if dir == "" || (!cfg.ShowGitStatus() && !cfg.ShowGitLog()) {
		return lines
	}
	root := repoRoot(dir)
	if root == "" {
		return lines
	}
	status, log := cache.load(root, cfg)
	return renderGitPreview(lines, cfg, status.lines, status.err, log.lines, log.err)
}
