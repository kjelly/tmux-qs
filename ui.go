package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/junegunn/fzf/src/util"
)

// fuzzyParallelThreshold is the entry count at/above which refilter fans
// the per-entry scoring out across workers. Below it the single-threaded
// path (which reuses the engine's slab + Chars cache) is faster because it
// avoids goroutine and per-worker-slab overhead.
const fuzzyParallelThreshold = 1500

const inputMinTopMargin = 2

const header = "  ^a all ^t tmux ^g configs ^x zoxide ^d tmux kill ^f find ^b branches ^w waiting ^y copy ? help"

// uiMode is the TUI's high-level state — which "screen" is currently
// being shown. Most keybindings only make sense in modeList.
type uiMode int

const (
	modeList uiMode = iota
	modeBranch
	modeHelp
	modeFiles
	modeAgentSelect
	modeTag
	modeGroup
)

// vimModeType tracks the input modality within modeList. vimInsert is
// the default: the textinput is focused and typing filters the list.
// vimNormal disables the textinput and interprets keys as vim commands
// (j/k, gg/G, dd, yy, etc.). Esc toggles between the two; in vimNormal
// Esc quits the program.
type vimModeType int

const (
	vimInsert vimModeType = iota
	vimNormal
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
	filesMsg       struct {
		dir   string
		files []string
	}
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
	input             textinput.Model
	mode              uiMode
	src               sourceKind
	items             []string          // current entries (raw)
	annots            map[string]string // raw entry -> git branch (without dirty mark)
	dirty             map[string]bool   // raw entry -> isDirty (only entries confirmed dirty)
	branches          []branchEntry     // branch-mode entries
	repo              string            // branch-mode repo path
	pinned            map[string]bool
	zoxideScores      map[string]float64 // entry path -> zoxide frecency score
	fileSearchDir     string
	currentSession    string
	currentPath       string
	agentSelectTarget string
	selectedAgent     string
	savedItems        []string
	templateMode      bool // true when Alt-t triggered template selection

	// vimMode is the input modality within modeList. vimInsert is
	// the default; vimNormal disables textinput and accepts vim
	// commands. Esc toggles insert→normal; in normal mode Esc quits.
	vimMode vimModeType
	// vimCount accumulates digit prefix (e.g. "5" before "j" to move
	// 5 lines). Reset after each action.
	vimCount string
	// vimLastKey tracks the last key pressed in normal mode, used
	// to detect double-key sequences like "dd" and "gg" (vim's
	// standard way of expressing "kill" and "go to top"). Reset by
	// any non-matching key.
	vimLastKey string

	filtered []int // indices into items/branches matching the query
	cursor   int   // index into filtered
	offset   int   // scroll offset into filtered
	// matchIdx stores the fuzzy-match character indices for each
	// filtered entry, used by the renderer to highlight matched
	// characters. Indexed by the entry's index in m.items (NOT by
	// position in m.filtered), so the renderer looks it up via
	// m.matchIdx[m.filtered[row]]. Empty (nil) when the query is
	// empty or the entry didn't match.
	matchIdx map[int][]int

	width, height int
	errText       string
	loading       bool
	styles        styleBundle // lipgloss styles resolved from config
	// resolvedCfg is the resolved tmux-qs configuration, mirrored
	// on the model so the renderer can consult it on every frame
	// without forking `loadConfig()` (which holds a mutex).
	// Refreshed on configReloadMsg; defaults applied at newModel
	// time.
	resolvedCfg Config
	// themeWatch is true when the running TUI should poll tmux's
	// window-style background color and re-apply the theme when it
	// changes. Set by main() right before tea.NewProgram runs (see
	// theme.go). Off when TMUX_QS_THEME=light|dark or when not in tmux.
	themeWatch bool

	// pane watcher state
	selfPane paneKey        // tmux-qs popup's own pane (excluded from detection)
	waiting  waitingInfo    // latest aggregation result
	watchOpt WatchingConfig // watcher options resolved from config (re-injected on tick)
	// paneCaptures is the per-pane capture snapshot (tty mtime + buffer)
	// from the last watcher tick, fed into the next watchCmd so it can
	// (a) detect "stuck" panes (buffer unchanged across ticks) and
	// (b) skip re-capturing panes whose tty hasn't changed.
	paneCaptures map[paneKey]paneCapture
	// watchErrs counts consecutive watcher failures. Reset to 0 on a
	// successful tick. Drives the exponential backoff in watch.go so a
	// hung tmux server doesn't peg the CPU.
	watchErrs int
	// watchLastErr holds the most recent watcher error message (or
	// "" if the last tick succeeded). Surfaced in the status line so
	// the user can see when the watcher is struggling.
	watchLastErr string

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

	// sessionInfo is the most recent snapshot of session-name ->
	// sessionInfo (path, window/pane counts, meta). Refreshed on each
	// itemsMsg. Used by renderEntry for window/pane count display.
	sessionInfo map[string]sessionInfo

	// dupBasenames is the set of directory basenames that appear as
	// the cwd of more than one tmux session on this server. Computed
	// from sessionInfo on each itemsMsg; renderEntry consults it to
	// decide which rows need a path disambiguator appended, and
	// itemsMsg uses it to decide which m.items rows should carry
	// an internal hint path so connect() can pick the right session
	// when the user hits Enter.
	dupBasenames map[string]bool
	// entryHintPath is the cwd hint for m.items[i] when the row
	// is a tmux session whose directory basename collides with
	// another session's basename (e.g. two sessions whose cwd ends
	// in "/foo"). The hint lets choose() forward the exact target
	// to connect() so the right session is picked. Empty string
	// means "no hint; m.items[i] is its own name."
	entryHintPath map[int]string

	// showDetail toggles a "per-pane detail panel" rendered inline
	// under the cursor row when it's a waiting entry. Toggled by
	// Ctrl-Space (a free binding in the current keymap).
	showDetail bool

	// pendingKill holds the entry the user pressed Ctrl-d on once;
	// the kill only happens when Ctrl-d is pressed a second time on
	// the same entry. Reset on any cursor move or filter change.
	pendingKill string

	// marked is the set of filtered indices that have been toggled
	// with Space for multi-select batch operations. Reset on source
	// reload or mode change.
	marked map[int]bool

	// tagFilter is the currently active tag filter. When non-empty,
	// only entries that have this tag are shown. Set by the tag
	// selection mode (Ctrl-,). Cleared by pressing Ctrl-, again or
	// Esc.
	tagFilter string
	// groupFilter is the currently active group filter. When
	// non-empty, only entries that have this group are shown. Set
	// by the group selection mode (Ctrl-;). Cleared by pressing
	// Ctrl-; again or Esc.
	groupFilter string

	// final selection, consumed by main after the program quits
	result            string // final selection (directory, session, or branch)
	resultPaneID      string
	resultCommand     string // chosen command from the global command palette
	resultToggleClose bool   // true if user pressed alt+q to close and switch to last session
	resultServer      string // server name from a "[server] session" entry in all-servers mode; "" otherwise
	resultHintPath    string // path hint from a "name\t<path>" picker entry; "" otherwise
	openWithAgent     bool   // true if chosen via Alt-v

	// preview panel state
	previewEntry   string
	previewContent string
	previewCache   map[string]previewCacheEntry
	// previewOffset is the number of lines scrolled off the TOP of
	// the preview panel. 0 = top of the preview content. Independent
	// of the list cursor so the user can scroll a long preview
	// without losing their place in the session list. Reset to 0
	// whenever the preview entry changes.
	previewOffset int

	// configMtime is the mtime of the config file at the last
	// successful hot-reload check. Compared against the file's
	// current mtime on each configCheckTickCmd tick. Zero means
	// "no file is tracked, skip the check".
	configMtime time.Time

	// autoSaveEvery is the resurrect auto-save interval resolved from
	// config. Zero disables periodic auto-save.
	autoSaveEvery time.Duration

	// keymap translates a pressed key to its canonical key before the
	// handleKey switch, implementing the [keybindings] config overrides.
	// nil when no overrides are configured (the common case).
	keymap map[string]string

	// inputHistory is the persisted list of previously submitted input
	// texts (oldest first); historyPos is the recall cursor into it.
	// historyPos == len(inputHistory) means "not currently recalling".
	inputHistory []string
	historyPos   int

	// recentCache is a memoized copy of recent.json, refreshed on
	// init and on every recordTouch. refilter's hot path reads this
	// instead of hitting the disk on every keystroke.
	recentCache recentFile

	// floatWaiting mirrors [waiting] float_to_top: when set, sessions
	// with a waiting agent sort just under pinned entries in the
	// default/all list.
	floatWaiting bool

	// killedUndo holds snapshots of the most recently killed session(s)
	// so Alt-u can recreate them (name, path, window/pane layout, and
	// allowed running programs). Captured just before kill-session.
	killedUndo []ResurrectSession

	// fuzzy is the reusable fzf matcher (slab + Chars cache) used by
	// refilter. Lazily initialized so models built as bare literals in
	// tests still work.
	fuzzy *fuzzyEngine
}

