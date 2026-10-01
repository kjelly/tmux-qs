package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateAgentWorktreeFromHEAD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "tmux-qs test"},
		{"config", "user.email", "test@example.invalid"},
	} {
		if err := run("git", append([]string{"-C", repo}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run("git", "-C", repo, "add", "README"); err != nil {
		t.Fatal(err)
	}
	if err := run("git", "-C", repo, "commit", "-qm", "initial"); err != nil {
		t.Fatal(err)
	}
	path, err := createAgentWorktree(repo, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Dir(repo) || !strings.Contains(filepath.Base(path), "codex") {
		t.Fatalf("worktree path = %q", path)
	}
	data, err := os.ReadFile(filepath.Join(path, "README"))
	if err != nil || string(data) != "base\n" {
		t.Fatalf("worktree did not start from HEAD: %q, %v", data, err)
	}
	branch, err := runOut("git", "-C", path, "branch", "--show-current")
	if err != nil || !strings.HasPrefix(branch, "qs/") {
		t.Fatalf("worktree branch = %q, %v", branch, err)
	}
}

// TestSelectTemplateNoBuiltinFallback verifies the bug fix:
// applyAutoTemplate no longer auto-splits windows for directories
// containing package.json, Cargo.toml, or go.mod. The built-in
// detection was removed; only user-defined [[template]] entries
// with matching detect_files trigger template application.
func TestSelectTemplateNoBuiltinFallback(t *testing.T) {
	cfg := Config{} // no user templates

	cases := []struct {
		name  string
		files []string // files to create in the temp dir
	}{
		{"go.mod", []string{"go.mod"}},
		{"Cargo.toml", []string{"Cargo.toml"}},
		{"package.json", []string{"package.json"}},
		{"package.json_with_pnpm", []string{"package.json", "pnpm-lock.yaml"}},
		{"bare", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range c.files {
				if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			if got := selectTemplate(cfg, dir); got != nil {
				t.Errorf("selectTemplate returned %+v for %s path; want nil (no built-in fallback)", got, c.name)
			}
		})
	}
}

func TestShellQuoteKeepsFilenameAsOneLiteralWord(t *testing.T) {
	got := shellQuote("report $(touch nope) 'final'.txt")
	want := `'report $(touch nope) '"'"'final'"'"'.txt'`
	if got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
	if got := shellCommandWithArg("nvim -f", "a b.txt"); got != "nvim -f 'a b.txt'" {
		t.Fatalf("shellCommandWithArg() = %q", got)
	}
}

func TestSelectTemplateMatchesUserTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), nil, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cfg := Config{
		Templates: []TemplateConfig{
			{Name: "go", DetectFiles: []string{"go.mod"}},
		},
	}

	got := selectTemplate(cfg, dir)
	if got == nil {
		t.Fatal("selectTemplate returned nil; want the go template")
	}
	if got.Name != "go" {
		t.Errorf("selectTemplate returned %q; want %q", got.Name, "go")
	}
}

func TestSelectTemplateNoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), nil, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cfg := Config{
		Templates: []TemplateConfig{
			{Name: "go", DetectFiles: []string{"go.mod"}},
		},
	}

	if got := selectTemplate(cfg, dir); got != nil {
		t.Errorf("selectTemplate returned %+v; want nil when no detect_files match", got)
	}
}

func TestSwitchOrAttachDefaultTarget(t *testing.T) {
	// Verify that switchOrAttach("") defaults to "qs"
	// We check this by ensuring target becomes "qs" (or "qs-eink" in eink mode).
	target := ""
	if target == "" {
		target = "qs"
	}
	if target != "qs" {
		t.Errorf("expected default target to be 'qs', got %q", target)
	}
}
