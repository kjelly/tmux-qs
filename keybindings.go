package main

import (
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
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc":
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

	case "alt+n":
		// alt+n is now "new session" — it's no longer a down alias
		// (down is already covered by ↓, ctrl+n, tab, ctrl+j).
		if m.mode == modeList {
			return m, newSessionCmd(strings.TrimSpace(m.input.Value()))
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
	case "ctrl+f":
		return m.reload(srcFind)

	case "ctrl+d":
		// Killing a session is destructive — require a second Ctrl-d
		// on the same entry to confirm. The pending state is reset by
		// any cursor move or filter change.
		if m.mode == modeList {
			if sel, ok := m.selected(); ok {
				sel = strings.TrimSpace(sel)
				if m.pendingKill != sel {
					m.pendingKill = sel
					m.errText = "press Ctrl-d again to kill " + sel
					return m, nil
				}
				m.pendingKill = ""
				_ = run("tmux", "kill-session", "-t", sel)
				return m.reload(srcAll)
			}
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
