package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/junegunn/fzf/src/algo"
	"github.com/junegunn/fzf/src/util"
)

// fzf's V2 algorithm uses a reusable scratch buffer ("slab") to avoid
// allocating per match. These sizes mirror fzf core's defaults.
const (
	fzfSlab16Size = 100 * 1024
	fzfSlab32Size = 2048
)

// fuzzyEngine holds the per-model state that makes fuzzy matching cheap on
// the refilter hot path: a reusable fzf slab (so FuzzyMatchV2 doesn't
// allocate scratch buffers on every call) and a cache of util.Chars keyed
// by the matched string (so the []byte→Chars conversion isn't redone for
// every entry on every keystroke). The same entry string always yields the
// same Chars, so the cache never needs invalidating; it's bounded by the
// universe of session/dir names seen in a session.
//
// Not safe for concurrent use — the slab and chars map are mutated. The
// parallel refilter path (large lists) gives each worker its own slab and
// skips the shared cache instead of sharing this engine.
type fuzzyEngine struct {
	slab  *util.Slab
	chars map[string]util.Chars
}

func newFuzzyEngine() *fuzzyEngine {
	return &fuzzyEngine{
		slab:  util.MakeSlab(fzfSlab16Size, fzfSlab32Size),
		chars: map[string]util.Chars{},
	}
}

// charsFor returns the cached util.Chars for s, building and memoizing it
// on first use.
func (e *fuzzyEngine) charsFor(s string) util.Chars {
	if c, ok := e.chars[s]; ok {
		return c
	}
	c := util.ToChars([]byte(s))
	e.chars[s] = c
	return c
}

// score matches query against s using the engine's cached Chars and shared
// slab. Same (score, ok, indices) contract as fuzzyScore.
func (e *fuzzyEngine) score(s, query string) (int, bool, []int) {
	query = strings.TrimSpace(query)
	if query == "" {
		return 0, true, nil
	}
	return fuzzyScoreChars(e.charsFor(s), query, e.slab)
}

// initFuzzy 初始化 fzf algo 評分表。
// 必須在所有 fuzzyScore 呼叫前執行一次。
// Scheme: "path" — same as the original workspace.nu's
// `--scheme=path`. Differences from the default scheme:
//   - delimiter chars are restricted to '/' (default includes
//     ',' and ':' too), so ',' and ':' in entries don't fire
//     bonusBoundaryDelimiter.
//   - bonusBoundaryWhite is set to bonusBoundary (=30) instead
//     of bonusBoundary+2 (=32), so the first char of a word
//     after whitespace gets 2 points less.
//   - initialCharClass is charDelimiter, so the implicit
//     "before idx 0" class behaves as if preceded by '/'.
//
// Matches the original nu script's fzf-tmux invocation:
//
//	fzf-tmux -- --filepath-word --tiebreak=length,end --scheme=path
func initFuzzy() {
	if !algo.Init("path") {
		panic("fzf algo: unknown scheme")
	}
}

// fuzzyMatch reports whether all query terms match s (AND semantics).
// If query contains multiple space-separated terms, each term must match s.
func fuzzyMatch(s, query string) bool {
	_, ok, _ := fuzzyScore(s, query)
	return ok
}

// fuzzyScore wraps fzf's algo.FuzzyMatchV2 to support the same
// (bool, int, []int) interface the rest of the codebase expects, and
// to add the multi-term (AND of space-separated terms) behavior we
// already expose. Each term is scored independently with V2 and the
// scores are summed; all terms must match for the entry to be
// included. The returned []int are the byte offsets of the matched
// characters in the original (un-lowercased) input, suitable for
// highlight rendering. Empty when the query is empty.
//
// Pattern lowercasing follows fzf's documented precondition: the
// pattern must be lowercase when caseSensitive=false (V2 only
// lowercases the input, not the pattern).
func fuzzyScore(s, query string) (int, bool, []int) {
	query = strings.TrimSpace(query)
	if query == "" {
		return 0, true, nil
	}
	return fuzzyScoreChars(util.ToChars([]byte(s)), query, nil)
}

