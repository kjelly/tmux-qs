package main

import (
	"slices"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const autoTagsTTL = 5 * time.Second

type tagSnapshot struct {
	tags []string
	at   time.Time
}

type tagsMsg struct {
	generation uint64
	request    uint64
	values     map[string]tagSnapshot
}

func (m model) tagSourceItems() []string {
	if m.mode == modeTag || m.mode == modeGroup || m.mode == modeAgentSelect || m.mode == modeSnippetSelect {
		return m.savedItems
	}
	return m.items
}

// Scanning is demand-driven. In particular, ordinary cursor movement and
// View never access the filesystem, even with auto-detected tags enabled.
func (m *model) updateTagsCmd(force bool) tea.Cmd {
	if m.tagsLoading && m.tagGeneration != m.loadGeneration {
		force = true
	}
	if force {
		m.tagRequest++
		m.tagsLoading = false
	}
	if !loadConfig().Tags.ShowAutoDetected && m.tagFilter == "" && m.mode != modeTag {
		return nil
	}
	if m.tagsLoading {
		return nil
	}
	if m.autoTags == nil {
		m.autoTags = make(map[string]tagSnapshot)
	}
	dirs := make(map[string]bool)
	for _, item := range m.tagSourceItems() {
		if dir := entryDir(item, m.sessionPaths); dir != "" {
			dirs[dir] = true
		}
	}
	// Bound retained state to the current workspace source, including its
	// saved list while a picker is open. Path changes cannot reuse old tags.
	for dir := range m.autoTags {
		if !dirs[dir] {
			delete(m.autoTags, dir)
		}
	}
	var stale []string
	for dir := range dirs {
		if cached, ok := m.autoTags[dir]; force || !ok || time.Since(cached.at) >= autoTagsTTL {
			stale = append(stale, dir)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	m.tagRequest++
	m.tagGeneration = m.loadGeneration
	m.tagsLoading = true
	return loadTagsCmd(stale, m.loadGeneration, m.tagRequest)
}

func loadTagsCmd(dirs []string, generation, request uint64) tea.Cmd {
	return func() tea.Msg {
		values := make(map[string]tagSnapshot, len(dirs))
		var mu sync.Mutex
		var wg sync.WaitGroup
		jobs := make(chan string)
		for range min(8, len(dirs)) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for dir := range jobs {
					snapshot := tagSnapshot{tags: autoDetectTags(dir), at: time.Now()}
					mu.Lock()
					values[dir] = snapshot
					mu.Unlock()
				}
			}()
		}
		for _, dir := range dirs {
			jobs <- dir
		}
		close(jobs)
		wg.Wait()
		return tagsMsg{generation: generation, request: request, values: values}
	}
}

func (m model) applyTags(msg tagsMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.loadGeneration || msg.request != m.tagRequest {
		return m, nil
	}
	m.tagsLoading = false
	if m.autoTags == nil {
		m.autoTags = make(map[string]tagSnapshot)
	}
	changed := false
	for dir, snapshot := range msg.values {
		if !slices.Equal(m.autoTags[dir].tags, snapshot.tags) {
			changed = true
		}
		m.autoTags[dir] = snapshot
	}
	if m.mode == modeTag {
		selected, _ := m.selected()
		m.items = m.allTags()
		m.refilter()
		m.restoreSelected(selected)
		if len(m.items) == 0 {
			m.errText = "no tags available"
		} else if m.errText == "no tags available" {
			m.errText = ""
		}
	} else if changed && m.mode == modeList && m.tagFilter != "" {
		selected, _ := m.selected()
		m.refilter()
		m.restoreSelected(selected)
		return m, m.updatePreviewCmd()
	}
	return m, nil
}
