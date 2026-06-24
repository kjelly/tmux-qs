package main

import (
	"strings"
	"testing"
)

func TestDeriveSessionName_NoCollision(t *testing.T) {
	existing := map[string]bool{"bar": true}
	got := deriveSessionName("/home/u/work/foo", existing, "parent-basename")
	if got != "foo" {
		t.Errorf("deriveSessionName = %q; want %q (no collision → basename)", got, "foo")
	}
}

func TestDeriveSessionName_ParentBasenameOnCollision(t *testing.T) {
	existing := map[string]bool{"foo": true}
	got := deriveSessionName("/home/u/work/foo", existing, "parent-basename")
	if got != "work-foo" {
		t.Errorf("deriveSessionName = %q; want %q", got, "work-foo")
	}
}

func TestDeriveSessionName_ParentBasenameStillCollides(t *testing.T) {
	// parent-prefix also collides → fall back to numeric suffix.
	existing := map[string]bool{"foo": true, "work-foo": true}
	got := deriveSessionName("/home/u/work/foo", existing, "parent-basename")
	if got != "foo-1" {
		t.Errorf("deriveSessionName = %q; want %q (fall back to suffix)", got, "foo-1")
	}
}

func TestDeriveSessionName_FallbackToNumericSuffix(t *testing.T) {
	// /foo collides; parent prefix "-" makes no sense; fall back to "-1".
	existing := map[string]bool{"foo": true}
	got := deriveSessionName("/foo", existing, "parent-basename")
	if got != "foo-1" {
		t.Errorf("deriveSessionName = %q; want %q", got, "foo-1")
	}
}

func TestDeriveSessionName_StrategySuffix(t *testing.T) {
	existing := map[string]bool{"foo": true}
	got := deriveSessionName("/home/u/work/foo", existing, "suffix")
	if got != "foo-1" {
		t.Errorf("deriveSessionName = %q; want %q (suffix strategy)", got, "foo-1")
	}
}

func TestDeriveSessionName_StrategyHash6(t *testing.T) {
	existing := map[string]bool{"foo": true}
	got := deriveSessionName("/home/u/work/foo", existing, "hash6")
	if !strings.HasPrefix(got, "foo-") {
		t.Fatalf("deriveSessionName = %q; want foo-<hash>", got)
	}
	suffix := strings.TrimPrefix(got, "foo-")
	if len(suffix) != 6 {
		t.Errorf("hash suffix = %q (len %d); want 6 chars", suffix, len(suffix))
	}
	// Same path must produce same hash (deterministic).
	got2 := deriveSessionName("/home/u/work/foo", map[string]bool{"foo": true}, "hash6")
	if got != got2 {
		t.Errorf("hash not deterministic: %q vs %q", got, got2)
	}
}

func TestDeriveSessionName_EmptyPath(t *testing.T) {
	got := deriveSessionName("", nil, "parent-basename")
	if got != "" {
		t.Errorf("deriveSessionName(\"\") = %q; want empty", got)
	}
}

func TestDeriveSessionName_DeterministicParentBasename(t *testing.T) {
	// Same path with same existing → same name (call twice).
	existing := map[string]bool{"foo": true}
	a := deriveSessionName("/home/u/work/foo", existing, "parent-basename")
	b := deriveSessionName("/home/u/work/foo", existing, "parent-basename")
	if a != b {
		t.Errorf("not deterministic: %q vs %q", a, b)
	}
}

func TestFindDuplicateBasenames(t *testing.T) {
	// Two sessions rooted in different dirs that both end in "/foo".
	// The numeric-suffix naming scheme gave them session names "foo"
	// and "foo-1"; we want the picker to flag "foo" as a duplicate
	// of cwd-basename "foo" and render a path suffix so the user can
	// tell them apart.
	info := map[string]sessionInfo{
		"foo":   {path: "/home/u/work/foo"},
		"foo-1": {path: "/home/u/personal/foo"},
		"bar":   {path: "/home/u/work/bar"},
	}
	dup := findDuplicateBasenames(info)
	if !dup["foo"] {
		t.Errorf("expected foo (cwd basename) to be flagged as duplicate")
	}
	if dup["bar"] {
		t.Errorf("non-colliding basenames should not be flagged")
	}
}

