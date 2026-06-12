package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const inputMinTopMargin = 2

const header = "  ^a all ^t tmux ^g configs ^x zoxide ^d tmux kill ^f find ^b branches ^w waiting ^y copy ? help"

// uiMode is the TUI's high-level state — which "screen" is currently
// being shown. Most keybindings only make sense in modeList.
type uiMode int

const (
	modeList uiMode = iota
	modeBranch
	modeHelp
)

type (
	itemsMsg struct {
		src   sourceKind
		items []string
		// info is the tmux session snapshot (path + meta), captured in
		// the same background goroutine as the item load so the Update
		// loop never forks tmux synchronously.
		info map[string]sessionInfo
	}
	annotMsg    map[string]string // raw entry -> git branch (without dirty mark)
	dirtyMsg    map[string]bool   // raw entry -> isDirty (only entries that are dirty)
	branchesMsg struct {
		repo     string
		branches []branchEntry
	}
	switchedMsg struct{ path string } // branch ready; connect to path
	uiErrMsg    struct{ err error }
	// copyDoneMsg is delivered after ctrl+y finishes writing to the
	// clipboard. path echoes what was sent (so the UI can show
	// confirmation); err is non-nil if the write failed.
	copyDoneMsg struct {
		path string
		err  error
	}
	// copyClearMsg clears the copy confirmation from the status line.
	copyClearMsg struct{}
	previewMsg   struct {
		entry   string
		content string
	}
	// previewTickMsg fires after the preview debounce window. The
	// preview is only loaded if the cursor is still on the same entry,
	// so holding an arrow key doesn't fork git/tmux for every row
	// skimmed past.
	previewTickMsg struct{ entry string }
)

const (
	// previewDebounce is how long the cursor must rest on an entry
	// before its preview is loaded.
	previewDebounce = 80 * time.Millisecond
	// previewCacheTTL is how long a loaded preview is reused without
	// reloading when the cursor returns to an entry.
	previewCacheTTL = 3 * time.Second
)

// previewCacheEntry is one cached preview panel render.
type previewCacheEntry struct {
	content string
	at      time.Time
}

type model struct {
	input    textinput.Model
	mode     uiMode
	src      sourceKind
	items    []string          // current entries (raw)
	annots   map[string]string // raw entry -> git branch (without dirty mark)
	dirty    map[string]bool   // raw entry -> isDirty (only entries confirmed dirty)
	branches []branchEntry     // branch-mode entries
	repo     string            // branch-mode repo path

	filtered []int // indices into items/branches matching the query
	cursor   int   // index into filtered
	offset   int   // scroll offset into filtered

	width, height int
	errText       string
	loading       bool
	styles        styleBundle // lipgloss styles resolved from config

	// pane watcher state
	selfPane paneKey        // tmux-qs popup's own pane (excluded from detection)
	waiting  waitingInfo    // latest aggregation result
	watchOpt WatchingConfig // watcher options resolved from config (re-injected on tick)
	// paneBufs is the per-pane capture-buffer snapshot from the last
	// watcher tick, fed into the next watchCmd so it can detect
	// "stuck" panes (buffer unchanged across ticks).
	paneBufs map[paneKey]string

	// inputPad is the number of blank lines printed before the prompt
	// line; used in popup mode to roughly center the input vertically
	// when there is spare vertical space.
	inputPad int

	// copyConfirm holds a "✓ copied <path>" message that briefly
	// replaces the status line after ctrl+y succeeds. expiresAt
	// triggers a copyClearMsg to remove it.
	copyConfirm string

	// sessionMeta is a snapshot of `tmux list-sessions` metadata
	// (created time, last-active time) used to render per-row age
	// hints. Refreshed on each itemsMsg.
	sessionMeta map[string]sessionMeta

	// sessionPaths is the most recent snapshot of session-name ->
	// session_path, used by Ctrl-y, Ctrl-b, and Alt-j/Alt-k. Refreshed
	// on each itemsMsg. Caching avoids forking `tmux list-sessions`
	// on every keystroke.
	sessionPaths map[string]string

	// showDetail toggles a "per-pane detail panel" rendered inline
	// under the cursor row when it's a waiting entry. Toggled by
	// Ctrl-Space (a free binding in the current keymap).
	showDetail bool

	// pendingKill holds the entry the user pressed Ctrl-d on once;
	// the kill only happens when Ctrl-d is pressed a second time on
	// the same entry. Reset on any cursor move or filter change.
	pendingKill string

	// final selection, consumed by main after the program quits
	result       string
	resultPaneID string

	// preview panel state
	previewEntry   string
	previewContent string
	previewCache   map[string]previewCacheEntry
}

