package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// lastViewFileName is the cache file that records the most recent
// TUI's list state. Used to restore "what the user was looking at"
// on the next invocation within lastViewTTL.
const lastViewFileName = "last-view.json"

// lastViewTTL is the maximum age of a persisted last-view snapshot
// that we are willing to reuse. 60s is long enough to cover the
// common "close picker, look at something, reopen" workflow but
// short enough that returning to a tmux-qs invocation hours later
// doesn't surface a stale view.
const lastViewTTL = 60 * time.Second

// lastViewVersion is bumped whenever the on-disk schema changes in
// an incompatible way. Old caches with a different version are
// silently ignored by loadLastView.
const lastViewVersion = 1

// lastView is the on-disk shape of a TUI snapshot. Field names are
// snake_case in JSON for forward-compat readability, and most
// fields are ints (the underlying enum values) rather than strings
// so renumbering a sourceKind / uiMode / vimModeType can't produce
// JSON that decodes to the wrong constant.
type lastView struct {
	Version       int       `json:"version"`
	SavedAt       time.Time `json:"saved_at"`
	Src           int       `json:"src"`
	Mode          int       `json:"mode"`
	Input         string    `json:"input"`
	Cursor        int       `json:"cursor"`
	TagFilter     string    `json:"tag_filter"`
	GroupFilter   string    `json:"group_filter"`
	VimMode       int       `json:"vim_mode"`
	PreviewOffset int       `json:"preview_offset"`
	ShowDetail    bool      `json:"show_detail"`
}

func lastViewPath() string {
	return xdgCachePath(lastViewFileName)
}

// saveLastView writes the current model's list state to the cache
// directory. Best-effort: any I/O error is swallowed because the
// next open will simply fall back to the default view. We don't
// want a permission problem to crash the picker on exit.
//
// We do not preserve `items` / `filtered` / `marked`: those are
// recomputed from the source on the next open via loadCmd(src).
func saveLastView(m *model) {
	p := lastViewPath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	lv := lastView{
		Version:       lastViewVersion,
		SavedAt:       time.Now(),
		Src:           int(m.src),
		Mode:          int(m.mode),
		Input:         m.input.Value(),
		Cursor:        m.cursor,
		TagFilter:     m.tagFilter,
		GroupFilter:   m.groupFilter,
		VimMode:       int(m.vimMode),
		PreviewOffset: m.previewOffset,
		ShowDetail:    m.showDetail,
	}
	data, err := json.Marshal(lv)
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}

// loadLastView returns the persisted state if (a) the file exists
// and is readable, (b) the JSON parses cleanly, (c) the schema
// version matches, (d) the saved_at timestamp is within lastViewTTL
// of now. Returns zero-value, false otherwise. We intentionally do
// NOT log or surface load errors — a stale or missing cache should
// be invisible to the user.
func loadLastView() (lastView, bool) {
	p := lastViewPath()
	if p == "" {
		return lastView{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return lastView{}, false
	}
	var lv lastView
	if err := json.Unmarshal(data, &lv); err != nil {
		return lastView{}, false
	}
	if lv.Version != lastViewVersion {
		return lastView{}, false
	}
	if lv.SavedAt.IsZero() {
		return lastView{}, false
	}
	if time.Since(lv.SavedAt) > lastViewTTL {
		return lastView{}, false
	}
	return lv, true
}

// applyLastView restores the persisted state to m. Non-modeList
// modes (modeBranch, modeTag, modeGroup, modeFiles, modeAgentSelect)
// are not safe to restore across processes because they depend on
// intermediate data — m.savedItems, the branch list, the file
// listing — that we don't persist. For those modes we fall back to
// modeList and clear the tag/group filters that were only
// meaningful in the previous session.
//
// Some srcKinds are also unsafe to restore: srcWaiting (in-memory
// watcher snapshot, not loadable from disk), srcFiles (depends on
// m.fileSearchDir which we don't persist), and srcWindows
// (populated on demand by showWindows, not by loadCmd). For those
// we fall back to srcDefault so the picker comes up with a usable
// list. The user can re-enter the special view with Ctrl-w / Alt-f
// / Ctrl-v.
func applyLastView(m *model, lv lastView) {
	src := sourceKind(lv.Src)
	switch src {
	case srcWaiting, srcFiles, srcWindows:
		src = srcDefault
	}
	m.src = src
	m.input.SetValue(lv.Input)
	m.cursor = lv.Cursor
	if m.vimEnabled {
		m.vimMode = vimModeType(lv.VimMode)
	} else {
		m.vimMode = vimInsert
	}
	m.previewOffset = lv.PreviewOffset
	m.showDetail = lv.ShowDetail

	mode := uiMode(lv.Mode)
	if mode != modeList {
		mode = modeList
		m.tagFilter = ""
		m.groupFilter = ""
	} else {
		// modeList: tagFilter and groupFilter are in-list sub-filters
		// and are safe to restore.
		m.tagFilter = lv.TagFilter
		m.groupFilter = lv.GroupFilter
	}
	m.mode = mode
}
