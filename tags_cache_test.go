package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

func performanceModel() model {
	cfg := defaultConfig
	input := textinput.New()
	input.Prompt = ""
	return model{input: input, width: 100, height: 24, src: srcDefault,
		noteEditor: newSessionNoteEditor(),
		styles:     newStyleBundle(cfg.Style), resolvedCfg: cfg, floatWaiting: cfg.Waiting.FloatToTop,
		annots: map[string]string{}, dirty: map[string]bool{}, previewCache: map[string]previewCacheEntry{}}
}

func isolatedPerformanceConfig(t *testing.T) {
	t.Helper()
	withIsolatedEnv(t)
	t.Setenv("TMUX_QS_CONFIG", "")
	resetConfigCache()
	t.Cleanup(resetConfigCache)
}

func TestTagsBackgroundLifecycle(t *testing.T) {
	isolatedPerformanceConfig(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	m := performanceModel()
	m.items = []string{"project", "alias"}
	m.sessionPaths = map[string]string{"project": dir, "alias": dir}
	m.refilter()
	if got := m.entryTags("project"); len(got) != 0 {
		t.Fatal("tag lookup synchronously scanned files")
	}
	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ctrl+,")})
	m = updated.(model)
	if cmd == nil || m.mode != modeTag || !m.tagsLoading {
		t.Fatal("tag picker did not start background scan")
	}
	if !slices.Equal(m.savedItems, []string{"project", "alias"}) {
		t.Fatal("workspace list was lost")
	}
	msg := cmd().(tagsMsg)
	if len(msg.values) != 1 {
		t.Fatal("aliases were scanned separately")
	}
	updated, _ = m.Update(msg)
	m = updated.(model)
	if !slices.Equal(m.items, []string{"go"}) || m.tagsLoading {
		t.Fatalf("tag completion: %+v", m.items)
	}
	if m.updateTagsCmd(false) != nil {
		t.Fatal("fresh tags were scanned again")
	}
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.tagFilter != "go" || len(m.filtered) != 2 {
		t.Fatal("Enter did not apply background tags")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	cached := m.autoTags[dir]
	cached.at = time.Now().Add(-autoTagsTTL)
	m.autoTags[dir] = cached
	cmd = m.updateTagsCmd(false)
	if cmd == nil {
		t.Fatal("expired tags did not refresh")
	}
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if len(m.filtered) != 0 || len(m.entryTags("project")) != 0 {
		t.Fatal("removed marker survived refresh")
	}
}

func TestTagsStaleRepliesAndEsc(t *testing.T) {
	isolatedPerformanceConfig(t)
	m := performanceModel()
	dir := t.TempDir()
	m.items, m.savedItems, m.mode = []string{}, []string{"project"}, modeTag
	m.sessionPaths = map[string]string{"project": dir}
	cmd := m.updateTagsCmd(false)
	old := cmd().(tagsMsg)
	m.loadGeneration++
	current := m.updateTagsCmd(false)
	if current == nil {
		t.Fatal("old generation left scan permanently pending")
	}
	updated, _ := m.Update(old)
	m = updated.(model)
	if !m.tagsLoading || len(m.autoTags) != 0 {
		t.Fatal("stale completion changed current tags")
	}
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.mode != modeList || !slices.Equal(m.items, []string{"project"}) {
		t.Fatal("Esc did not restore workspaces")
	}
	updated, _ = m.Update(current())
	if updated.(model).mode != modeList {
		t.Fatal("late tags reopened picker")
	}
}

func TestTagsPathChangeDiscardsOldDirectory(t *testing.T) {
	isolatedPerformanceConfig(t)
	m := performanceModel()
	oldDir, newDir := t.TempDir(), t.TempDir()
	m.mode, m.savedItems = modeTag, []string{"project"}
	m.autoTags = map[string]tagSnapshot{oldDir: {tags: []string{"go"}, at: time.Now()}}
	m.sessionPaths = map[string]string{"project": newDir}
	if cmd := m.updateTagsCmd(true); cmd == nil {
		t.Fatal("changed path was not scanned")
	}
	if len(m.autoTags) != 0 || len(m.entryTags("project")) != 0 {
		t.Fatal("old directory tags leaked")
	}
}

type tagsProgramModel struct{ model }

func (tagsProgramModel) Init() tea.Cmd { return nil }

func TestTeatestBackgroundTagsPicker(t *testing.T) {
	isolatedPerformanceConfig(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	m := performanceModel()
	m.items = []string{"workspace"}
	m.sessionPaths = map[string]string{"workspace": dir}
	m.refilter()
	tm := teatest.NewTestModel(t, tagsProgramModel{m}, teatest.WithInitialTermSize(70, 24))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ctrl+,")})
	waitForOutput(t, tm, 3*time.Second, func(s string) bool { return strings.Contains(s, "> go") })
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	waitForOutput(t, tm, 3*time.Second, func(s string) bool { return strings.Contains(s, "> workspace") })
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(model)
	if final.mode != modeList || final.tagFilter != "go" || len(final.filtered) != 1 {
		t.Fatal("async tag flow lost selection")
	}
}