func newModel() model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Focus()
	// Load config here (not in Init) because Init is a value receiver and
	// any mutation is lost. newModel's return value is what gets handed
	// to tea.NewProgram, so the watchOpt is preserved for Update to use.
	cfg := loadConfig()
	// Seed waiting state from the cross-process cache so a quick
	// reopen of the popup can surface the most recent waiting agents
	// before the first watcher tick arrives.
	w, _ := loadWaitingCache()
	return model{
		input:  ti,
		src:    srcDefault,
		annots: map[string]string{},
		dirty:  map[string]bool{},
		height: 24,
		width:  80,
		styles: newStyleBundle(cfg.Style),
		// Compile the prompt regexes once here; watchCmd runs every
		// poll tick and must not pay the recompile cost each time.
		watchOpt:     cfg.toWatchingConfig().compilePatterns(),
		waiting:      w,
		previewCache: map[string]previewCacheEntry{},
	}
}

func (m model) Init() tea.Cmd {
	// Only kick off the initial load and self-pane probe here. The watcher
	// is started by the selfPaneMsg handler so its first tick has the
	// correct self-pane exclusion (avoids a race where the popup's own
	// pane is briefly counted as waiting).
	return tea.Batch(loadCmd(srcDefault), selfPaneCmd())
}

func loadCmd(src sourceKind) tea.Cmd {
	return func() tea.Msg {
		items, err := loadSource(src)
		if err != nil {
			return uiErrMsg{err}
		}
		return itemsMsg{src: src, items: items, info: tmuxSessionInfo()}
	}
}

func annotateCmd(items []string, sessionPaths map[string]string) tea.Cmd {
	return func() tea.Msg {
		return annotMsg(resolveBranches(items, sessionPaths))
	}
}

// dirtyCmd resolves the dirty flag for entries that already have a
// branch annotation. It runs `git status --porcelain` on each entry's
// directory in parallel and emits a dirtyMsg containing only the
// entries that turned out to be dirty. The UI appends "*" to those
// rows.
//
// Decoupling dirty detection from the initial annotation pass means
// the first list render is not blocked on git status (which can be
// slow on large repos / over NFS). The branch name shows up
// immediately; the dirty mark appears a moment later.
func dirtyCmd(items []string, sessionPaths map[string]string) tea.Cmd {
	return func() tea.Msg {
		return dirtyMsg(resolveDirty(items, sessionPaths))
	}
}

func branchesCmd(repo string) tea.Cmd {
	return func() tea.Msg {
		branches, err := listBranches(repo)
		if err != nil {
			return uiErrMsg{fmt.Errorf("not a git repository: %s", repo)}
		}
		if len(branches) == 0 {
			return uiErrMsg{fmt.Errorf("no branches in %s", repo)}
		}
		return branchesMsg{repo: repo, branches: branches}
	}
}

func jumpBranchCmd(repo string, b branchEntry) tea.Cmd {
	return func() tea.Msg {
		if b.worktreePath != "" {
			return switchedMsg{path: b.worktreePath}
		}
		if b.current {
			return switchedMsg{path: repo}
		}
		if err := switchBranch(repo, b.name); err != nil {
			return uiErrMsg{err}
		}
		return switchedMsg{path: repo}
	}
}