// fuzzyScoreChars is the shared core: it scores each space-separated term
// of query against the pre-built chars, summing scores (all terms must
// match) and merging the highlight positions. slab may be nil (V2 will
// allocate its own scratch).
//
// Smart-case: a term containing an uppercase letter is matched
// case-sensitively; an all-lowercase term is matched case-insensitively.
// V2 only lowercases its input when caseSensitive=false, so a
// case-insensitive term must itself be lowercased first.
func fuzzyScoreChars(chars util.Chars, query string, slab *util.Slab) (int, bool, []int) {
	total := 0
	var indices []int
	for _, term := range strings.Fields(query) {
		caseSensitive := hasUpper(term)
		pat := term
		if !caseSensitive {
			pat = strings.ToLower(term)
		}
		res, pos := algo.FuzzyMatchV2(
			caseSensitive,
			false, /* normalize */
			true,  /* forward */
			&chars, []rune(pat), true /* withPos */, slab)
		if res.Start < 0 {
			return 0, false, nil
		}
		total += res.Score
		if pos != nil {
			indices = append(indices, *pos...)
		}
	}
	// Multi-term matches can produce unsorted/overlapping positions;
	// sort + dedupe so the renderer highlights each column once.
	if len(indices) > 1 {
		sort.Ints(indices)
		indices = dedupeSortedInts(indices)
	}
	return total, true, indices
}

