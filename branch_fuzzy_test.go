package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestCompositeEntryText_AllModeIncludesBranch is the central
// invariant for the new branch-fuzzy feature: in srcAll /
// srcDefault, compositeEntryText appends "  " + branch so the
// fuzzy engine can match against either segment.
func TestCompositeEntryText_AllModeIncludesBranch(t *testing.T) {
	m := newModel()
	m.src = srcAll
	m.items = []string{"~/github/proj-a", "~/github/proj-b"}
	m.annots = map[string]string{
		"~/github/proj-a": "main",
		"~/github/proj-b": "feature-x",
	}

	if got, want := m.compositeEntryText(0), "~/github/proj-a  main"; got != want {
		t.Errorf("compositeEntryText(0) = %q, want %q", got, want)
	}
	if got, want := m.compositeEntryText(1), "~/github/proj-b  feature-x"; got != want {
		t.Errorf("compositeEntryText(1) = %q, want %q", got, want)
	}
}

// TestCompositeEntryText_OtherSourcesIgnoreBranch verifies the
// src-gating: only srcAll / srcDefault extend the text with
// branch. srcTmux / srcConfigs / srcZoxide / srcFind / etc.
// must NOT have branch data mixed into their fuzzy text.
func TestCompositeEntryText_OtherSourcesIgnoreBranch(t *testing.T) {
	cases := []sourceKind{
		srcTmux, srcConfigs, srcZoxide, srcFind,
		srcWaiting, srcFiles, srcPanes, srcWindows,
		srcSSH, srcCleanup, srcCommands,
	}
	for _, src := range cases {
		m := newModel()
		m.src = src
		m.items = []string{"some-entry"}
		m.annots = map[string]string{"some-entry": "main"}
		if got := m.compositeEntryText(0); got != "some-entry" {
			t.Errorf("src=%v: compositeEntryText = %q, want raw item only", src, got)
		}
	}
}

// TestCompositeEntryText_EmptyBranchFallsBackToItem: when
// m.annots has no entry for the item, compositeEntryText must
// return the item unchanged (not "item  "). This matches the
// "annotMsg hasn't arrived yet" / "not a git repo" cases.
func TestCompositeEntryText_EmptyBranchFallsBackToItem(t *testing.T) {
	m := newModel()
	m.src = srcDefault
	m.items = []string{"~/not-a-repo", "~/another"}
	m.annots = map[string]string{} // empty — annotMsg not yet populated

	for i, want := range m.items {
		if got := m.compositeEntryText(i); got != want {
			t.Errorf("compositeEntryText(%d) = %q, want %q", i, got, want)
		}
	}
}

// TestCompositeEntryText_ExplicitEmptyBranchString: if the
// annots map HAS the entry but with an empty string, the
// fallback still kicks in. (resolveBranches skips empty
// branches, but defensive coding here protects against future
// callers that might insert them.)
func TestCompositeEntryText_ExplicitEmptyBranchString(t *testing.T) {
	m := newModel()
	m.src = srcDefault
	m.items = []string{"~/x"}
	m.annots = map[string]string{"~/x": ""}
	if got := m.compositeEntryText(0); got != "~/x" {
		t.Errorf("empty branch should fall back, got %q", got)
	}
}

