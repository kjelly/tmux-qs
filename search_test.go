package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/junegunn/fzf/src/util"
)

func TestPreparedFuzzyParallelEquivalenceAndReuse(t *testing.T) {
	items := benchItems(4000)
	items[0], items[1] = "~/專案/café/MyServer", "~/專案/café/myserver"
	e := newFuzzyEngine()
	for _, query := range []string{"", "server", "MyS", "café server", "專案 café", "missing", "server"} {
		q := prepareFuzzyQuery(query)
		results := e.scoreList(len(items), func(i int) string { return items[i] }, q)
		for i, item := range items {
			score, ok, indices := fuzzyScore(item, query)
			if results[i].score != score || results[i].ok != ok || !slices.Equal(results[i].indices, indices) {
				t.Fatalf("query %q, item %q: %+v vs (%d,%v,%v)", query, item, results[i], score, ok, indices)
			}
		}
	}
	slabs := slices.Clone(e.workers)
	e.scoreList(len(items), func(i int) string { return items[i] }, prepareFuzzyQuery("server"))
	if !reflect.DeepEqual(slabs, e.workers) {
		t.Fatal("worker slabs were replaced")
	}
	// Force the same inputs through the sequential core with its own slab.
	slab := util.MakeSlab(fzfSlab16Size, fzfSlab32Size)
	for i, item := range items {
		score, ok, idx := prepareFuzzyQuery("server").score(e.charsFor(item), slab)
		if result := e.results[i]; result.score != score || result.ok != ok || !slices.Equal(result.indices, idx) {
			t.Fatal("sequential and parallel matching differ")
		}
	}
}

func TestRefilterSubsetTieBreakUsesMatchedItems(t *testing.T) {
	m := performanceModel()
	m.items = []string{"unmatched", "~/long/path/server", "~/server"}
	m.input.SetValue("server")
	m.refilter()
	if !slices.Equal(m.filtered, []int{2, 1}) {
		t.Fatalf("subset tie-break used unmatched rows: %v", m.filtered)
	}
}

func TestPreparedSessionBonusMatchesTerms(t *testing.T) {
	e := newFuzzyEngine()
	for _, row := range []string{"display\tMyProject\t%1", "display\tmyproject\t%2", "display", "display\t\t%3", "專案\t專案\t%4"} {
		for _, query := range []string{"", "my", "My", "my missing", "missing My", "專案"} {
			want := 0
			_, rest, found := strings.Cut(row, "\t")
			session, _, _ := strings.Cut(rest, "\t")
			if found && session != "" {
				for term := range strings.FieldsSeq(query) {
					if fuzzyMatch(session, term) {
						want = sessionNameBonus
						break
					}
				}
			}
			if got := prepareFuzzyQuery(query).sessionBonus(row, e); got != want {
				t.Fatalf("%q/%q bonus=%d, want %d", row, query, got, want)
			}
		}
	}
	if rank, ok := visitRank(nil, "work"); ok || rank != -1 {
		t.Fatal("empty visit rank contract changed")
	}
}

func TestRefilterSortKeysPreserveVisitTwinRanks(t *testing.T) {
	m := performanceModel()
	m.items = []string{"work", "other", "work-eink"}
	m.visitStack = []string{"other-eink", "work-eink", "work"}
	m.sessionInfo = map[string]sessionInfo{"work": {}, "other": {}, "work-eink": {}}
	m.refilter()
	if !slices.Equal(m.filtered, []int{1, 0, 2}) {
		t.Fatalf("twin visit ranking changed: %v", m.filtered)
	}
}

func BenchmarkRefilterLargeCached(b *testing.B) {
	m := performanceModel()
	m.items = benchItems(4000)
	m.input.SetValue("server")
	m.refilter()
	b.ReportAllocs()
	for b.Loop() {
		m.refilter()
	}
}