// hasUpper reports whether s contains an uppercase letter (smart-case
// trigger).
func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// dedupeSortedInts removes consecutive duplicates from a sorted slice,
// in place.
func dedupeSortedInts(a []int) []int {
	out := a[:1]
	for _, v := range a[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// sessionNameBonus returns an extra score boost for srcTmux rows
// whose session-name segment fuzzy-matches any term of the query.
// Other sources are unaffected. The raw item is the full m.items[i]
// entry, which for srcTmux has the form
//
//	display \t <session> \t <paneID>
//
// so the session name is the second tab-separated field. We use
// fuzzyMatch (not a full V2 score) here so the bonus is a flat
// "yes/no" addition that doesn't fight with the main per-row score
// already produced by the engine.
//
// The +50 number was chosen to be roughly the size of a fzf
// bonusBoundary (30) plus a small word-start bonus (16), so that a
// session-name hit will reliably float a row above a row that only
// matches on the pane's cwd/title but not on the session name.
const sessionNameBonus = 50

func sessionNameBonusFor(rawItem, query string) int {
	parts := strings.SplitN(rawItem, "\t", 3)
	if len(parts) < 2 {
		return 0
	}
	session := parts[1]
	if session == "" {
		return 0
	}
	for _, term := range strings.Fields(query) {
		if fuzzyMatch(session, term) {
			return sessionNameBonus
		}
	}
	return 0
}

// handleKey translates a single keypress into a model update + an
// optional follow-up command. The dispatch is mode-aware: e.g. Esc
// only quits in modeList; in modeBranch / modeHelp it returns to the
// list. The huge switch is intentional — keeping all keybindings in
// one place makes it easy to audit the keymap and to keep helpText in
// help.go in sync.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Snippets send directly from their list: Enter sends and closes, while
	// Space sends and keeps the list open for repeated inputs.
	if m.mode == modeSnippetSelect {
		idx := -1
		if m.cursor >= 0 && m.cursor < len(m.filtered) {
			idx = m.filtered[m.cursor]
		}
		switch msg.String() {
		case "enter":
			return m.sendSelectedSnippet(idx, true)
		case " ":
			return m.sendSelectedSnippet(idx, false)
		case "ctrl+c":
			return m, tea.Quit
		}
	}
	// In vimNormal mode (within modeList), dispatch to the vim key
	// handler instead of the default keybindings. The textinput is
	// already disabled in Update() so keys don't leak into the input
	// box. We still allow non-modeList modes (branch, help, tag, etc.)
	// to use their own key handlers below.
	if m.mode == modeList && m.vimMode == vimNormal {
		return m.handleVimNormal(msg)
	}
	// Translate the pressed key through the configurable keymap so the
	// switch below can keep dispatching on canonical keys. With no
	// [keybindings] overrides this is the identity.
	switch m.remapKey(msg.String()) {
	case "ctrl+c":
		return m, tea.Quit

	case "esc":
		// In modeList, Esc toggles insert↔normal vim mode. The
		// normal-mode Esc-quits behavior is handled inside
		// handleVimNormal (it never reaches this switch).
		if m.mode == modeList {
			if m.vimMode == vimInsert {
				if m.vimEnabled {
					m.vimMode = vimNormal
					m.vimCount = ""
					return m, nil
				} else {
					return m, tea.Quit
				}
			}
		}
		if m.mode == modeSnippetSelect {
			m.leaveSnippetPicker()
			return m, nil
		}
		if m.mode == modeAgentSelect {
			m.items = m.savedItems
			m.mode = modeList
			m.agentSelectTarget = ""
			m.errText = ""
			m.input.SetValue("")
			m.refilter()
			return m, nil
		}
		if m.mode == modeTag {
			m.mode = modeList
			m.tagFilter = ""
			m.errText = ""
			m.input.SetValue("")
			m.refilter()
			return m, nil
		}
		if m.mode == modeGroup {
			m.mode = modeList
			m.groupFilter = ""
			m.errText = ""
			m.input.SetValue("")
			m.refilter()
			return m, nil
		}
		if m.src == srcFiles {
			m.src = srcDefault
			m.fileSearchDir = ""
			m.refilter()
			return m, nil
		}
		if m.mode == modeBranch || m.mode == modeHelp {
			m.mode = modeList
			m.errText = ""
			m.refilter()
			return m, nil
		}
		return m, tea.Quit

	case "?":
		// Toggle help overlay.
		if m.mode == modeHelp {
			m.mode = modeList
		} else {
			m.mode = modeHelp
		}
		return m, nil

	case "ctrl+up":
		// Recall older input-box history (shell-style).
		if m.mode == modeList {
			m.recallHistory(-1)
		}
		return m, nil
	case "ctrl+down":
		if m.mode == modeList {
			m.recallHistory(1)
		}
		return m, nil

	case "down", "ctrl+n", "ctrl+j", "alt+n":
		m.move(1)
		return m, nil

	case "tab":
		// Tab is the fast two-mode switch: the normal session list and
		// the per-window/pane tmux view. Keep Tab as ordinary down-arrow
		// navigation in transient pickers such as snippets and agents.
		if m.mode != modeList {
			m.move(1)
			return m, nil
		}
		if m.src == srcTmux {
			return m.reload(srcDefault)
		}
		return m.reload(srcTmux)

	case "up", "ctrl+p", "shift+tab", "ctrl+k", "alt+p":
		m.move(-1)
		return m, nil

	case "alt+o":
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				if dir := entryDir(sel, m.sessionPaths); dir != "" {
					return m, openBrowserCmd(dir)
				}
				m.errText = "no directory for this entry"
			}
		}
		return m, nil

	case "alt+v":
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				var installed []string
				for _, cmd := range loadConfig().Waiting.Commands {
					if isCommandInstalled(cmd) {
						installed = append(installed, cmd)
					}
				}
				if len(installed) == 0 {
					m.errText = "no monitored AI agents are installed on the system"
					return m, nil
				}
				m.savedItems = m.items
				m.items = installed
				m.agentSelectTarget = sel
				m.mode = modeAgentSelect
				m.input.SetValue("")
				m.refilter()
				return m, nil
			}
		}
		return m, nil

	case " ":
		if m.mode == modeList {
			if err := m.startSnippetPicker(false); err != nil {
				m.errText = err.Error()
			}
		}
		return m, nil

	case "ctrl+s":
		if m.mode == modeList {
			if err := m.startSnippetPicker(true); err != nil {
				m.errText = err.Error()
			}
		}
		return m, nil

	case "alt+q":
		// Close the TUI and switch to the last session — the
		// second-press half of the toggle keybinding. Works inside
		// the popup because tmux's bind-key -n cannot fire when the
		// terminal is in raw mode (Bubble Tea captures all input).
		if m.mode == modeList {
			m.resultToggleClose = true
			return m, tea.Quit
		}
		return m, nil

	case "alt+i":
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				trimmed := strings.TrimSpace(sel)
				return m, togglePinCmd(trimmed, m.pinned[trimmed])
			}
		}
		return m, nil

	case "alt+left":
		if m.mode == modeList {
			return m, visitStackBackCmd()
		}
		return m, nil

	case "alt+right":
		if m.mode == modeList {
			return m, visitStackForwardCmd()
		}
		return m, nil

	case "alt+t":
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				dir := entryDir(sel, m.sessionPaths)
				if dir == "" {
					m.errText = "no directory for this entry"
					return m, nil
				}
				cfg := loadConfig()
				if len(cfg.Templates) == 0 {
					m.errText = "no templates defined in config"
					return m, nil
				}
				m.savedItems = m.items
				var names []string
				for _, t := range cfg.Templates {
					names = append(names, t.Name)
				}
				m.items = names
				m.mode = modeAgentSelect
				m.agentSelectTarget = sel
				m.templateMode = true
				m.input.SetValue("")
				m.refilter()
				return m, nil
			}
		}
		return m, nil

	case "alt+f":
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				if dir := entryDir(sel, m.sessionPaths); dir != "" {
					m.src = srcFiles
					m.fileSearchDir = dir
					m.loading = true
					return m, loadFilesCmd(dir)
				}
				m.errText = "no directory for this entry"
			}
		}
		return m, nil

	case "alt+m":
		if m.mode == modeList {
			name := strings.TrimSpace(m.input.Value())
			if name != "" {
				m.recordInput(name)
			}
			return m, newSessionCmd(name)
		}
		return m, nil

	case "alt+c":
		if m.mode == modeList {
			return m.reload(srcCleanup)
		}
		return m, nil

	case "alt+u":
		// Undo the most recent kill: recreate the captured session(s),
		// then reload so they reappear in the list.
		if m.mode == modeList && len(m.killedUndo) > 0 {
			snaps := m.killedUndo
			m.killedUndo = nil
			restored := undoKillCmd(snaps)
			model, reloadCmd := m.reload(m.src)
			return model, tea.Sequence(restored, reloadCmd)
		}
		return m, nil

	case "alt+j":
		m.jumpToSession(1)
		return m, nil
	case "alt+k":
		m.jumpToSession(-1)
		return m, nil

	case "alt+up":
		// Scroll preview pane up one line. No-op if the preview
		// pane isn't visible (narrow terminal).
		m.scrollPreview(-1)
		return m, nil
	case "alt+down":
		m.scrollPreview(1)
		return m, nil
	case "pgup":
		// Page-scroll the preview pane.
		m.scrollPreview(-m.listHeight())
		return m, nil
	case "pgdown":
		m.scrollPreview(m.listHeight())
		return m, nil

	case "enter":
		return m.choose()

	case "alt+enter":
		// Send the current input box text to the selected session via
		// `tmux send-keys`, then clear the input. Useful for "drop a
		// prompt to the agent without leaving the picker" (e.g. type
		// "/continue" and tap Alt-Enter). When the session has a
		// waiting agent pane, target that pane directly — the session
		// default would land on the active pane, which is not
		// necessarily where the agent is blocked.
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				text := m.input.Value()
				if text == "" {
					m.errText = "type a prompt first"
					return m, nil
				}
				dest, err := resolvePaneTarget(sel)
				if err != nil {
					m.errText = "cannot resolve target pane"
					return m, nil
				}
				// A selected pane row always wins. For a session row, retain
				// the existing waiting-agent preference.
				if !hasPaneEnvelope(sel) {
					for _, wp := range m.waiting.panes {
						if wp.session == dest.session {
							dest.paneID = wp.paneID
							break
						}
					}
				}
				if err := sendTextToPane(dest.paneID, text, false); err != nil {
					m.errText = "send failed"
					return m, nil
				}
				m.recordInput(text)
				m.input.SetValue("")
			}
		}
		return m, nil

	case "ctrl+a":
		return m.reload(srcAll)
	case "ctrl+t":
		return m.reload(srcTmux)
	case "ctrl+g":
		return m.reload(srcConfigs)
	case "ctrl+x":
		return m.reload(srcZoxide)
	case "alt+r":
		return m.reload(srcZoxideRoot)
	case "ctrl+r":
		// Rename the selected session. The new name is taken from
		// the current input box (so the user types the new name
		// before pressing Ctrl-r). Session name entries only.
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				newName := strings.TrimSpace(m.input.Value())
				if newName == "" {
					m.errText = "type a new name first"
					return m, nil
				}
				m.recordInput(newName)
				return m, renameSessionCmd(sel, newName)
			}
		}
		return m, nil
	case "ctrl+e":
		return m.reload(srcPanes)
	case "ctrl+v":
		// List the windows of the selected tmux session and jump to one.
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				name := strings.TrimSpace(sel)
				if _, isSession := m.sessionPaths[name]; isSession {
					return m.showWindows(name)
				}
				m.errText = "not a running tmux session"
			}
		}
		return m, nil
	case "ctrl+h":
		return m.reload(srcSSH)
	case "ctrl+o":
		return m.reload(srcCommands)
	case "ctrl+f":
		return m.reload(srcFind)

	case "ctrl+d":
		// Killing a session is destructive — require a second Ctrl-d
		// on the same entry to confirm. The pending state is reset by
		// any cursor move or filter change.
		// When entries are marked (multi-select), batch-kill all
		// marked sessions with a single confirmation.
		if m.mode == modeList {
			if len(m.marked) > 0 {
				if m.pendingKill != "batch" {
					m.pendingKill = "batch"
					m.errText = fmt.Sprintf("press Ctrl-d again to kill %d session(s)", len(m.marked))
					return m, nil
				}
				m.pendingKill = ""
				var names []string
				for idx := range m.marked {
					names = append(names, strings.TrimSpace(m.items[idx]))
				}
				m.captureForUndo(names)
				for _, sel := range names {
					_ = tmuxRun("kill-session", "-t", sel)
				}
				m.marked = nil
				return m.reload(m.src)
			}
			if m.src == srcCleanup {
				if m.pendingKill != "cleanup-all" {
					m.pendingKill = "cleanup-all"
					m.errText = fmt.Sprintf("press Ctrl-d again to kill all %d stale session(s)", len(m.items))
					return m, nil
				}
				m.pendingKill = ""
				var names []string
				for _, item := range m.items {
					names = append(names, strings.TrimSpace(item))
				}
				m.captureForUndo(names)
				for _, sel := range names {
					_ = tmuxRun("kill-session", "-t", sel)
				}
				return m.reload(srcCleanup)
			}
			if sel, ok := m.selected(); ok {
				sel = strings.TrimSpace(sel)
				if m.pendingKill != sel {
					m.pendingKill = sel
					m.errText = "press Ctrl-d again to kill " + sel
					return m, nil
				}
				m.pendingKill = ""
				m.captureForUndo([]string{sel})
				_ = tmuxRun("kill-session", "-t", sel)
				return m.reload(m.src)
			}
		}
		return m, nil

	case "ctrl+,":
		if m.mode == modeList {
			tags := m.allTags()
			if len(tags) == 0 {
				m.errText = "no tags available"
				return m, nil
			}
			m.savedItems = m.items
			m.items = tags
			m.mode = modeTag
			m.input.SetValue("")
			m.refilter()
			return m, nil
		}
		return m, nil

	case "ctrl+;":
		if m.mode == modeList {
			groups := allGroups()
			if len(groups) == 0 {
				m.errText = "no groups defined in config"
				return m, nil
			}
			m.savedItems = m.items
			m.items = groups
			m.mode = modeGroup
			m.input.SetValue("")
			m.refilter()
			return m, nil
		}
		return m, nil

	case "ctrl+w":
		if m.mode == modeList {
			return m.showWaiting()
		}
		return m, nil

	case "ctrl+b":
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				if dir := entryDir(sel, m.sessionPaths); dir != "" {
					m.loading = true
					return m, branchesCmd(dir)
				}
			}
			m.errText = "no directory for this entry"
		}
		return m, nil

	case "ctrl+y":
		// modeBranch: branch names aren't paths; no-op (no error).
		if m.mode != modeList {
			return m, nil
		}
		if sel, ok := m.selected(); ok {
			if dir := entryDir(sel, m.sessionPaths); dir != "" {
				return m, copyToClipboardCmd(dir)
			}
			m.errText = "no directory for this entry"
		}
		return m, nil

	case "ctrl+@":
		// Ctrl-Space (which Bubble Tea reports as "ctrl+@") toggles
		// a per-pane detail panel for the cursor row.
		if m.mode == modeList {
			m.showDetail = !m.showDetail
		}
		return m, nil
	}

	var cmd tea.Cmd
	before := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		// Manual editing exits history recall, so the next Ctrl-Up
		// starts from the newest entry again.
		m.historyPos = len(m.inputHistory)
		m.refilter()
	}
	return m, cmd
}

