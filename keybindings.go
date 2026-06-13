package main

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleKey translates a single keypress into a model update + an
// optional follow-up command. The dispatch is mode-aware: e.g. Esc
// only quits in modeList; in modeBranch / modeHelp it returns to the
// list. The huge switch is intentional — keeping all keybindings in
// one place makes it easy to audit the keymap and to keep helpText in
// help.go in sync.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// In vimNormal mode (within modeList), dispatch to the vim key
	// handler instead of the default keybindings. The textinput is
	// already disabled in Update() so keys don't leak into the input
	// box. We still allow non-modeList modes (branch, help, tag, etc.)
	// to use their own key handlers below.
	if m.mode == modeList && m.vimMode == vimNormal {
		return m.handleVimNormal(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc":
		// In modeList, Esc toggles insert↔normal vim mode. The
		// normal-mode Esc-quits behavior is handled inside
		// handleVimNormal (it never reaches this switch).
		if m.mode == modeList {
			if m.vimMode == vimInsert {
				m.vimMode = vimNormal
				m.vimCount = ""
				return m, nil
			}
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

	case "down", "ctrl+n", "tab", "ctrl+j":
		m.move(1)
		return m, nil

	case "up", "ctrl+p", "shift+tab", "ctrl+k":
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

	case "alt+p":
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

	case "alt+n":
		// alt+n is now "new session" — it's no longer a down alias
		// (down is already covered by ↓, ctrl+n, tab, ctrl+j).
		if m.mode == modeList {
			return m, newSessionCmd(strings.TrimSpace(m.input.Value()))
		}
		return m, nil

	case "alt+c":
		if m.mode == modeList {
			return m.reload(srcCleanup)
		}
		return m, nil

	case "alt+j":
		m.jumpToSession(1)
		return m, nil
	case "alt+k":
		m.jumpToSession(-1)
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
				dest := strings.TrimSpace(sel)
				for _, wp := range m.waiting.panes {
					if wp.session == dest {
						dest = wp.paneID
						break
					}
				}
				_ = run("tmux", "send-keys", "-t", dest, text)
				m.input.SetValue("")
			}
		}
		return m, nil

	case "ctrl+a":
		return m.reload(srcAll)
	case "ctrl+t", "ctrl+s":
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
				return m, renameSessionCmd(sel, newName)
			}
		}
		return m, nil
	case "ctrl+e":
		return m.reload(srcPanes)
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
				for idx := range m.marked {
					sel := strings.TrimSpace(m.items[idx])
					_ = run("tmux", "kill-session", "-t", sel)
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
				for _, item := range m.items {
					sel := strings.TrimSpace(item)
					_ = run("tmux", "kill-session", "-t", sel)
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
				_ = run("tmux", "kill-session", "-t", sel)
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

	case " ":
		if m.mode == modeList {
			if len(m.filtered) == 0 {
				return m, nil
			}
			idx := m.filtered[m.cursor]
			if m.marked == nil {
				m.marked = make(map[int]bool)
			}
			if m.marked[idx] {
				delete(m.marked, idx)
			} else {
				m.marked[idx] = true
			}
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

// fuzzyMatch reports whether all query characters appear in s in order.
// If query contains multiple space-separated terms, each term must match s.
func fuzzyMatch(s, query string) bool {
	ok, _ := fuzzyScore(s, query)
	return ok
}

// fuzzyScore is fuzzyMatch plus an fzf-like rank: higher scores mean a
// better match. An empty query matches everything with score 0. The
// score is the sum of per-term scores (see scoreTerm).
func fuzzyScore(s, query string) (bool, int) {
	query = strings.TrimSpace(query)
	if query == "" {
		return true, 0
	}
	terms := strings.Fields(strings.ToLower(query))
	s = strings.ToLower(s)
	total := 0
	for _, term := range terms {
		ok, sc := scoreTerm(s, term)
		if !ok {
			return false, 0
		}
		total += sc
	}
	return true, total
}

// scoreTerm greedily matches term as a subsequence of s and scores each
// matched character: +3 at a word start (string start or right after a
// path/word separator), and a consecutive char inherits the bonus of
// the run it extends (fzf does the same — it's what makes the "qs" run
// in "tmux-qs" beat the two scattered boundary hits in "quiet-shell").
// Every gap between matched chars costs 1, so tight matches outrank
// spread-out ones.
func scoreTerm(s, term string) (bool, int) {
	score := 0
	prev := -2
	runBonus := 0
	j := 0
	for i := 0; i < len(s) && j < len(term); i++ {
		if s[i] != term[j] {
			continue
		}
		switch {
		case i == 0 || isWordBoundary(s[i-1]):
			runBonus = 3
		case i == prev+1:
			if runBonus < 2 {
				runBonus = 2
			}
		default:
			runBonus = 1
		}
		score += runBonus
		if j > 0 && i != prev+1 {
			score-- // gap penalty
		}
		prev = i
		j++
	}
	if j < len(term) {
		return false, 0
	}
	return true, score
}

func isWordBoundary(c byte) bool {
	switch c {
	case '/', '-', '_', '.', ' ', '~':
		return true
	}
	return false
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
//	Space          toggle multi-select mark
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
			killed := 0
			for idx := range m.marked {
				sel := strings.TrimSpace(m.items[idx])
				if _, isSession := m.sessionPaths[sel]; !isSession {
					continue
				}
				if err := run("tmux", "kill-session", "-t", sel); err == nil {
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
	if m.vimCount != "" {
		if n, err := strconv.Atoi(m.vimCount); err == nil && n > 0 {
			count = n
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
			m.cursor = count - 1
			if m.cursor >= len(m.filtered) {
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
			if _, isSession := m.sessionPaths[sel]; !isSession {
				continue
			}
			// Prefer a waiting pane for the target session.
			dest := sel
			for _, wp := range m.waiting.panes {
				if wp.session == sel {
					dest = wp.paneID
					break
				}
			}
			_ = run("tmux", "send-keys", "-t", dest, text)
			sent++
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
		// Space: toggle multi-select mark (same as in insert mode).
		if len(m.filtered) == 0 {
			return m, nil
		}
		idx := m.filtered[m.cursor]
		if m.marked == nil {
			m.marked = make(map[int]bool)
		}
		if m.marked[idx] {
			delete(m.marked, idx)
		} else {
			m.marked[idx] = true
		}
		return m, nil
	}

	// Unknown key in normal mode: silently ignore.
	return m, nil
}