// copyToClipboardCmd writes path to the system clipboard via OSC 52
// and returns a copyDoneMsg describing the result. Errors are surfaced
// to the UI (status line), never silently dropped.
func copyToClipboardCmd(path string) tea.Cmd {
	return func() tea.Msg {
		err := copyToClipboard(path)
		return copyDoneMsg{path: path, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
		m.recalcInputPad()
		return m, nil

	case itemsMsg:
		m.mode = modeList
		m.src = msg.src
		// Apply recency sort: most-recently-selected sessions float
		// to the top of the list. Currently this only affects
		// srcDefault / srcAll, which both show the running tmux
		// sessions; the other sources (zoxide, configs, find) keep
		// their natural order.
		items := msg.items
		if msg.src == srcDefault || msg.src == srcAll {
			items = loadRecent().orderedByRecency(items)
		}
		m.items = items
		m.loading = false
		m.errText = ""
		// Refresh per-render caches (paths + meta) so Ctrl-y,
		// Ctrl-b, Alt-j/Alt-k, and the age hints in renderEntry all
		// see the latest tmux server state without forking tmux
		// per keystroke / per frame. The snapshot was taken in
		// loadCmd's goroutine so this handler never blocks on a
		// tmux fork.
		info := msg.info
		m.sessionPaths = make(map[string]string, len(info))
		m.sessionMeta = make(map[string]sessionMeta, len(info))
		for name, si := range info {
			m.sessionPaths[name] = si.path
			m.sessionMeta[name] = si.meta
		}
		m.refilter()
		return m, tea.Batch(annotateCmd(m.items, m.sessionPaths), dirtyCmd(m.items, m.sessionPaths), m.updatePreviewCmd())

	case annotMsg:
		for k, v := range msg {
			m.annots[k] = v
		}
		return m, nil

	case dirtyMsg:
		for k := range msg {
			m.dirty[k] = true
		}
		return m, nil

	case selfPaneMsg:
		m.selfPane = msg.key
		return m, watchCmd(m.selfPane, m.watchOpt, m.paneBufs)

	case watchMsg:
		if msg.err != nil {
			return m, watchCmd(m.selfPane, m.watchOpt, m.paneBufs)
		}
		// Detect transitions: any session that newly entered the
		// "waiting" set since the last tick should trigger a
		// notification (terminal bell + optional desktop notif).
		var newSessions []string
		prev := m.waiting.bySession
		for name := range msg.info.bySession {
			if _, ok := prev[name]; !ok {
				newSessions = append(newSessions, name)
			}
		}
		m.waiting = msg.info
		m.paneBufs = msg.bufs
		next := watchCmd(m.selfPane, m.watchOpt, m.paneBufs)
		if len(newSessions) > 0 {
			return m, tea.Batch(next, notifyWaitingCmd(newSessions))
		}
		return m, next

	case branchesMsg:
		m.mode = modeBranch
		m.repo = msg.repo
		m.branches = msg.branches
		m.loading = false
		m.errText = ""
		m.input.SetValue("")
		m.refilter()
		return m, nil

	case switchedMsg:
		m.result = msg.path
		return m, tea.Quit

	case uiErrMsg:
		m.loading = false
		m.errText = msg.err.Error()
		return m, nil

	case copyDoneMsg:
		if msg.err != nil {
			m.copyConfirm = ""
			m.errText = "copy failed"
			return m, nil
		}
		m.errText = ""
		m.copyConfirm = msg.path
		// Clear the confirmation after 1.5s so it doesn't linger
		// indefinitely when the user is busy.
		return m, tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg {
			return copyClearMsg{}
		})

	case copyClearMsg:
		m.copyConfirm = ""
		return m, nil

	case previewTickMsg:
		// Debounce window elapsed; only load if the cursor still
		// rests on the same entry.
		if msg.entry == m.previewEntry {
			return m, loadPreviewCmd(msg.entry, m.sessionPaths, m.waiting.panes)
		}
		return m, nil

	case previewMsg:
		if m.previewCache == nil {
			m.previewCache = map[string]previewCacheEntry{}
		}
		m.previewCache[msg.entry] = previewCacheEntry{content: msg.content, at: time.Now()}
		if msg.entry == m.previewEntry {
			m.previewContent = msg.content
		}
		return m, nil

	case tea.MouseMsg:
		prevCursor := m.cursor
		prevFilteredLen := len(m.filtered)
		resModel, cmd := m.handleMouse(msg)
		m = resModel.(model)
		if m.cursor != prevCursor || len(m.filtered) != prevFilteredLen {
			return m, tea.Batch(cmd, m.updatePreviewCmd())
		}
		return m, cmd

	case tea.KeyMsg:
		prevCursor := m.cursor
		prevFilteredLen := len(m.filtered)
		prevInputVal := m.input.Value()
		resModel, cmd := m.handleKey(msg)
		m = resModel.(model)
		if m.cursor != prevCursor || len(m.filtered) != prevFilteredLen || m.input.Value() != prevInputVal {
			return m, tea.Batch(cmd, m.updatePreviewCmd())
		}
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) reload(src sourceKind) (tea.Model, tea.Cmd) {
	m.loading = true
	m.input.SetValue("")
	m.src = src
	return *m, loadCmd(src)
}

// showWaiting swaps the list to only the sessions that currently have
// waiting agents (the ^w binding advertised in the header). The list is
// built from the in-memory watcher snapshot — no external forks — so it
// reflects the most recent tick, not live state.
func (m model) showWaiting() (tea.Model, tea.Cmd) {
	names := make([]string, 0, len(m.waiting.bySession))
	for name := range m.waiting.bySession {
		names = append(names, name)
	}
	sort.Strings(names)
	m.src = srcWaiting
	m.items = names
	m.input.SetValue("")
	m.errText = ""
	m.loading = false
	m.refilter()
	return m, tea.Batch(annotateCmd(m.items, m.sessionPaths), dirtyCmd(m.items, m.sessionPaths), m.updatePreviewCmd())
}

func (m model) choose() (tea.Model, tea.Cmd) {
	if len(m.filtered) == 0 {
		return m, nil
	}
	idx := m.filtered[m.cursor]
	if m.mode == modeBranch {
		m.loading = true
		return m, jumpBranchCmd(m.repo, m.branches[idx])
	}
	selected := strings.TrimSpace(m.items[idx])
	// Record the selection so the next TUI can bubble it up in
	// the recency-sorted list view. Only record for entries that
	// are real tmux sessions (i.e. not arbitrary paths).
	if _, ok := m.sessionPaths[selected]; ok {
		recent := loadRecent().recordTouch(selected)
		recent.save()
	}
	m.result = selected
	// If there is a waiting pane for this session, save its paneID!
	for _, wp := range m.waiting.panes {
		if wp.session == selected {
			m.resultPaneID = wp.paneID
			break
		}
	}
	return m, tea.Quit
}

func (m model) selected() (string, bool) {
	if m.mode != modeList || len(m.filtered) == 0 {
		return "", false
	}
	return strings.TrimSpace(m.items[m.filtered[m.cursor]]), true
}

func (m *model) move(delta int) {
	if len(m.filtered) == 0 {
		return
	}
	// Clamp at the list boundaries instead of wrapping — pressing Up
	// on the first row (or Down on the last) leaves the cursor in
	// place rather than teleporting to the opposite end.
	next := m.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(m.filtered) {
		next = len(m.filtered) - 1
	}
	m.cursor = next
	m.pendingKill = ""
	m.clampScroll()
}

// jumpToSession moves the cursor to the next/prev entry in the filtered
// list that corresponds to an *existing* tmux session. delta = +1 jumps
// down, -1 jumps up. If no other session exists in the chosen direction
// (e.g. cursor is on the only session), the cursor is left where it was.
// Uses the cached m.sessionPaths so the keystroke does not fork tmux.
func (m *model) jumpToSession(delta int) {
	if len(m.filtered) == 0 || len(m.sessionPaths) == 0 {
		return
	}
	n := len(m.filtered)
	// Walk up to n steps; skip the current cursor slot so we never
	// re-find ourselves. The first match in the chosen direction wins.
	for step := 1; step <= n; step++ {
		next := (m.cursor + delta*step + n) % n
		if next == m.cursor {
			continue
		}
		entry := strings.TrimSpace(m.items[m.filtered[next]])
		if _, ok := m.sessionPaths[entry]; ok {
			m.cursor = next
			m.clampScroll()
			return
		}
	}
}

func (m *model) entryText(i int) string {
	if m.mode == modeBranch {
		return m.branches[i].name
	}
	return m.items[i]
}

func (m *model) entryCount() int {
	if m.mode == modeBranch {
		return len(m.branches)
	}
	return len(m.items)
}

// refilter applies fzf-like subsequence matching. With an empty query
// the source order is preserved (the original workspace script runs fzf
// with --no-sort); once the user types, matches are ranked by fuzzy
// score (boundary/consecutive bonuses), with source order as the
// stable tie-break.
func (m *model) refilter() {
	query := m.input.Value()
	m.filtered = m.filtered[:0]
	hasQuery := strings.TrimSpace(query) != ""
	var scores []int
	for i := 0; i < m.entryCount(); i++ {
		ok, score := fuzzyScore(m.entryText(i), query)
		if !ok {
			continue
		}
		m.filtered = append(m.filtered, i)
		if hasQuery {
			scores = append(scores, score)
		}
	}
	if hasQuery && len(m.filtered) > 1 {
		order := make([]int, len(m.filtered))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			return scores[order[a]] > scores[order[b]]
		})
		sorted := make([]int, len(m.filtered))
		for i, o := range order {
			sorted[i] = m.filtered[o]
		}
		copy(m.filtered, sorted)
	}
	m.cursor = 0
	m.offset = 0
	m.pendingKill = ""
	m.recalcInputPad()
}