const configCheckInterval = 5 * time.Second

type configReloadMsg struct {
	cfg   Config
	mtime time.Time
}

// configCheckTickCmd schedules the next config mtime check. The
// returned tea.Cmd fires a configCheckTickMsg after the interval;
// the model decides whether to actually stat the file or trigger a
// reload.
func configCheckTickCmd() tea.Cmd {
	return tea.Tick(configCheckInterval, func(time.Time) tea.Msg {
		return configCheckTickMsg{}
	})
}

type configCheckTickMsg struct{}

// resurrectAutoSaveMsg fires on the resurrect auto-save interval.
type resurrectAutoSaveMsg struct{}

func resurrectAutoSaveTickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return resurrectAutoSaveMsg{} })
}

// resurrectSaveCmd snapshots the workspace state in the background
// (it forks tmux), staying silent so periodic saves don't flash the
// tmux status line.
func resurrectSaveCmd() tea.Cmd {
	return func() tea.Msg {
		_ = doResurrectSave(false)
		return nil
	}
}

// newModel builds the initial TUI state. Optional positional args:
//
//	themeWatch  — enable live terminal-bg watching via watchTheme()
//	restore     — load the cached "last view" and apply it, so a
//	              reopen within lastViewTTL resumes the previous list
//	              state. Fast-path CLI flags (--last etc.) pass false.
//
// We keep the variadic signature so existing call sites that pass
// just `themeWatch` (and tests that pass nothing) keep compiling.
func newModel(opts ...bool) model {
	themeWatch := false
	if len(opts) > 0 {
		themeWatch = opts[0]
	}
	restore := false
	if len(opts) > 1 {
		restore = opts[1]
	}
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
	hist := loadInputHistory()
	m := model{
		input:          ti,
		src:            srcDefault,
		annots:         map[string]string{},
		dirty:          map[string]bool{},
		height:         24,
		width:          80,
		styles:         newStyleBundle(cfg.Style),
		resolvedCfg:    cfg,
		watchOpt:       cfg.toWatchingConfig().compilePatterns(),
		waiting:        w,
		previewCache:   map[string]previewCacheEntry{},
		pinned:         loadPinned(),
		currentSession: currentSessionName(),
		currentPath:    attachedSessionPath(),
		themeWatch:     themeWatch,
		autoSaveEvery:  parseDuration(cfg.Resurrect.AutoSaveInterval, 0),
		keymap:         buildKeymap(cfg),
		inputHistory:   hist,
		historyPos:     len(hist),
		recentCache:    loadRecent(),
		floatWaiting:   cfg.Waiting.FloatToTop,
		fuzzy:          newFuzzyEngine(),
	}
	// Restore the previously-seen list state if the caller opted
	// in. We do this AFTER the default m is fully populated so a
	// missing cache can never leave the model in a half-built
	// state. Items / filtered / marked are intentionally NOT
	// restored — they are recomputed from the source in Init().
	//
	// The log lines below are diagnostic: if the 60s restore ever
	// seems to not work, running the binary directly (not via
	// `make build` without reinstalling) shows whether the cache
	// was found, accepted, and applied. If you see "cache missing
	// or expired" right after a successful exit, the most likely
	// cause is a stale binary in $PATH — the popup child
	// inherited the OLD code that did not write the cache.
	if restore {
		if lv, ok := loadLastView(); ok {
			log.Printf("tmux-qs: restoring last view from %s: src=%v input=%q cursor=%d mode=%v",
				lastViewPath(), sourceKind(lv.Src), lv.Input, lv.Cursor, uiMode(lv.Mode))
			applyLastView(&m, lv)
		} else {
			log.Printf("tmux-qs: no last view to restore (cache missing or expired): %s", lastViewPath())
		}
	}
	return m
}