// handleMouse translates a mouse click into a cursor move. We support
// left-click in the list area to jump the cursor to the clicked row.
// Scroll-wheel events (encoded as MouseButtonWheelUp/...) are mapped
// to up/down moves. Middle-click is ignored (terminal compatibility
// — many terminals send middle-click as paste).
func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.move(-1)
	case tea.MouseButtonWheelDown:
		m.move(1)
	case tea.MouseButtonLeft:
		if msg.Action == tea.MouseActionPress {
			// Translate the click Y into a list index. Layout
			// (top-to-bottom):
			//   Y=0..inputPad-1      : inputPad blank lines
			//   Y=inputPad            : prompt line
			//   Y=inputPad+1          : header line
			//   Y=inputPad+2..end     : list rows (filtered[m.offset..])
			// So a click at Y=inputPad+2 is the first visible row,
			// which is filtered[m.offset]. Convert to absolute index.
			row := msg.Y - m.inputPad - 2
			if row >= 0 {
				target := m.offset + row
				if target < len(m.filtered) {
					m.cursor = target
					m.clampScroll()
				}
			}
		}
	}
	return m, nil
}

// handleVimNormal processes keypresses in vimNormal mode. In this mode
// the textinput is disabled (Update() skips input.Update), so keys
// are interpreted as vim commands. Supported commands:
//
//	j / k          move cursor down / up (count prefix supported)
//	gg / G         jump to first / last item
//	0-9            accumulate count prefix (applied to next motion)
//	/              enter insert mode (focuses input for filtering)
//	i / a          enter insert mode
//	Enter          select current item (same as insert-mode Enter)
//	dd             kill: if marked sessions exist, batch-kill all of
//	               them; otherwise kill the cursor session (confirmation)
//	yy             copy current entry's directory (like Ctrl-y)
//	S              batch-send input text to all marked sessions
//	               (like Alt-Enter with marks)
//	u              unmark all marked sessions
//	Space          open snippets for the selected pane
//	Esc / :q       quit program
//	Ctrl-c         quit program
//
// Digit keys accumulate into m.vimCount; a non-digit action key
// consumes the count (default 1) and resets it.
func (m model) handleVimNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Always-allowed: quit keys.
	if key == "esc" || key == "ctrl+c" {
		return m, tea.Quit
	}
	if key == ":q" {
		return m, tea.Quit
	}

	// Digit accumulation for count prefix.
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		// Bare "0" is a vim "start of line" — but we don't
		// edit a line here, so treat as no-op.
		if key == "0" && m.vimCount == "" {
			m.vimLastKey = ""
			return m, nil
		}
		m.vimCount += key
		m.vimLastKey = key
		return m, nil
	}

	// Double-key sequence detection: "dd" and "gg". msg.String()
	// only ever returns a single key name, so we track the previous
	// key in m.vimLastKey to detect these vim idioms.
	if key == "d" && m.vimLastKey == "d" {
		// dd: kill current session, or batch-kill all marked
		// sessions if any exist. Both flows require the first
		// press to have set pendingKill; the second press (this
		// one) executes.
		m.vimLastKey = ""
		m.vimCount = ""
		if len(m.marked) > 0 {
			if m.pendingKill != "batch" {
				m.pendingKill = "batch"
				m.errText = fmt.Sprintf("press dd again to kill %d session(s)", len(m.marked))
				return m, nil
			}
			m.pendingKill = ""
			// Only kill entries that are actually running tmux
			// sessions (present in sessionPaths). Paths, zoxide
			// dirs, and config sessions without a running tmux
			// session are silently skipped — tmux kill-session
			// would fail on them.
			var names []string
			for idx := range m.marked {
				sel := strings.TrimSpace(m.items[idx])
				if _, isSession := m.sessionPaths[sel]; isSession {
					names = append(names, sel)
				}
			}
			m.captureForUndo(names)
			killed := 0
			for _, sel := range names {
				if err := tmuxRun("kill-session", "-t", sel); err == nil {
					killed++
				}
			}
			m.marked = nil
			if killed == 0 {
				m.errText = "no killable sessions among marked items"
				return m, nil
			}
			return m.reload(srcAll)
		}
		if sel, ok := m.selected(); ok {
			sel = strings.TrimSpace(sel)
			if _, isSession := m.sessionPaths[sel]; !isSession {
				m.errText = "not a running tmux session"
				return m, nil
			}
			if m.pendingKill != sel {
				// First dd press for this session: show
				// confirmation. The second dd press (caught
				// by the d+vimLastKey=="d" branch above)
				// will actually kill.
				m.pendingKill = sel
				m.errText = "press dd again to kill " + sel
				return m, nil
			}
			// Reached only if vimLastKey was "d" but pendingKill
			// somehow doesn't match — treat as fresh confirmation.
			m.pendingKill = sel
			m.errText = "press dd again to kill " + sel
			return m, nil
		}
		return m, nil
	}
	if key == "g" && m.vimLastKey == "g" {
		// gg: go to top.
		m.vimLastKey = ""
		m.vimCount = ""
		if len(m.filtered) > 0 {
			m.cursor = 0
			m.clampScroll()
		}
		return m, m.updatePreviewCmd()
	}

	// If this is the first key of a double-key sequence, remember
	// it and wait for the second key.
	if key == "d" || key == "g" {
		// First d/g press: show confirmation hint for kill, or
		// just wait silently for gg. We set pendingKill here so
		// the second press (caught above) knows the target.
		if key == "d" {
			if len(m.marked) > 0 {
				// Count only killable items (actual tmux
				// sessions) for a clearer confirmation message.
				killable := 0
				for idx := range m.marked {
					sel := strings.TrimSpace(m.items[idx])
					if _, isSession := m.sessionPaths[sel]; isSession {
						killable++
					}
				}
				if killable == 0 {
					m.errText = "no killable sessions among marked items"
				} else {
					m.pendingKill = "batch"
					m.errText = fmt.Sprintf("press dd again to kill %d session(s)", killable)
				}
			} else if sel, ok := m.selected(); ok {
				sel = strings.TrimSpace(sel)
				if _, isSession := m.sessionPaths[sel]; !isSession {
					m.errText = "not a running tmux session"
				} else {
					m.pendingKill = sel
					m.errText = "press dd again to kill " + sel
				}
			} else {
				// No selection and no marks: just wait.
			}
		}
		m.vimLastKey = key
		m.vimCount = ""
		return m, nil
	}

	// Any other key resets the double-key tracker.
	m.vimLastKey = ""

	// Resolve count (default 1).
	count := 1
	hasCount := false
	if m.vimCount != "" {
		if n, err := strconv.Atoi(m.vimCount); err == nil && n > 0 {
			count = n
			hasCount = true
		}
	}
	m.vimCount = ""

	switch key {
	case "j", "down":
		for i := 0; i < count; i++ {
			m.move(1)
		}
		return m, m.updatePreviewCmd()
	case "k", "up":
		for i := 0; i < count; i++ {
			m.move(-1)
		}
		return m, m.updatePreviewCmd()
	case "G":
		// G: go to last item (or to Nth item if count given).
		if len(m.filtered) > 0 {
			if hasCount {
				m.cursor = count - 1
				if m.cursor >= len(m.filtered) {
					m.cursor = len(m.filtered) - 1
				}
			} else {
				m.cursor = len(m.filtered) - 1
			}
			m.clampScroll()
		}
		return m, m.updatePreviewCmd()
	case "/":
		// /: focus input box, enter insert mode, clear input.
		m.vimMode = vimInsert
		m.input.SetValue("")
		m.refilter()
		m.input.Focus()
		return m, nil
	case "i", "a":
		m.vimMode = vimInsert
		m.input.Focus()
		return m, nil
	case "enter":
		return m.choose()
	case "S":
		// S (shift+s): batch-send input box text to all marked
		// sessions via tmux send-keys. Like Alt-Enter but works in
		// normal mode and targets all marked sessions. Only sends
		// to entries that are actually running tmux sessions —
		// paths, zoxide dirs, and config sessions without a
		// running tmux session are silently skipped.
		text := m.input.Value()
		if text == "" {
			m.errText = "type a prompt first"
			return m, nil
		}
		if len(m.marked) == 0 {
			m.errText = "no marked sessions"
			return m, nil
		}
		sent := 0
		for idx := range m.marked {
			sel := strings.TrimSpace(m.items[idx])
			if _, isSession := m.sessionPaths[sel]; !isSession && !hasPaneEnvelope(sel) {
				continue
			}
			dest, err := resolvePaneTarget(sel)
			if err != nil {
				continue
			}
			// Prefer a waiting pane only for a session row. Exact pane
			// selections must never be redirected to another pane.
			if !hasPaneEnvelope(sel) {
				for _, wp := range m.waiting.panes {
					if wp.session == dest.session {
						dest.paneID = wp.paneID
						break
					}
				}
			}
			if err := sendTextToPane(dest.paneID, text, false); err != nil {
				continue
			}
			sent++
		}
		if sent > 0 {
			m.recordInput(text)
		}
		m.input.SetValue("")
		m.marked = nil
		if sent == 0 {
			m.errText = "no sendable sessions among marked items"
		}
		return m, nil
	case "u":
		// u: unmark all marked sessions.
		if len(m.marked) == 0 {
			return m, nil
		}
		m.marked = nil
		return m, nil
	case "yy":
		// yy: copy directory to clipboard (same as Ctrl-y).
		if sel, ok := m.selected(); ok {
			if dir := entryDir(sel, m.sessionPaths); dir != "" {
				return m, copyToClipboardCmd(dir)
			}
			m.errText = "no directory for this entry"
		}
		return m, nil
	case " ":
		if err := m.startSnippetPicker(false); err != nil {
			m.errText = err.Error()
		}
		return m, nil
	}

	// Unknown key in normal mode: if the key isn't owned by normal
	// mode, fall through to the insert-mode handler so users get the
	// same shortcuts in both vim submodes (e.g. alt+o, ?, ctrl+y,
	// tab all work in BOTH insert and normal mode). The dedicated
	// normal-mode handler keeps the keys that genuinely differ —
	// vim motions (j/k/G/gg), dd, yy, S, u, /, i, a, enter, esc,
	// ctrl+c, :q, space — and the count-prefix digits are consumed
	// earlier in this function.
	if !vimNormalOwnKeys[key] {
		return m.dispatchAsInsert(msg)
	}
	return m, nil
}

