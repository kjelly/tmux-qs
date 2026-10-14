package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWatchUnchangedMembershipKeepsListState(t *testing.T) {
	m := performanceModel()
	m.items = benchItems(30)
	m.refilter()
	m.cursor, m.offset, m.pendingKill = 12, 5, "pending-confirmation"
	next, _ := m.Update(watchMsg{info: waitingInfo{version: 2, lastUpdated: time.Now()}})
	m = next.(model)
	if m.offset != 5 || m.pendingKill != "pending-confirmation" || m.cursor != 12 {
		t.Fatal("unchanged watch refiltered the list")
	}
	path := expandPath(m.items[20])
	next, _ = m.Update(watchMsg{info: waitingInfo{byPath: map[string][]procCount{path: {{name: "codex", count: 1}}}}})
	m = next.(model)
	if m.filtered[0] != 20 || m.pendingKill != "" {
		t.Fatal("new waiting member did not reorder")
	}
	m.offset, m.pendingKill = 3, "second-confirmation"
	next, _ = m.Update(watchMsg{info: waitingInfo{byPath: map[string][]procCount{path: {{name: "claude", count: 2}}}}})
	if next.(model).offset != 3 || next.(model).pendingKill != "second-confirmation" {
		t.Fatal("process detail changes needlessly reordered")
	}
	// A modal retains its own items even when workspace waiting order changes.
	m.mode, m.items, m.filtered = modeSnippetSelect, []string{"Continue"}, []int{0}
	next, _ = m.Update(watchMsg{info: waitingInfo{}})
	if !slices.Equal(next.(model).filtered, []int{0}) || !slices.Equal(next.(model).items, []string{"Continue"}) {
		t.Fatal("watcher modified snippet picker")
	}
}

func TestWatchPinnedCaptureScopeKeepsPreviewMetadata(t *testing.T) {
	withTestTmuxServer(t)
	server := getTmuxServer()
	if err := tmuxRun("new-session", "-d", "-s", "scope-agent", "-x", "80", "-y", "24", "cat"); err != nil {
		t.Fatal(err)
	}
	pane, err := tmuxRunOut("display-message", "-p", "-t", "scope-agent", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	opts := WatchingConfig{Commands: []string{"cat"}, PinnedOnly: true, Idle: time.Minute}
	msg := collectWatchMessage(server, paneKey{}, opts, nil, 0, nil, time.Now())
	if msg.err != nil || msg.stats.CaptureTotal != 0 || len(msg.captures) != 0 {
		t.Fatalf("unpinned pane captured: %+v", msg)
	}
	if msg.info.activePanes["scope-agent"] != pane || msg.info.outputSignals[pane] == "" {
		t.Fatal("unpinned pane lost preview signals")
	}
	msg = collectWatchMessage(server, paneKey{}, opts, nil, 0, map[string]bool{"scope-agent": true}, time.Now())
	if msg.err != nil || msg.stats.CaptureTotal != 1 || len(msg.captures) != 1 {
		t.Fatal("newly pinned pane was not captured")
	}
	msg = collectWatchMessage(server, paneKey{}, opts, msg.captures, 0, nil, time.Now())
	if msg.err != nil || msg.stats.CaptureTotal != 0 || len(msg.captures) != 0 {
		t.Fatal("unpin kept old captures")
	}
	preview := loadPreviewCmdOn("scope-agent", map[string]string{}, nil, 1, 1, server)().(previewMsg)
	if preview.paneEnd == 0 {
		t.Fatal("unpinned pane preview became unavailable")
	}
}

func TestPreviewVisibleLinesPreservePaneTailAndIndicator(t *testing.T) {
	m := performanceModel()
	m.width, m.height = 120, 12
	var lines []string
	for i := range 1000 {
		lines = append(lines, fmt.Sprintf("visible-%04d %s", i, strings.Repeat("x", 200)))
	}
	m.previewContent = strings.Join(lines, "\n")
	m.previewOffset = 400
	view := m.View()
	if !strings.Contains(view, "visible-0400") || !strings.Contains(view, "visible-0407") || strings.Contains(view, "visible-0408") {
		t.Fatal("preview processed wrong viewport")
	}
	m.previewOffset = 1000 - m.listHeight()
	view = m.View()
	if !strings.Contains(view, "↓ 993/1000") {
		t.Fatal("tail indicator changed")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 16})
	m = next.(model)
	if m.previewOffset > previewMaxOffset(m.previewContent, m.listHeight()) {
		t.Fatal("resize left offset outside preview")
	}
}

func TestRepositorySnapshotCoalescesAliasesAndAncestors(t *testing.T) {
	root := previewTestRepository(t)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	items := []string{"one", "two", root, sub, "missing-session"}
	msg := repositoryCmd(items, map[string]string{"one": sub, "two": sub}, 5)().(repositoriesMsg)
	if msg.generation != 5 || len(msg.byDir) != 1 || len(msg.byDir[root]) != 4 {
		t.Fatalf("repository snapshot: %+v", msg)
	}
	branches := resolveBranchesByDir(msg.byDir)
	for _, item := range items[:4] {
		if branches[item] != "main" {
			t.Fatal("branch lookup lost alias/subdirectory")
		}
	}
}

func TestPeriodicPreviewRefreshAndConfigRejectStaleReplies(t *testing.T) {
	isolatedPerformanceConfig(t)
	m := performanceModel()
	m.items, m.filtered = []string{"work"}, []int{0}
	m.previewEntry, m.previewContent, m.previewRequest = "work", "old Git details", 3
	m.previewCache["work"] = previewCacheEntry{content: m.previewContent, at: time.Now()}
	if m.refreshPreviewIfStaleCmd() != nil || m.previewRequest != 3 {
		t.Fatal("fresh preview scheduled redundant load")
	}
	m.previewCache["work"] = previewCacheEntry{content: m.previewContent, at: time.Now().Add(-previewCacheTTL)}
	if m.refreshPreviewIfStaleCmd() == nil || m.previewRequest != 4 {
		t.Fatal("stationary expired preview did not refresh")
	}
	cfg := m.resolvedCfg
	no := false
	cfg.Preview.GitStatus = &no
	next, _ := m.Update(configReloadMsg{cfg: cfg, mtime: time.Now()})
	m = next.(model)
	if len(m.previewCache) != 0 || m.previewRequest != 5 {
		t.Fatal("config change retained old preview request")
	}
	next, _ = m.Update(previewMsg{entry: "work", request: 4, content: "stale config"})
	if next.(model).previewContent == "stale config" {
		t.Fatal("old settings overwrote new preview")
	}
	m.mode = modeSnippetSelect
	if m.refreshPreviewIfStaleCmd() != nil {
		t.Fatal("periodic refresh probed snippet label as a session")
	}
}

func BenchmarkPreviewLongViewport(b *testing.B) {
	m := performanceModel()
	m.previewContent = strings.Repeat(strings.Repeat("x", 200)+"\n", 1000)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.View()
	}
}

func BenchmarkWatchUnchangedLarge(b *testing.B) {
	m := performanceModel()
	m.items = benchItems(4000)
	m.input.SetValue("server")
	m.refilter()
	msg := watchMsg{info: waitingInfo{}}
	b.ReportAllocs()
	for b.Loop() {
		next, _ := m.Update(msg)
		m = next.(model)
	}
}