func (m model) Init() tea.Cmd {
	// Only kick off the initial load and self-pane probe here. The watcher
	// is started by the selfPaneMsg handler so its first tick has the
	// correct self-pane exclusion (avoids a race where the popup's own
	// pane is briefly counted as waiting).
	//
	// Load the source the model is currently set to — usually
	// srcDefault for a fresh launch, but applyLastView may have
	// restored a previous src (e.g. srcTmux) when the picker is
	// reopened within lastViewTTL. We must read m.src, NOT
	// hardcode srcDefault, otherwise the restored view is
	// immediately clobbered by the itemsMsg handler's
	// `m.src = msg.src` reassignment.
	cmds := []tea.Cmd{loadCmd(m.src), selfPaneCmd(), configCheckTickCmd()}
	if m.themeWatch {
		cmds = append(cmds, watchTheme())
	}
	if m.autoSaveEvery > 0 {
		cmds = append(cmds, resurrectAutoSaveTickCmd(m.autoSaveEvery))
	}
	return tea.Batch(cmds...)
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
		if m.themeWatch {
			// tmux-set-background 依 client 寬度切換主題，resize 後立即重查
			return m, checkThemeNow()
		}
		return m, nil

	case msgThemeChecked:
		var cmds []tea.Cmd
		if msg.fromTick && m.themeWatch {
			cmds = append(cmds, watchTheme())
		}
		if applyTheme(msg) {
			cmds = append(cmds, tea.ClearScreen)
		}
		return m, tea.Batch(cmds...)

	case itemsMsg:
		m.mode = modeList
		m.src = msg.src
		// Reset per-source caches so stale data from a previous
		// source can't bleed into the new list's sort. m.annots
		// is the "is git" signal for the directory group — if
		// we don't clear it here, a session name that happened
		// to coincide with a path from the previous source
		// would inherit the previous source's git annotation.
		// annotateCmd is fired in the same batch below, so the
		// annotations will be repopulated within a few hundred
		// milliseconds.
		m.annots = map[string]string{}
		m.dirty = map[string]bool{}
		items := msg.items
		// zoxide scores are used as the directory-group primary
		// sort key in the default/all list view, so load them
		// regardless of which source was selected. The same map
		// is also used by the fuzzy-score tie-break when the
		// user types a query into the search box. zoxide may
		// not be installed — loadZoxide returns nil for the
		// scores map in that case, and downstream code treats
		// "no zoxide score" as "fall back to recent.json".
		_, scores, _ := loadZoxide("", buildExcludedSessionPaths())
		m.zoxideScores = scores
		if msg.src == srcDefault || msg.src == srcAll {
			items = m.recentCache.orderedByFrecency(items)
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
		m.sessionInfo = make(map[string]sessionInfo, len(info))
		for name, si := range info {
			m.sessionPaths[name] = si.path
			m.sessionMeta[name] = si.meta
			m.sessionInfo[name] = si
		}
		m.dupBasenames = findDuplicateBasenames(info)
		// For tmux sessions whose cwd basename collides with another
		// session's, record an internal hint path so choose() and
		// connect() can pick the exact one the user pointed at.
		// Non-colliding rows get no hint, so every existing caller of
		// m.items[idx] / m.sessionPaths keeps working unchanged.
		m.entryHintPath = make(map[int]string)
		for i, name := range m.items {
			si, ok := m.sessionInfo[name]
			if !ok || si.path == "" {
				continue
			}
			if !m.dupBasenames[filepath.Base(si.path)] {
				continue
			}
			m.entryHintPath[i] = si.path
		}
		m.refilter()
		return m, tea.Batch(annotateCmd(m.items, m.sessionPaths), dirtyCmd(m.items, m.sessionPaths), m.updatePreviewCmd())

	case annotMsg:
		for k, v := range msg {
			m.annots[k] = v
		}
		// m.annots is the source of truth for the "is git"
		// sub-group in the directory sort. Until the first
		// annotMsg arrives (and on every source switch —
		// itemsMsg clears m.annots), the directory group sorts
		// by zoxide score alone. Re-running refilter here
		// applies the git-first ordering now that the data is
		// in place. refilter is cheap (purely in-memory sort
		// over m.filtered), so the re-paint is invisible.
		m.refilter()
		return m, nil

	case dirtyMsg:
		for k := range msg {
			m.dirty[k] = true
		}
		return m, nil

	case selfPaneMsg:
		m.selfPane = msg.key
		return m, watchCmd(m.selfPane, m.watchOpt, m.paneCaptures, nextWatchDelay(m.watchOpt, m.watchErrs), m.watchErrs, m.pinned)

	case watchMsg:
		if msg.err != nil {
			m.watchErrs = msg.consecutiveErrs
			m.watchLastErr = msg.err.Error()
			return m, watchCmd(m.selfPane, m.watchOpt, m.paneCaptures, nextWatchDelay(m.watchOpt, m.watchErrs), m.watchErrs, m.pinned)
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
		m.paneCaptures = msg.captures
		m.watchErrs = 0
		m.watchLastErr = ""
		next := watchCmd(m.selfPane, m.watchOpt, m.paneCaptures, nextWatchDelay(m.watchOpt, 0), 0, m.pinned)
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

	case configCheckTickMsg:
		// Re-check the config file's mtime. If it changed, kick off
		// a reload. Always re-schedule the next tick.
		return m, tea.Batch(configReloadCmd(m.configMtime), configCheckTickCmd())

	case resurrectAutoSaveMsg:
		if m.autoSaveEvery <= 0 {
			return m, nil
		}
		return m, tea.Batch(resurrectSaveCmd(), resurrectAutoSaveTickCmd(m.autoSaveEvery))

	case configReloadMsg:
		m.configMtime = msg.mtime
		// If a non-zero mtime came back, the file is tracked and
		// (if changed) the config was reloaded. Re-derive the things
		// that depend on config: styles, watcher options.
		if !msg.mtime.IsZero() {
			m.styles = newStyleBundle(msg.cfg.Style)
			m.resolvedCfg = msg.cfg
			m.watchOpt = msg.cfg.toWatchingConfig().compilePatterns()
			m.keymap = buildKeymap(msg.cfg)
			m.floatWaiting = msg.cfg.Waiting.FloatToTop
			// Pick up auto-save interval changes. If it was disabled and
			// is now enabled, start the ticker; if disabled, the tick
			// handler stops rescheduling on its own.
			wasOff := m.autoSaveEvery <= 0
			m.autoSaveEvery = parseDuration(msg.cfg.Resurrect.AutoSaveInterval, 0)
			if wasOff && m.autoSaveEvery > 0 {
				return m, resurrectAutoSaveTickCmd(m.autoSaveEvery)
			}
		}
		return m, nil

	case filesMsg:
		m.loading = false
		m.items = msg.files
		m.errText = ""
		m.input.SetValue("")
		m.refilter()
		return m, nil

	case pinMsg:
		if m.pinned == nil {
			m.pinned = make(map[string]bool)
		}
		if msg.pinned {
			m.pinned[msg.entry] = true
		} else {
			delete(m.pinned, msg.entry)
		}
		m.refilter()
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
	// In vimNormal mode the textinput is disabled — keys are
	// interpreted as vim commands, not as input text. Skip the
	// input.Update to prevent stray characters from leaking in.
	if m.vimMode == vimNormal {
		return m, nil
	}
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) reload(src sourceKind) (tea.Model, tea.Cmd) {
	m.loading = true
	m.input.SetValue("")
	m.src = src
	m.marked = nil
	m.tagFilter = ""
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

// showWindows swaps the list to the windows of a single session, each row
// carrying the window's active pane id so Enter jumps straight to it
// (reusing the same select-pane path as the Ctrl-e pane view). Forks tmux
// once to read the window/pane layout.
func (m model) showWindows(session string) (tea.Model, tea.Cmd) {
	session = strings.TrimSpace(session)
	lines, err := tmuxRunLines("list-panes", "-s", "-t", session, "-F",
		"#{window_index}\t#{window_name}\t#{pane_active}\t#{pane_id}\t#{pane_current_command}")
	if err != nil || len(lines) == 0 {
		m.errText = "no windows for this session"
		return m, nil
	}
	type win struct {
		index    int
		name     string
		activePn string
		firstPn  string
		cmds     []string
		seen     map[string]bool
	}
	var order []int
	wins := map[int]*win{}
	for _, l := range lines {
		p := strings.Split(l, "\t")
		if len(p) < 5 {
			continue
		}
		idx, _ := strconv.Atoi(p[0])
		w, ok := wins[idx]
		if !ok {
			w = &win{index: idx, name: p[1], seen: map[string]bool{}}
			wins[idx] = w
			order = append(order, idx)
		}
		if w.firstPn == "" {
			w.firstPn = p[3]
		}
		if p[2] == "1" {
			w.activePn = p[3]
		}
		if cmd := p[4]; cmd != "" && !w.seen[cmd] {
			w.seen[cmd] = true
			w.cmds = append(w.cmds, cmd)
		}
	}
	sort.Ints(order)
	var items []string
	for _, idx := range order {
		w := wins[idx]
		pane := w.activePn
		if pane == "" {
			pane = w.firstPn
		}
		display := fmt.Sprintf("%-3d %-20s [%s]", w.index, w.name, strings.Join(w.cmds, ", "))
		// display \t session \t paneID — same envelope as srcPanes so
		// choose() can reuse the pane-jump branch.
		items = append(items, fmt.Sprintf("%s\t%s\t%s", display, session, pane))
	}
	m.src = srcWindows
	m.mode = modeList
	m.items = items
	m.input.SetValue("")
	m.errText = ""
	m.loading = false
	m.marked = nil
	m.refilter()
	return m, m.updatePreviewCmd()
}

func (m model) choose() (tea.Model, tea.Cmd) {
	if len(m.filtered) == 0 {
		return m, nil
	}
	idx := m.filtered[m.cursor]
	if m.mode == modeTag {
		m.tagFilter = strings.TrimSpace(m.items[idx])
		m.items = m.savedItems
		m.mode = modeList
		m.input.SetValue("")
		m.refilter()
		return m, nil
	}
	if m.mode == modeGroup {
		m.groupFilter = strings.TrimSpace(m.items[idx])
		m.items = m.savedItems
		m.mode = modeList
		m.input.SetValue("")
		m.refilter()
		return m, nil
	}
	if m.mode == modeAgentSelect {
		selected := strings.TrimSpace(m.items[idx])
		if m.templateMode {
			dir := entryDir(m.agentSelectTarget, m.sessionPaths)
			if dir != "" {
				applyTemplateByName(selected, m.agentSelectTarget, dir)
			}
			m.items = m.savedItems
			m.mode = modeList
			m.agentSelectTarget = ""
			m.templateMode = false
			m.input.SetValue("")
			m.refilter()
			return m, nil
		}
		m.result = m.agentSelectTarget
		m.selectedAgent = selected
		m.openWithAgent = true
		return m, tea.Quit
	}
	if m.mode == modeBranch {
		m.loading = true
		return m, jumpBranchCmd(m.repo, m.branches[idx])
	}
	selected := strings.TrimSpace(m.items[idx])
	if m.src == srcCommands {
		m.resultCommand = selected
		return m, tea.Quit
	}
	if m.src == srcPanes || m.src == srcWindows || m.src == srcTmux {
		parts := strings.Split(m.items[idx], "\t")
		if len(parts) >= 3 {
			m.result = parts[1]
			m.resultPaneID = parts[2]
			return m, tea.Quit
		}
	}
	if m.src == srcFiles {
		m.result = filepath.Join(m.fileSearchDir, selected)
		return m, tea.Quit
	}
	// Record the selection so the next TUI can bubble it up in
	// the recency-sorted list view. Only record for entries that
	// are real tmux sessions (i.e. not arbitrary paths).
	if _, ok := m.sessionPaths[selected]; ok {
		m.recentCache = loadRecent().recordTouch(selected)
		m.recentCache.save()
	}
	// In all-servers mode, entries have the form "[server] session".
	// Extract the server so the connect path can target the right
	// server. The bare session name is what we pass to connect().
	if server, bare := sessionServer(selected); server != "" {
		m.resultServer = server
		selected = bare
	}
	// If the picker entry carries a "\t<path>" hint (i.e. the session
	// had a colliding basename with another session), forward the
	// hint to connect() so it can pick the exact one. The chosen
	// name is still the user-facing session name.
	if hint, ok := m.entryHintPath[idx]; ok {
		m.resultHintPath = hint
	}
	m.result = selected
	// If there is a waiting pane for this session, save its paneID!
	// Skip this lookup for sources whose rows already carry an exact
	// paneID in their tab envelope (srcPanes, srcWindows, srcTmux):
	// those rows have a more precise target than "any waiting pane
	// in this session" and shouldn't be hijacked.
	if m.src != srcPanes && m.src != srcWindows && m.src != srcTmux {
		for _, wp := range m.waiting.panes {
			if wp.session == selected {
				m.resultPaneID = wp.paneID
				break
			}
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

// cfg returns the model's current resolved Config. Convenience for
// the renderer, which would otherwise have to call loadConfig() on
// every frame and pay for a mutex acquire.
func (m model) cfg() Config { return m.resolvedCfg }

// captureForUndo snapshots the named sessions (those still alive) into the
// kill-undo buffer. Call it immediately BEFORE kill-session so the layout
// is still readable.
func (m *model) captureForUndo(names []string) {
	var snaps []ResurrectSession
	for _, n := range names {
		if s, ok := captureResurrectSession(strings.TrimSpace(n)); ok {
			snaps = append(snaps, s)
		}
	}
	m.killedUndo = snaps
}

// undoKillCmd recreates the sessions captured in the undo buffer.
func undoKillCmd(snaps []ResurrectSession) tea.Cmd {
	return func() tea.Msg {
		allowed := restoreProgramSet()
		for _, s := range snaps {
			restoreSession(s, allowed)
		}
		return nil
	}
}

// recordInput persists text to the input history and resets the recall
// cursor to the end (so the next Ctrl-Up starts from the newest entry).
func (m *model) recordInput(text string) {
	m.inputHistory = appendInputHistory(m.inputHistory, text)
	m.historyPos = len(m.inputHistory)
}

// recallHistory moves the recall cursor by delta (-1 = older, +1 = newer)
// and loads that entry into the input box. Moving past the newest entry
// clears the input. No-op when history is empty.
func (m *model) recallHistory(delta int) {
	if len(m.inputHistory) == 0 {
		return
	}
	pos := m.historyPos + delta
	if pos < 0 {
		pos = 0
	}
	if pos >= len(m.inputHistory) {
		// Past the newest entry: clear the input ("live" line).
		m.historyPos = len(m.inputHistory)
		m.input.SetValue("")
		m.input.CursorEnd()
		m.refilter()
		return
	}
	m.historyPos = pos
	m.input.SetValue(m.inputHistory[pos])
	m.input.CursorEnd()
	m.refilter()
}

func (m *model) move(delta int) {
	if len(m.filtered) == 0 {
		return
	}
	// Wrap around at list boundaries — pressing Up on the first row
	// wraps to the last row (which displays the current workspace/directory),
	// and pressing Down on the last row wraps to the first row.
	next := m.cursor + delta
	if next < 0 {
		next = len(m.filtered) - 1
	} else if next >= len(m.filtered) {
		next = 0
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

// entryTextMemo returns a memoizing wrapper around entryText. The sort
// comparators call entryText O(n log n) times; for srcPanes/srcWindows
// that means repeated SplitN + allocation per comparison. The memo
// computes each entry's display text at most once per refilter. Not safe
// for concurrent use — only the single-threaded sequential match path and
// the (single-threaded) comparators call it.
func (m *model) entryTextMemo() func(int) string {
	cache := make(map[int]string)
	return func(i int) string {
		if s, ok := cache[i]; ok {
			return s
		}
		s := m.entryText(i)
		cache[i] = s
		return s
	}
}

func (m *model) entryText(i int) string {
	if m.mode == modeBranch {
		return m.branches[i].name
	}
	item := m.items[i]
	if m.src == srcPanes || m.src == srcWindows || m.src == srcTmux {
		return strings.SplitN(item, "\t", 2)[0]
	}
	return item
}

// compositeEntryText is the text fed to the fuzzy engine for
// matching. For sources that carry an associated git branch
// (currently srcAll / srcDefault, which call loadAllSources and
// trigger annotateCmd), we append "  " + branch so the user's
// query can also match the branch — typing "main" then finds
// every repo on the main branch. The match indices stored in
// m.matchIdx are interpreted relative to this composite string;
// renderEntry's highlight path maps them back to either the
// rawItem or the branch segment when highlighting.
//
// When m.annots has not yet been populated (the first paint
// before annotMsg arrives, or an entry with no .git directory
// at any ancestor), the branch segment is empty and
// compositeEntryText returns just the item — matching the
// pre-existing behavior of entryText. This means the user
// sees one paint with item-only matching, then a second
// paint (driven by the existing annotMsg handler that calls
// refilter) where branch matches start showing up.
//
// Non-all sources are unaffected: srcTmux, srcWindows, srcPanes
// already embed the cmd in the item string and ignore the
// annots map; srcZoxide / srcConfigs / srcFind don't carry
// branch info, so there's nothing to extend.
func (m *model) compositeEntryText(i int) string {
	if m.mode == modeBranch {
		return m.branches[i].name
	}
	item := m.items[i]
	if m.src != srcAll && m.src != srcDefault {
		return item
	}
	if br, ok := m.annots[item]; ok && br != "" {
		return item + "  " + br
	}
	return item
}

// compositeEntryTextMemo is the memoizing wrapper that
// refilter's sequential path uses. Mirrors entryTextMemo; the
// only difference is that it reads from compositeEntryText so
// the fuzzy engine sees the branch-augmented text. The memo
// is keyed on the entry's raw item (the i-th call always
// produces the same item) plus m.annots, so it stays correct
// across the second refilter triggered by annotMsg.
func (m *model) compositeEntryTextMemo() func(int) string {
	var lastI = -1
	var last string
	return func(i int) string {
		if i == lastI {
			return last
		}
		lastI = i
		last = m.compositeEntryText(i)
		return last
	}
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
//
// When tagFilter is non-empty, only entries that have that tag are
// included in the filtered list.
//
// The match indices returned by fuzzyScore (positions of matched
// characters in the entry text) are stored on the model so the
// renderer can highlight them.
func (m *model) refilter() {
	query := m.input.Value()
	m.filtered = m.filtered[:0]
	if m.matchIdx == nil {
		m.matchIdx = make(map[int][]int)
	} else {
		// Clear stale entries from the previous refilter.
		for k := range m.matchIdx {
			delete(m.matchIdx, k)
		}
	}
	hasQuery := strings.TrimSpace(query) != ""
	if m.fuzzy == nil {
		m.fuzzy = newFuzzyEngine()
	}
	n := m.entryCount()
	// Use compositeEntryText (not entryText) so the fuzzy
	// engine sees the branch appended to each item in srcAll
	// / srcDefault mode. The match indices returned are
	// relative to this composite string; renderEntry splits
	// them back into item-segment / branch-segment highlights.
	et := m.compositeEntryTextMemo()

	var scores []int
	// include applies the tag/group filters and, if the entry survives,
	// appends it to the filtered set. Shared by the sequential and
	// parallel scoring paths so the filter logic lives in one place.
	include := func(i, score int, ok bool, idx []int) {
		if !ok {
			return
		}
		if m.tagFilter != "" && m.mode == modeList {
			entry := strings.TrimSpace(m.items[i])
			hasTag := false
			for _, t := range m.entryTags(entry) {
				if t == m.tagFilter {
					hasTag = true
					break
				}
			}
			if !hasTag {
				return
			}
		}
		if m.groupFilter != "" && m.mode == modeList {
			entry := strings.TrimSpace(m.items[i])
			if entryGroup(entry) != m.groupFilter {
				return
			}
		}
		m.filtered = append(m.filtered, i)
		if hasQuery {
			scores = append(scores, score)
			m.matchIdx[i] = idx
		}
	}

	if hasQuery && n >= fuzzyParallelThreshold {
		// Large list: score in parallel. Each worker owns a slab and
		// builds its own Chars (the shared engine isn't concurrency
		// safe). Results are assembled in item order below so the
		// output is identical to the sequential path.
		type matchResult struct {
			ok    bool
			score int
			idx   []int
		}
		results := make([]matchResult, n)
		workers := runtime.GOMAXPROCS(0)
		if workers < 1 {
			workers = 1
		}
		chunk := (n + workers - 1) / workers
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			lo := w * chunk
			if lo >= n {
				break
			}
			hi := lo + chunk
			if hi > n {
				hi = n
			}
			wg.Add(1)
			go func(lo, hi int) {
				defer wg.Done()
				slab := util.MakeSlab(fzfSlab16Size, fzfSlab32Size)
				for i := lo; i < hi; i++ {
					score, ok, idx := fuzzyScoreChars(util.ToChars([]byte(m.compositeEntryText(i))), query, slab)
					results[i] = matchResult{ok, score, idx}
				}
			}(lo, hi)
		}
		wg.Wait()
		for i := 0; i < n; i++ {
			score := results[i].score
			if m.src == srcTmux {
				score += sessionNameBonusFor(m.items[i], query)
			}
			include(i, score, results[i].ok, results[i].idx)
		}
	} else {
		for i := 0; i < n; i++ {
			score, ok, idx := m.fuzzy.score(et(i), query)
			if m.src == srcTmux {
				score += sessionNameBonusFor(m.items[i], query)
			}
			include(i, score, ok, idx)
		}
	}
	if hasQuery && len(m.filtered) > 1 {
		order := make([]int, len(m.filtered))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			scoreA := scores[order[a]]
			scoreB := scores[order[b]]
			if scoreA != scoreB {
				return scoreA > scoreB
			}
			// Use raw item (not composite) for the zoxide /
			// recency tie-breaks. See the comment in the
			// sortTier block above for why.
			textA := strings.TrimSpace(m.items[order[a]])
			textB := strings.TrimSpace(m.items[order[b]])
			if m.zoxideScores != nil {
				zoxA, okA := m.zoxideScores[textA]
				zoxB, okB := m.zoxideScores[textB]
				if okA != okB {
					return okA
				}
				if okA && zoxA != zoxB {
					return zoxA > zoxB
				}
			}
			recent := m.recentCache
			recA, okA := recencyOf(textA, m.sessionInfo, recent)
			recB, okB := recencyOf(textB, m.sessionInfo, recent)
			if okA != okB {
				return okA
			}
			if okA && recA != recB {
				return recA > recB
			}
			return false
		})
		sorted := make([]int, len(m.filtered))
		for i, o := range order {
			sorted[i] = m.filtered[o]
		}
		copy(m.filtered, sorted)
	}
	if len(m.filtered) > 1 {
		// Final ordering. Three independent keys, applied in order:
		//
		//   1. Tier   — pinned (0), waiting when floatWaiting is on
		//               (1), normal (2), current session/path (3).
		//               Current is always last; pinned is always first.
		//   2. Group  — within tier 2 (normal), tmux sessions
		//               rank ahead of directories. This is the
		//               user-facing contract: "tmux session (most
		//               recently used first), then directories
		//               (highest zoxide score first), then the
		//               current session".
		//   3. Group-internal key —
		//      - tmux sessions:  #{session_activity} (via recencyOf)
		//      - directories:    zoxide score, with recent.json as
		//                        fallback when zoxide has no record
		//                        (e.g. configured sessions, or
		//                        zoxide paths not currently in the
		//                        zoxide list).
		//
		// sort.SliceStable preserves the pre-existing order for
		// entries that tie on every key, so the "itemsMsg" pass's
		// orderedByFrecency layout still leaks through for entries
		// that have no signal at all on any of the three keys.
		sort.SliceStable(m.filtered, func(i, j int) bool {
			// Use the raw item (not the composite entry text)
			// for sort keys. The composite (item + "  " +
			// branch) is only meant for fuzzy matching; sort
			// keys like zoxideScores / recencyOf / isGitEntry
			// are keyed on the raw item string, so passing a
			// branch-suffixed name would miss every lookup
			// and produce wrong ordering. The fuzzy-score
			// step above (which uses the composite) is
			// unaffected — that runs once at line 1299, this
			// is a separate later pass.
			nameI := strings.TrimSpace(m.items[m.filtered[i]])
			nameJ := strings.TrimSpace(m.items[m.filtered[j]])
			tierI := m.sortTier(nameI)
			tierJ := m.sortTier(nameJ)
			if tierI != tierJ {
				return tierI < tierJ
			}
			// Tier 0/1 (pinned / waiting) and tier 3 (current)
			// don't have the user-facing "tmux → directories"
			// contract — fall through to a flat recency sort
			// within those tiers, just like before this change.
			if tierI != 2 {
				recI, okI := recencyOf(nameI, m.sessionInfo, m.recentCache)
				recJ, okJ := recencyOf(nameJ, m.sessionInfo, m.recentCache)
				if okI != okJ {
					return okI
				}
				if okI && recI != recJ {
					return recI > recJ
				}
				return false
			}
			groupI := m.entryGroup(nameI)
			groupJ := m.entryGroup(nameJ)
			if groupI != groupJ {
				return groupI < groupJ
			}
			switch groupI {
			case groupTmuxSession:
				recI, okI := recencyOf(nameI, m.sessionInfo, m.recentCache)
				recJ, okJ := recencyOf(nameJ, m.sessionInfo, m.recentCache)
				if okI != okJ {
					return okI
				}
				if okI && recI != recJ {
					return recI > recJ
				}
				return false
			case groupDirectory:
				// Sub-group 1: git directories. Sub-group 2:
				// non-git. Strict grouping — a git entry with
				// the lowest possible zoxide score still wins
				// over a non-git entry with the highest. The
				// git signal is sourced from m.annots
				// (populated by annotateCmd's branchOfDir
				// pass); see isGitEntry.
				gitI := m.isGitEntry(nameI)
				gitJ := m.isGitEntry(nameJ)
				if gitI != gitJ {
					return gitI
				}
				// Within the same isGit sub-group: zoxide
				// score first, recent.json as fallback.
				zoxI, hasZoxI := m.zoxideScore(nameI)
				zoxJ, hasZoxJ := m.zoxideScore(nameJ)
				if hasZoxI != hasZoxJ {
					return hasZoxI
				}
				if hasZoxI && zoxI != zoxJ {
					return zoxI > zoxJ
				}
				// Same zoxide status: fall back to recent.json
				// so a frequently-picked configured session
				// beats a never-picked one with the same zoxide
				// ranking.
				lastI, okI := recentFromRecent(nameI, m.recentCache)
				lastJ, okJ := recentFromRecent(nameJ, m.recentCache)
				if okI != okJ {
					return okI
				}
				if okI && lastI != lastJ {
					return lastI > lastJ
				}
				return false
			}
			return false
		})
	}
	// Clamp the cursor to the new filtered list. We do NOT
	// unconditionally reset to 0 here: that was the previous
	// behavior, but it silently clobbered the cursor that
	// applyLastView restored (see TestLastViewEndToEnd_...).
	// Clamping preserves the cursor whenever it's still in
	// range, and falls back to 0 only when the new list is
	// shorter (or empty).
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.offset = 0
	m.pendingKill = ""
	m.recalcInputPad()
}

// sortTier returns the ordering tier for an entry: 0 pinned (top),
// 1 waiting agent (when floatWaiting is on for this source), 2 normal,
// 3 current session/path (bottom). Lower tiers sort earlier.
func (m *model) sortTier(name string) int {
	isCurrent := (m.currentSession != "" && name == m.currentSession) ||
		(m.currentPath != "" && (name == m.currentPath || expandPath(name) == m.currentPath))
	switch {
	case isCurrent:
		return 3
	case m.pinned[name]:
		return 0
	case m.floatWaiting && (m.src == srcDefault || m.src == srcAll) && m.isWaitingEntry(name):
		return 1
	default:
		return 2
	}
}

// isWaitingEntry reports whether the entry currently has a waiting agent
// according to the latest watcher snapshot.
func (m *model) isWaitingEntry(name string) bool {
	return len(m.waiting.lookup(strings.TrimSpace(name))) > 0
}

// entryGroupKind classifies a list entry into one of the two
// user-facing sort groups that apply within the normal tier
// (sortTier == 2). Pinned, waiting, and current-session tiers
// don't use this grouping — see refilter's end-sort for the
// full decision tree.
type entryGroupKind int

const (
	// groupTmuxSession: the entry is a currently running tmux
	// session, identified by being present in m.sessionInfo. In
	// the default list these bubble to the top of the normal
	// tier, sorted by #{session_activity} (most recent first).
	groupTmuxSession entryGroupKind = iota
	// groupDirectory: anything that isn't a tmux session —
	// configured sessions, zoxide paths, in --all-servers mode
	// the synthetic "[server] name" entries that didn't resolve
	// to a running session. Sorted by zoxide score, with
	// recent.json as fallback.
	groupDirectory
)

// entryGroup returns the group that name belongs to for the
// group-sort step. It uses m.sessionInfo as the source of truth
// (populated from `tmux list-sessions`). All-servers mode entries
// have the "[server] " prefix; the bare name is extracted via
// sessionServer before lookup so the prefix doesn't fool the
// classification.
func (m *model) entryGroup(name string) entryGroupKind {
	if m.isTmuxSessionEntry(name) {
		return groupTmuxSession
	}
	return groupDirectory
}

// isTmuxSessionEntry reports whether name corresponds to a
// currently running tmux session, as far as the model knows. The
// check is m.sessionInfo lookup with the all-servers prefix
// stripped. A configured session whose path happens to match a
// running tmux session path will be classified as a tmux session
// here (and surface the session's lastActivity through the
// recencyOf path), which is the right behavior — the user is
// looking at the same workspace either way.
func (m *model) isTmuxSessionEntry(name string) bool {
	bare := name
	if _, b := sessionServer(name); b != "" {
		bare = b
	}
	_, ok := m.sessionInfo[bare]
	return ok
}

// zoxideScore returns the zoxide frecency score for name, if any.
// Returns (0, false) when the entry has no zoxide record — either
// zoxide isn't installed (m.zoxideScores is nil) or the entry is
// a configured session that was never zoxide'd. The directory
// group uses this as its primary sort key.
func (m *model) zoxideScore(name string) (float64, bool) {
	if m.zoxideScores == nil {
		return 0, false
	}
	s, ok := m.zoxideScores[name]
	return s, ok
}

// isGitEntry reports whether name corresponds to a directory that
// lives inside a git repository. The signal is sourced from
// m.annots, populated by annotateCmd's resolveBranches pass via
// branchOfDir — that helper already walks up the directory tree
// looking for a .git entry, so a non-empty branch name implies
// "yes, this is a git directory". The directory group uses this
// as the primary sub-group key: git directories outrank non-git
// directories regardless of zoxide score.
//
// Trade-off: on the first paint, before annotMsg has arrived,
// the result is always false (m.annots is empty). The annotMsg
// handler triggers a re-refilter so the final git-first ordering
// applies within a few hundred milliseconds of the picker
// opening. itemsMsg also clears m.annots on each source switch
// so stale annotations from a previous source can't bleed into
// the new list's ordering.
func (m *model) isGitEntry(name string) bool {
	return m.annots[name] != ""
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
		m.previewOffset = 0
		return nil
	}
	// When the previewed entry changes, reset the scroll offset so
	// the user sees the top of the new preview. (Same entry, cursor
	// moved within list: keep the offset so the user doesn't lose
	// their place in a long preview.)
	if m.previewEntry != sel {
		m.previewOffset = 0
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

// configReloadCmd returns a tea.Cmd that calls reloadConfigIfStale
// in a background goroutine and delivers the result as a
// configReloadMsg. Used by the hot-reload tick to detect edits to
// config.toml and update derived state (styles, watch options)
// without restarting the TUI.
func configReloadCmd(_ time.Time) tea.Cmd {
	return func() tea.Msg {
		cfg, mtime := reloadConfigIfStale()
		return configReloadMsg{cfg: cfg, mtime: mtime}
	}
}

// scrollPreview moves the preview pane up (delta < 0) or down
// (delta > 0) by |delta| lines. The offset is clamped to [0, maxOffset]
// where maxOffset is the number of preview content lines that don't
// fit in the visible pane. If the preview is empty or the entry
// hasn't been loaded yet, this is a no-op.
func (m *model) scrollPreview(delta int) {
	if m.width < previewColumnMinWidth {
		// No preview pane visible — nothing to scroll.
		return
	}
	if m.previewContent == "" {
		return
	}
	maxOffset := previewMaxOffset(m.previewContent, m.listHeight())
	next := m.previewOffset + delta
	if next < 0 {
		next = 0
	}
	if next > maxOffset {
		next = maxOffset
	}
	m.previewOffset = next
}

// previewMaxOffset returns the maximum legal scroll offset for a
// preview with the given content rendered into a panel of height h.
// It's the number of "extra" content lines that don't fit in the
// visible area. A no-op return of 0 means the content already fits.
func previewMaxOffset(content string, height int) int {
	if height <= 0 {
		return 0
	}
	lines := strings.Split(content, "\n")
	total := len(lines)
	// Right column has its own width limit but height is bounded by
	// the list height shared with the left column. We just need to
	// know how many lines overflow.
	if total <= height {
		return 0
	}
	return total - height
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

		// 3. If it's a tmux session, show the currently active window's
		// active pane and its last 20 lines of capture buffer. Other
		// windows / panes are intentionally omitted — the preview is
		// meant to be a quick "what's happening right now" snapshot,
		// not a full session dump.
		if !looksLikePath(entry) {
			cpu, mem, procs := sessionResources(entry)
			if cpu > 0 || mem > 0 {
				lines = append(lines, "Resource Usage:")
				lines = append(lines, fmt.Sprintf("  CPU: %.1f%%, MEM: %.1f%% (%s)", cpu, mem, strings.Join(procs, ", ")))
				lines = append(lines, "")
			}
			paneLines, err := tmuxRunLines("list-panes", "-s", "-t", entry, "-F",
				"#{window_index}\t#{window_name}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}\t#{pane_active}\t#{window_active}\t#{pane_current_path}")
			if err == nil && len(paneLines) > 0 {
				type paneInfo struct {
					windowIdx  int
					windowName string
					paneIdx    int
					id         string
					cmd        string
					dir        string
				}
				var active *paneInfo

				for _, pl := range paneLines {
					parts := strings.Split(pl, "\t")
					if len(parts) < 8 {
						continue
					}
					wIdx, _ := strconv.Atoi(parts[0])
					wName := parts[1]
					pIdx, _ := strconv.Atoi(parts[2])
					pID := parts[3]
					pCmd := parts[4]
					pActive := parts[5] == "1"
					wActive := parts[6] == "1"
					pDir := parts[7]

					if !(wActive && pActive) {
						continue
					}
					active = &paneInfo{
						windowIdx:  wIdx,
						windowName: wName,
						paneIdx:    pIdx,
						id:         pID,
						cmd:        pCmd,
						dir:        pDir,
					}
					break
				}

				if active != nil {
					// Single capture-pane call: no goroutine / mutex /
					// wait group needed.
					pBuf, bufErr := tmuxRunOut("capture-pane", "-p", "-t", active.id, "-S", "-20")

					dirStr := active.dir
					if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(dirStr, home) {
						dirStr = "~" + strings.TrimPrefix(dirStr, home)
					}
					lines = append(lines, fmt.Sprintf("  %d.%d [%s] @ %s", active.windowIdx, active.paneIdx, active.cmd, dirStr))
					if bufErr == nil && pBuf != "" {
						for _, bl := range strings.Split(strings.TrimSpace(pBuf), "\n") {
							if strings.TrimSpace(bl) != "" {
								lines = append(lines, "    │ "+bl)
							}
						}
					}
					lines = append(lines, "")
				}
			}
		}

		return previewMsg{entry: entry, content: strings.Join(lines, "\n")}
	}
}

func loadFilesCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		files, err := findFiles(dir)
		if err != nil {
			return uiErrMsg{err}
		}
		return filesMsg{dir: dir, files: files}
	}
}

func sessionResources(session string) (float64, float64, []string) {
	ttys, err := tmuxRunLines("list-panes", "-s", "-t", session, "-F", "#{pane_tty}")
	if err != nil || len(ttys) == 0 {
		return 0, 0, nil
	}
	var cleanTty []string
	for _, t := range ttys {
		t = strings.TrimPrefix(t, "/dev/")
		if t != "" {
			cleanTty = append(cleanTty, t)
		}
	}
	if len(cleanTty) == 0 {
		return 0, 0, nil
	}
	psLines, err := runLines("ps", "--no-headers", "-o", "%cpu,%mem,comm", "-t", strings.Join(cleanTty, ","))
	if err != nil {
		return 0, 0, nil
	}
	var totalCPU, totalMem float64
	procsMap := make(map[string]bool)
	for _, l := range psLines {
		l = strings.TrimSpace(l)
		fields := strings.Fields(l)
		if len(fields) < 3 {
			continue
		}
		cpu, _ := strconv.ParseFloat(fields[0], 64)
		mem, _ := strconv.ParseFloat(fields[1], 64)
		comm := fields[2]
		totalCPU += cpu
		totalMem += mem
		procsMap[comm] = true
	}
	var procs []string
	for p := range procsMap {
		procs = append(procs, p)
	}
	sort.Strings(procs)
	return totalCPU, totalMem, procs
}

func currentSessionName() string {
	name, _ := tmuxRunOut("display-message", "-p", "#S")
	return name
}
