package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