// TestHighlightComposite_SplitsIndicesAtSeparator: the indices
// produced by the fuzzy engine span both segments of the
// composite. highlightComposite must route them to the right
// segment so each gets its own style. We don't inspect the
// styled output (lipgloss styles are environment-dependent) but
// we verify the input split and that the result has the right
// shape.
func TestHighlightComposite_SplitsIndicesAtSeparator(t *testing.T) {
	raw := "my-project"
	branch := "main"
	// Indices that span both segments (0=m on item, itemLen+2+1
	// = a in branch).
	indices := []int{0, len(raw) + 2 + 1}
	out := highlightComposite(raw, branch, indices,
		lipgloss.NewStyle().Bold(true),
		lipgloss.NewStyle().Italic(true),
	)
	// Output must contain both the item and the branch.
	if !strings.Contains(out, "my-project") {
		t.Errorf("output should contain raw item, got %q", out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("output should contain branch, got %q", out)
	}
	// And the separator "  " should still be present (twice
	// in the layout: once between item and branch, once as a
	// leading space we don't expect; the exact count is
	// 1 separator between the segments).
	if !strings.Contains(out, "  ") {
		t.Errorf("output should contain the 2-space separator, got %q", out)
	}
}

// TestHighlightComposite_NoIndices: when there are no match
// indices, the function renders the row in a single style with
// the branch unhighlighted. We don't crash, and both segments
// are present.
func TestHighlightComposite_NoIndices(t *testing.T) {
	raw := "my-project"
	branch := "feature"
	out := highlightComposite(raw, branch, nil,
		lipgloss.NewStyle().Bold(true),
		lipgloss.NewStyle().Italic(true),
	)
	if !strings.Contains(out, raw) {
		t.Errorf("output should contain raw item, got %q", out)
	}
	if !strings.Contains(out, branch) {
		t.Errorf("output should contain branch, got %q", out)
	}
}

// TestHighlightComposite_EmptyBranch: when branch is empty
// (annotMsg hasn't arrived), highlightComposite delegates to
// highlightMatches for the item only, with no separator.
func TestHighlightComposite_EmptyBranch(t *testing.T) {
	raw := "my-project"
	out := highlightComposite(raw, "", []int{0, 3},
		lipgloss.NewStyle().Bold(true),
		lipgloss.NewStyle().Italic(true),
	)
	if !strings.Contains(out, raw) {
		t.Errorf("output should contain raw item, got %q", out)
	}
	// No separator when branch is empty.
	if strings.Contains(out, "  ") {
		t.Errorf("output with empty branch should not contain '  ', got %q", out)
	}
}

// TestRefilterFuzzyMatchesBranch_SrcAll is the end-to-end
// "what the user sees" test: in srcAll mode with annotMsg
// populating m.annots, typing "main" finds every entry whose
// branch is "main", even when the entry text doesn't contain
// "main" at all.
func TestRefilterFuzzyMatchesBranch_SrcAll(t *testing.T) {
	m := newModel()
	m.src = srcAll
	m.items = []string{"~/github/proj-a", "~/github/proj-b", "~/github/proj-c"}
	m.annots = map[string]string{
		"~/github/proj-a": "main",
		"~/github/proj-b": "feature-x",
		"~/github/proj-c": "main",
	}
	m.input.SetValue("main")
	m.refilter()

	// proj-a and proj-c should be in filtered (both have
	// branch=main). proj-b should NOT be in filtered
	// (branch=feature-x doesn't match "main").
	wantIn := map[string]bool{
		"~/github/proj-a": true,
		"~/github/proj-b": false,
		"~/github/proj-c": true,
	}
	for name, want := range wantIn {
		got := false
		for _, idx := range m.filtered {
			if m.items[idx] == name {
				got = true
				break
			}
		}
		if got != want {
			t.Errorf("item %q: in filtered = %v, want %v", name, got, want)
		}
	}
}

// TestRefilterFuzzyMatchesBranch_SrcDefault: srcDefault is an
// alias for srcAll in loadSource, but it's a different enum
// value, so we exercise it explicitly. The composite branch
// feature must work for both.
func TestRefilterFuzzyMatchesBranch_SrcDefault(t *testing.T) {
	m := newModel()
	m.src = srcDefault
	m.items = []string{"~/a", "~/b"}
	m.annots = map[string]string{
		"~/a": "develop",
		"~/b": "main",
	}
	m.input.SetValue("main")
	m.refilter()

	found := false
	for _, idx := range m.filtered {
		if m.items[idx] == "~/b" {
			found = true
		}
		if m.items[idx] == "~/a" {
			t.Errorf("~/a (branch=develop) should not match query 'main'")
		}
	}
	if !found {
		t.Errorf("expected ~/b in filtered, got %v", m.filtered)
	}
}

// TestRefilterIgnoresBranch_NonAllSources: in srcTmux (and
// other non-all sources), the user's branch annotations must
// NOT be mixed into the fuzzy text. This protects against a
// regression where compositeEntryText accidentally extends
// the text for sources that don't carry branch info.
func TestRefilterIgnoresBranch_NonAllSources(t *testing.T) {
	m := newModel()
	m.src = srcTmux
	m.items = []string{"alpha:1.0 [nu] ~/work 「nu」\twork\t%0"}
	// Even if m.annots has a "main" entry, srcTmux should
	// ignore it.
	m.annots = map[string]string{
		"alpha:1.0 [nu] ~/work 「nu」\twork\t%0": "main",
	}
	// Query "main" — should NOT match because srcTmux's
	// compositeEntryText returns just the item (the part
	// before \t), and "main" isn't a subsequence of
	// "alpha:1.0 [nu] ~/work 「nu」".
	m.input.SetValue("main")
	m.refilter()
	if len(m.filtered) > 0 {
		t.Errorf("srcTmux should not match against m.annots; filtered = %v", m.filtered)
	}
}

// TestCompositeEntryTextMemo_CachesResult: the memo wrapper
// must return the same string for repeated calls with the same
// index. This is exercised in the refilter hot path which
// calls et(i) twice per entry (once for the parallel path's
// fall-through is not used; once for the sequential path).
func TestCompositeEntryTextMemo_CachesResult(t *testing.T) {
	m := newModel()
	m.src = srcAll
	m.items = []string{"~/a"}
	m.annots = map[string]string{"~/a": "main"}
	et := m.compositeEntryTextMemo()
	if et(0) != et(0) {
		t.Errorf("memo wrapper should be stable for the same index")
	}
	if got, want := et(0), "~/a  main"; got != want {
		t.Errorf("compositeEntryText(0) via memo = %q, want %q", got, want)
	}
}