// vimNormalOwnKeys is the set of keys that have a dedicated meaning
// in vimNormal mode. Any other key falls through to the insert-mode
// handler so users get a uniform set of shortcuts across both vim
// submodes. The count-prefix digits (0-9) are NOT in this set — they
// are consumed at the top of handleVimNormal and never reach the
// fall-through check. The double-key first-press keys (d, g, y) ARE
// in the set as a defensive marker: the main switch's own handling
// fires before this check, but listing them here documents the
// intent and keeps a key from ever escaping into insert dispatch
// in odd edge cases.
var vimNormalOwnKeys = map[string]bool{
	"esc":    true,
	"ctrl+c": true,
	":q":     true,
	"j":      true,
	"k":      true,
	"G":      true,
	"/":      true,
	"i":      true,
	"a":      true,
	"enter":  true,
	"S":      true,
	"u":      true,
	"y":      true,
	"d":      true,
	"g":      true,
	" ":      true,
}

// dispatchAsInsert re-runs the key through handleKey as if the user
// were in insert mode. We temporarily flip vimMode to vimInsert so
// handleKey's early-return guard for vimNormal doesn't recurse into
// handleVimNormal. We also clear vimCount so a count prefix that
// the normal-mode dispatcher accumulated (e.g. "5" then "alt+up")
// doesn't leak into the next motion key the user presses.
//
// Used by handleVimNormal's fall-through path: any key without a
// dedicated normal-mode meaning should behave the same as it does
// in insert mode.
func (m model) dispatchAsInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	savedMode := m.vimMode
	m.vimMode = vimInsert
	m.vimCount = ""
	resModel, cmd := m.handleKey(msg)
	m = resModel.(model)
	m.vimMode = savedMode
	return m, cmd
}