func (m *model) listHeight() int {
	h := m.height - 3 // prompt + header + error/status line
	if h < 1 {
		h = 1
	}
	return h
}

func (m *model) clampScroll() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
}

// recalcInputPad decides how many blank lines should precede the prompt
// line so that the input box appears roughly in the vertical middle of a
// popup. It is a no-op when not running inside a popup, and it falls back
// to no padding when the list already fills the visible area. A minimum
// top margin of inputMinTopMargin rows is always respected when any
// padding is applied.
func (m *model) recalcInputPad() {
	if os.Getenv(popupEnv) == "" {
		m.inputPad = 0
		return
	}
	available := m.height - 3 // prompt + header + status line
	if available <= 0 {
		m.inputPad = 0
		return
	}
	// Effective list rows: we render at most listHeight rows, but never
	// more than the number of items that actually exist.
	rows := m.listHeight()
	if len(m.filtered) < rows {
		rows = len(m.filtered)
	}
	if rows+1 >= available {
		m.inputPad = 0
		return
	}
	rawPad := (available - (rows + 1)) / 2
	if rawPad < inputMinTopMargin {
		rawPad = inputMinTopMargin
	}
	m.inputPad = rawPad
}

// updatePreviewCmd schedules a preview refresh for the selected entry.
// A fresh cache hit is applied synchronously (no forks); otherwise a
// debounce tick is scheduled and the actual load only happens if the
// cursor is still on the entry when it fires.
func (m *model) updatePreviewCmd() tea.Cmd {
	sel, ok := m.selected()
	if !ok {
		m.previewEntry = ""
		m.previewContent = ""
		return nil
	}
	m.previewEntry = sel
	if c, hit := m.previewCache[sel]; hit && time.Since(c.at) < previewCacheTTL {
		m.previewContent = c.content
		return nil
	}
	entry := sel
	return tea.Tick(previewDebounce, func(time.Time) tea.Msg {
		return previewTickMsg{entry: entry}
	})
}

