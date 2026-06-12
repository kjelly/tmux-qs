package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// branchOfDir returns the current git branch of dir without spawning a
// process: it reads .git/HEAD directly (following "gitdir:" indirection for
// worktrees/submodules). Walks up parent directories so subdirectories of a
// repo report the enclosing repo's branch. Returns "" outside a repository.
func branchOfDir(dir string) string {
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	gitDir := gitPath
	if !info.IsDir() {
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return ""
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(line, "gitdir:") {
			return ""
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(head))
	if strings.HasPrefix(ref, "ref: refs/heads/") {
		return strings.TrimPrefix(ref, "ref: refs/heads/")
	}
	if len(ref) >= 7 {
		return ref[:7] // detached HEAD
	}
	return ""
}

// resolveBranches resolves git branches for every entry that maps to a
// directory — path entries directly, session-name entries via the
// pre-resolved sessionPaths map. Entries without a directory are
// skipped. Returns a map of raw entry -> branch name.
//
// sessionPaths may be nil for callers that don't have an existing
// snapshot (e.g. tests); in that case session-name entries fall back
// to a fresh tmuxSessionPaths() call. The intent is that hot paths
// (e.g. the itemsMsg handler) build the map once and share it.
func resolveBranches(entries []string, sessionPaths map[string]string) map[string]string {
	if sessionPaths == nil {
		sessionPaths = tmuxSessionPaths()
	}
	result := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, e := range entries {
		dir := entryDir(e, sessionPaths)
		if dir == "" {
			continue
		}
		wg.Add(1)
		go func(raw, dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if b := branchOfDir(dir); b != "" {
				mu.Lock()
				result[raw] = b
				mu.Unlock()
			}
		}(e, dir)
	}
	wg.Wait()
	return result
}

// entryDir resolves a list entry to a directory: a path entry expands
// directly; a session-name entry resolves to that tmux session's working
// directory. Returns "" when the entry has no corresponding directory.
func entryDir(entry string, sessions map[string]string) string {
	entry = strings.TrimSpace(entry)
	if looksLikePath(entry) {
		return expandPath(entry)
	}
	return sessions[entry]
}

// resolveDirty returns the set of entries whose working tree is
// dirty. Used by the TUI as a follow-up pass to annotateCmd so the
// initial render is not blocked on `git status`. Only entries that
// are actually dirty are included in the result map (clean ones are
// simply absent).
func resolveDirty(entries []string, sessionPaths map[string]string) map[string]bool {
	if sessionPaths == nil {
		sessionPaths = tmuxSessionPaths()
	}
	out := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, e := range entries {
		dir := entryDir(e, sessionPaths)
		if dir == "" {
			continue
		}
		wg.Add(1)
		go func(raw, dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if isGitDirty(dir) {
				mu.Lock()
				out[raw] = true
				mu.Unlock()
			}
		}(e, dir)
	}
	wg.Wait()
	return out
}


type branchEntry struct {
	name         string
	current      bool
	worktreePath string // non-empty if the branch is checked out in a worktree
	remote       bool   // remote-only branch (no local counterpart yet)
}

// listBranches lists local branches, branches checked out in worktrees, and
// remote-only branches (with the remote prefix stripped) of the repo.
func listBranches(repo string) ([]branchEntry, error) {
	locals, err := runLines("git", "-C", repo, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	current := branchOfDir(repo)
	worktrees := worktreeBranches(repo)
	seen := make(map[string]bool)
	var out []branchEntry
	for _, b := range locals {
		seen[b] = true
		out = append(out, branchEntry{
			name:         b,
			current:      b == current,
			worktreePath: worktrees[b],
		})
	}
	remotes, _ := runLines("git", "-C", repo, "for-each-ref", "--format=%(refname:short)", "refs/remotes")
	for _, r := range remotes {
		parts := strings.SplitN(r, "/", 2)
		if len(parts) != 2 || parts[1] == "HEAD" {
			continue
		}
		name := parts[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, branchEntry{name: name, remote: true})
	}
	return out, nil
}

// worktreeBranches maps branch name -> worktree path for the repo.
func worktreeBranches(repo string) map[string]string {
	out := make(map[string]string)
	lines, err := runLines("git", "-C", repo, "worktree", "list", "--porcelain")
	if err != nil {
		return out
	}
	var path string
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "worktree "):
			path = strings.TrimPrefix(l, "worktree ")
		case strings.HasPrefix(l, "branch refs/heads/"):
			out[strings.TrimPrefix(l, "branch refs/heads/")] = path
		}
	}
	return out
}

// switchBranch checks out the branch in repo. `git switch` auto-creates a
// local tracking branch when the name matches exactly one remote branch.
// Fails (without touching anything) when the working tree has conflicting
// local changes.
func switchBranch(repo, branch string) error {
	return run("git", "-C", repo, "switch", branch)
}

// isGitDirty reports whether the git repository at dir has any unstaged,
// staged, or untracked changes.
func isGitDirty(dir string) bool {
	out, err := runOut("git", "-C", dir, "status", "--porcelain")
	if err != nil {
		return false
	}
	return out != ""
}