func TestFindDuplicateBasenames_Empty(t *testing.T) {
	dup := findDuplicateBasenames(map[string]sessionInfo{})
	if len(dup) != 0 {
		t.Errorf("empty input should produce empty map; got %v", dup)
	}
}

func TestFindDuplicateBasenames_OnlyOne(t *testing.T) {
	info := map[string]sessionInfo{
		"foo": {path: "/home/u/work/foo"},
	}
	dup := findDuplicateBasenames(info)
	if len(dup) != 0 {
		t.Errorf("single entry should produce empty map; got %v", dup)
	}
}

// entryDisplayName strips any "\t<path>" hint suffix from an entry
// string. Even though the production code now stores hints in a
// separate map, the helper is exercised here as a public utility.
func TestEntryDisplayName_NoHint(t *testing.T) {
	got := entryDisplayName("foo")
	if got != "foo" {
		t.Errorf("entryDisplayName = %q; want %q", got, "foo")
	}
}

func TestEntryDisplayName_StripsTabPath(t *testing.T) {
	got := entryDisplayName("foo\t/home/u/work/foo")
	if got != "foo" {
		t.Errorf("entryDisplayName = %q; want %q", got, "foo")
	}
}

func TestEntryDisplayName_StripsTabPathFromPathEntry(t *testing.T) {
	// A path entry with a tab in it is unusual but legal in zoxide —
	// we still split on the first tab because that matches the
	// srcPanes convention used throughout the picker.
	got := entryDisplayName("~/projects/foo\t/home/u/projects/foo")
	if got != "~/projects/foo" {
		t.Errorf("entryDisplayName = %q; want %q", got, "~/projects/foo")
	}
}

// splitEntryWithPath splits a "displayName\thintPath" picker entry.
// In the production code base, hints live in a separate map
// (model.entryHintPath), but the helper is exercised here as a
// public utility mirroring the same tab convention used by srcPanes.
func TestSplitEntryWithPath_NoHint(t *testing.T) {
	name, hint := splitEntryWithPath("foo")
	if name != "foo" || hint != "" {
		t.Errorf("got name=%q hint=%q; want name=%q hint=%q", name, hint, "foo", "")
	}
}

func TestSplitEntryWithPath_WithHint(t *testing.T) {
	name, hint := splitEntryWithPath("foo\t/home/u/work/foo")
	if name != "foo" || hint != "/home/u/work/foo" {
		t.Errorf("got name=%q hint=%q; want name=%q hint=%q", name, hint, "foo", "/home/u/work/foo")
	}
}

func TestShowPathWhenDuplicate_Default(t *testing.T) {
	// nil pointer → default (true).
	if !showPathWhenDuplicate(Config{}) {
		t.Errorf("showPathWhenDuplicate(Config{}) = false; want default true")
	}
}

func TestShowPathWhenDuplicate_Explicit(t *testing.T) {
	yes := true
	no := false
	if !showPathWhenDuplicate(Config{Naming: NamingConfig{ShowPathWhenDuplicate: &yes}}) {
		t.Errorf("explicit true should be true")
	}
	if showPathWhenDuplicate(Config{Naming: NamingConfig{ShowPathWhenDuplicate: &no}}) {
		t.Errorf("explicit false should be false")
	}
}

func TestShortDisambiguator(t *testing.T) {
	// /home/u/work/foo → "work/foo"
	got := shortDisambiguator("/home/u/work/foo")
	if got != "work/foo" {
		t.Errorf("shortDisambiguator = %q; want %q", got, "work/foo")
	}
	// Already at root → just the basename
	got = shortDisambiguator("/foo")
	if got != "/foo" {
		t.Errorf("shortDisambiguator(/foo) = %q; want %q", got, "/foo")
	}
}
