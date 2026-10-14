package main

import (
	"cmp"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/junegunn/fzf/src/util"
)

type fuzzyResult struct {
	score   int
	ok      bool
	indices []int
}

// Only the model goroutine populates the character cache. Workers read an
// immutable batch and each owns its scratch slab for the duration of a scan.
func (e *fuzzyEngine) scoreList(n int, text func(int) string, q fuzzyQuery) []fuzzyResult {
	if cap(e.results) < n {
		e.results = make([]fuzzyResult, n)
	}
	e.results = e.results[:n]
	if len(q.terms) == 0 {
		for i := range n {
			e.results[i] = fuzzyResult{ok: true}
		}
		return e.results
	}
	if n < fuzzyParallelThreshold {
		for i := range n {
			score, ok, idx := q.score(e.charsFor(text(i)), e.slab)
			e.results[i] = fuzzyResult{score, ok, idx}
		}
		return e.results
	}
	if cap(e.batch) < n {
		e.batch = make([]util.Chars, n)
	}
	e.batch = e.batch[:n]
	for i := range n {
		e.batch[i] = e.charsFor(text(i))
	}
	workers := min(max(1, runtime.GOMAXPROCS(0)), n)
	for len(e.workers) < workers {
		e.workers = append(e.workers, util.MakeSlab(fzfSlab16Size, fzfSlab32Size))
	}
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for w := range workers {
		lo, hi := w*chunk, min((w+1)*chunk, n)
		if lo >= n {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				score, ok, idx := q.score(e.batch[i], e.workers[w])
				e.results[i] = fuzzyResult{score, ok, idx}
			}
		}()
	}
	wg.Wait()
	return e.results
}

func (q fuzzyQuery) exactBasename(item string) bool {
	if q.exactTerm == "" {
		return false
	}
	base := filepath.Base(strings.TrimSpace(item))
	if q.exactCase {
		return base == q.exactTerm
	}
	return strings.EqualFold(base, q.exactTerm)
}

type entrySortKey struct {
	name          string
	score         int
	basenameExact bool
	tier          int
	group         entryGroupKind
	git           bool
	zoxide        float64
	hasZoxide     bool
	recent        int64
	hasRecent     bool
	recency       int64
	hasRecency    bool
}

func (m *model) entrySortKeys(indices []int, query fuzzyQuery) []entrySortKey {
	ranks := make(map[string]int, len(m.visitStack)*2)
	for i, name := range m.visitStack {
		other := name + "-eink"
		if isEinkSessionName(name) {
			other = strings.TrimSuffix(name, "-eink")
		}
		for _, key := range []string{name, other} {
			if _, seen := ranks[key]; !seen {
				ranks[key] = i
			}
		}
	}
	keys := make([]entrySortKey, m.entryCount())
	for _, i := range indices {
		var name string
		if m.mode == modeBranch {
			name = m.branches[i].name
		} else {
			name = m.items[i]
		}
		name = strings.TrimSpace(name)
		k := entrySortKey{name: name, tier: m.sortTier(name), group: m.entryGroup(name), git: m.isGitEntry(name)}
		k.score = m.fuzzy.results[i].score
		if m.src == srcTmux {
			k.score += query.sessionBonus(name, m.fuzzy)
		}
		k.basenameExact = query.exactBasename(name)
		k.zoxide, k.hasZoxide = m.zoxideScore(name)
		k.recent, k.hasRecent = recentFromRecent(name, m.recentCache)
		if rank, ok := ranks[sessionNameForItem(name)]; ok {
			k.recency, k.hasRecency = visitRankBase-int64(rank), true
		} else {
			k.recency, k.hasRecency = recencyOf(name, m.sessionInfo, m.recentCache, nil)
		}
		keys[i] = k
	}
	return keys
}

func compareRecency(a, b entrySortKey) int {
	if a.hasRecency != b.hasRecency {
		if a.hasRecency {
			return -1
		}
		return 1
	}
	if a.hasRecency {
		return cmp.Compare(b.recency, a.recency)
	}
	return 0
}