func loadPreviewCmd(entry string, sessionPaths map[string]string, waitingPanes []waitingPane) tea.Cmd {
	return func() tea.Msg {
		dir := entryDir(entry, sessionPaths)
		var lines []string

		// 1. Prepend waiting agent details if any (highest priority)
		var wps []waitingPane
		for _, wp := range waitingPanes {
			if wp.session == entry || (dir != "" && sessionPaths[wp.session] == dir) {
				wps = append(wps, wp)
			}
		}
		if len(wps) > 0 {
			lines = append(lines, "Waiting Agents:")
			for _, wp := range wps {
				age := formatAge(wp.ttyIdle)
				lines = append(lines, fmt.Sprintf("  ⏳ %s @ %s.%d (%s)", wp.cmd, wp.window, wp.index, age))
			}
			lines = append(lines, "")
		}

		// 2. If it has a git directory, get git status / log (medium priority)
		if dir != "" {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				lines = append(lines, "Git Status:")
				if statusOut, err := runLines("git", "-C", dir, "status", "-s"); err == nil && len(statusOut) > 0 {
					for i, s := range statusOut {
						if i >= 5 {
							lines = append(lines, "  ...")
							break
						}
						lines = append(lines, "  "+s)
					}
				} else {
					lines = append(lines, "  clean")
				}
				lines = append(lines, "")

				lines = append(lines, "Recent Commits:")
				if logOut, err := runLines("git", "-C", dir, "log", "-n", "5", "--oneline"); err == nil {
					for _, l := range logOut {
						lines = append(lines, "  "+l)
					}
				}
				lines = append(lines, "")
			}
		}

		// 3. If it's a tmux session, get all panes and their content (lowest priority, shown at the end)
		if !looksLikePath(entry) {
			paneLines, err := runLines("tmux", "list-panes", "-s", "-t", entry, "-F",
				"#{window_index}\t#{window_name}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}\t#{pane_active}\t#{pane_current_path}")
			if err == nil && len(paneLines) > 0 {
				type paneInfo struct {
					index  int
					id     string
					cmd    string
					active bool
					dir    string
				}
				type windowInfo struct {
					index     int
					name      string
					panes     []paneInfo
					activeDir string
				}
				var windows []windowInfo
				var winMap = make(map[int]int)

				for _, pl := range paneLines {
					parts := strings.Split(pl, "\t")
					if len(parts) < 7 {
						continue
					}
					wIdx, _ := strconv.Atoi(parts[0])
					wName := parts[1]
					pIdx, _ := strconv.Atoi(parts[2])
					pID := parts[3]
					pCmd := parts[4]
					pActive := parts[5] == "1"
					pDir := parts[6]

					p := paneInfo{index: pIdx, id: pID, cmd: pCmd, active: pActive, dir: pDir}

					if idx, exists := winMap[wIdx]; exists {
						windows[idx].panes = append(windows[idx].panes, p)
						if pActive {
							windows[idx].activeDir = pDir
						}
					} else {
						winMap[wIdx] = len(windows)
						wInfo := windowInfo{index: wIdx, name: wName, panes: []paneInfo{p}}
						if pActive {
							wInfo.activeDir = pDir
						}
						windows = append(windows, wInfo)
					}
				}

				for i := range windows {
					if windows[i].activeDir == "" && len(windows[i].panes) > 0 {
						windows[i].activeDir = windows[i].panes[0].dir
					}
				}

				// Display high-level window list first, showing running commands and directory overrides in each window
				lines = append(lines, "Tmux Windows:")
				for _, w := range windows {
					var cmds []string
					seenCmd := make(map[string]bool)
					for _, p := range w.panes {
						if !seenCmd[p.cmd] {
							seenCmd[p.cmd] = true
							cmds = append(cmds, p.cmd)
						}
					}
					cmdsStr := strings.Join(cmds, ", ")

					// Determine directory overlay
					dirOverlay := ""
					if w.activeDir != "" && dir != "" && filepath.Clean(w.activeDir) != filepath.Clean(dir) {
						cleanWDir := filepath.Clean(w.activeDir)
						cleanSDir := filepath.Clean(dir)
						if strings.HasPrefix(cleanWDir, cleanSDir) {
							rel := strings.TrimPrefix(cleanWDir, cleanSDir)
							rel = strings.TrimPrefix(rel, string(filepath.Separator))
							if rel != "" {
								dirOverlay = " (" + rel + ")"
							}
						} else {
							// If outside the session dir, show relative to home or absolute path
							home, err := os.UserHomeDir()
							if err == nil && strings.HasPrefix(cleanWDir, home) {
								dirOverlay = " (~" + strings.TrimPrefix(cleanWDir, home) + ")"
							} else {
								dirOverlay = " (" + cleanWDir + ")"
							}
						}
					}

					lines = append(lines, fmt.Sprintf("  %d: %s%s [%s]", w.index, w.name, dirOverlay, cmdsStr))
				}
				lines = append(lines, "")

				// Capture each pane's tail in parallel (bounded) —
				// the sequential loop made preview latency
				// proportional to the session's pane count.
				bufs := make(map[string]string, len(paneLines))
				var mu sync.Mutex
				var wg sync.WaitGroup
				sem := make(chan struct{}, watchCaptureParallel)
				for _, w := range windows {
					for _, p := range w.panes {
						wg.Add(1)
						go func(id string) {
							defer wg.Done()
							sem <- struct{}{}
							defer func() { <-sem }()
							if pBuf, err := runOut("tmux", "capture-pane", "-p", "-t", id, "-S", "-5"); err == nil {
								mu.Lock()
								bufs[id] = pBuf
								mu.Unlock()
							}
						}(p.id)
					}
				}
				wg.Wait()

				// Display detailed panes list and output preview at the very end
				lines = append(lines, "Tmux Panes & Output:")
				for _, w := range windows {
					for _, p := range w.panes {
						activeStr := ""
						if p.active {
							activeStr = " (active)"
						}
						lines = append(lines, fmt.Sprintf("  %d.%d%s [%s]", w.index, p.index, activeStr, p.cmd))

						// Last 5 lines of the pane content
						if pBuf := bufs[p.id]; pBuf != "" {
							bufLines := strings.Split(strings.TrimSpace(pBuf), "\n")
							for _, bl := range bufLines {
								if strings.TrimSpace(bl) != "" {
									lines = append(lines, "    │ "+bl)
								}
							}
						}
					}
				}
				lines = append(lines, "")
			}
		}

		return previewMsg{entry: entry, content: strings.Join(lines, "\n")}
	}
}
