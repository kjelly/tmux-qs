package main

import (
	"fmt"
	"reflect"
	"testing"
)

// benchItems builds a realistic-ish list of path-like entries.
func benchItems(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("~/github/project-%d/cmd/server", i)
	}
	return out
}

// BenchmarkFuzzyFreeFunc scores the whole list every iteration using the
// standalone fuzzyScore (fresh ToChars + nil slab each call) — the old hot
// path.
func BenchmarkFuzzyFreeFunc(b *testing.B) {
	items := benchItems(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for _, s := range items {
			fuzzyScore(s, "server")
		}
	}
}

// BenchmarkFuzzyEngine scores the same list every iteration through the
// reusable engine (cached Chars + shared slab) — the new hot path. The
// per-iteration re-scan models repeated keystrokes over the same items.
func BenchmarkFuzzyEngine(b *testing.B) {
	items := benchItems(1000)
	e := newFuzzyEngine()
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for _, s := range items {
			e.score(s, "server")
		}
	}
}

func TestDedupeSortedInts(t *testing.T) {
	got := dedupeSortedInts([]int{0, 0, 1, 2, 2, 2, 5})
	want := []int{0, 1, 2, 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedupeSortedInts = %v, want %v", got, want)
	}
}

// TestFuzzyEngineMatchesFreeFunc verifies the cached engine produces the
// same (score, ok) as the standalone fuzzyScore for the same inputs.
func TestFuzzyEngineMatchesFreeFunc(t *testing.T) {
	e := newFuzzyEngine()
	cases := []struct{ s, q string }{
		{"~/github/sesh", "sesh"},
		{"tmux-qs", "qs"},
		{"FooBar", "foo"},  // lowercase query, case-insensitive
		{"FooBar", "Foo"},  // uppercase query, case-sensitive (matches)
		{"foobar", "Foo"},  // uppercase query, case-sensitive (no match)
		{"a/b/c", "x"},     // no match
		{"dir/proj", "dp"}, // multi-boundary
	}
	for _, c := range cases {
		s1, ok1, _ := fuzzyScore(c.s, c.q)
		// Call twice to exercise the Chars cache hit path.
		_, _, _ = e.score(c.s, c.q)
		s2, ok2, _ := e.score(c.s, c.q)
		if ok1 != ok2 || s1 != s2 {
			t.Errorf("%q/%q: engine=(%d,%v) free=(%d,%v)", c.s, c.q, s2, ok2, s1, ok1)
		}
	}
}

// TestSmartCase verifies the smart-case rule end to end.
func TestSmartCase(t *testing.T) {
	// Lowercase query is case-insensitive.
	if _, ok, _ := fuzzyScore("MyProject", "myproject"); !ok {
		t.Error("lowercase query should match mixed-case text")
	}
	// Uppercase in query makes it case-sensitive.
	if _, ok, _ := fuzzyScore("myproject", "MyProject"); ok {
		t.Error("uppercase query should NOT match all-lowercase text")
	}
	if _, ok, _ := fuzzyScore("MyProject", "MyP"); !ok {
		t.Error("uppercase query should match when case agrees")
	}
}

// TestRefilterParallelEquivalence drives refilter with enough items to
// trigger the parallel scoring path and checks the matched set is exactly
// the entries that contain the query subsequence.
func TestRefilterParallelEquivalence(t *testing.T) {
	if fuzzyParallelThreshold > 4000 {
		t.Skip("threshold too high for this test size")
	}
	m := newModel()
	m.currentSession = ""
	m.currentPath = ""
	m.src = srcTmux

	const total = 4000 // comfortably above fuzzyParallelThreshold
	items := make([]string, total)
	for i := 0; i < total; i++ {
		if i%2 == 0 {
			items[i] = fmt.Sprintf("proj-qs-%d", i) // contains "qs"
		} else {
			items[i] = fmt.Sprintf("other-%d", i) // does not
		}
	}
	m.items = items
	m.input.SetValue("qs")
	m.refilter()

	got := make(map[string]bool, len(m.filtered))
	for _, fi := range m.filtered {
		got[m.items[fi]] = true
	}
	if len(got) != total/2 {
		t.Fatalf("expected %d matches, got %d", total/2, len(got))
	}
	for i, it := range items {
		if want := i%2 == 0; got[it] != want {
			t.Fatalf("item %q matched=%v want %v", it, got[it], want)
		}
	}
}
