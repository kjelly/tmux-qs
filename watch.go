package main

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func lipglossWidth(s string) int { return lipgloss.Width(s) }

const (
	watchCaptureLines    = 50
	watchSuffixSafety    = 2
	watchCaptureParallel = 8 // bounded parallel capture-pane forks per tick
)

// WatchingConfig bundles the user-configurable knobs that drive waiting-agent
// detection. The values are sourced from the TOML config (see config.go).
type WatchingConfig struct {
	Commands   []string         // foreground command names that count as an AI agent
	IdleShells []string         // foreground command names that count as a "waiting" interactive shell/REPL (opt-in)
	Prompts    []string         // regex source; compiled lazily
	Idle       time.Duration    // tty mtime threshold
	Poll       time.Duration    // polling interval
	patterns   []*regexp.Regexp // compiled Prompts (set by compilePatterns)
}

func (w WatchingConfig) compiledPrompts() []*regexp.Regexp {
	if w.patterns != nil || len(w.Prompts) == 0 {
		return w.patterns
	}
	return compilePromptRegex(w.Prompts)
}

// compilePatterns produces a copy of w with patterns pre-compiled. Used by
// the watchCmd ticker to avoid recompiling on every tick.
func (w WatchingConfig) compilePatterns() WatchingConfig {
	w.patterns = compilePromptRegex(w.Prompts)
	return w
}

// paneKey uniquely identifies a tmux pane.
type paneKey struct {
	session   string
	window    string
	paneIndex int
}

type paneState struct {
	key    paneKey
	dir    string
	cmd    string
	pid    int
	dead   bool
	deadAt int64
	tty    string // e.g. /dev/pts/3
	buf    string
	paneID string
}

// procCount is one aggregated waiting process (deduped within a session/path).
type procCount struct {
	name   string
	count  int
	signal string // which detection signal first fired: "dead", "prompt", "stuck"
}

type waitingInfo struct {
	// bySession: session name -> ordered list of (process, count) waiting.
	bySession map[string][]procCount
	// byPath: pane cwd path -> ordered list of (process, count) waiting.
	byPath map[string][]procCount
	// panes: session name (or pane cwd) -> ordered list of individual
	// waiting panes (NOT deduped). Used by the Ctrl-Space detail
	// panel to show "which pane is waiting and how long".
	panes []waitingPane
	// paths: session name -> session_path snapshot, refreshed every watcher
	// tick. Used by lookup() so the UI does not fork `tmux list-sessions`
	// on every keystroke.
	paths map[string]string
	// totalWaiting is the number of distinct sessions that have at least one
	// waiting pane.
	totalWaiting int
	// version is bumped on every refresh; the UI uses it to invalidate
	// its entry-render cache.
	version int64
	// lastUpdated is the wall-clock time the snapshot was taken. Used
	// for the cross-process cache (see cache.go): a fresh TUI reopens
	// can reuse the snapshot only if it's within waitingCacheTTL.
	lastUpdated time.Time
}

// waitingPane describes one individual pane that the watcher classified
// as waiting. paneKey uniquely identifies the pane; cmd is its
// foreground process name; ttyIdle is how long the tty has been idle
// (when detection was via tty or buffer diff).
type waitingPane struct {
	session string
	window  string
	index   int
	paneID  string
	cmd     string
	signal  string
	ttyIdle time.Duration
}

type watchMsg struct {
	info waitingInfo
	// bufs holds each pane's capture buffer from this tick. The UI
	// stores it on the model and feeds it back into the next watchCmd
	// so "stuck" detection (buffer unchanged across ticks) can compare
	// against the previous tick. Keeping this state on the model —
	// rather than in a closure — matters because watchCmd is re-created
	// after every tick.
	bufs map[paneKey]string
	err  error
}

// selfPaneMsg reports the pane id of the tmux-qs popup itself; consumers
// should drop this pane from any aggregation.
type selfPaneMsg struct{ key paneKey }

// paneDeadSupported reports whether the running tmux exposes pane_dead and
// pane_dead_time format variables (tmux 1.7+).
func paneDeadSupported() bool {
	_, err := runOut("tmux", "list-panes", "-a", "-F", "#{pane_dead}")
	return err == nil
}

// collectPanes gathers metadata for every pane on the server, plus a small
// capture buffer for prompt-pattern matching. tmux 3.6 removed
// pane_activity / pane_last_activity; pane-idle is inferred from the
// mtime of the pane's tty (every tty write updates the tty's mtime).
//
// capture-pane calls are run in parallel (bounded by watchCaptureParallel)
// to keep the per-tick wall time low on large tmux servers (the old
// sequential loop did N+1 forks; with 50 panes on a slow box a single
// tick could take ~500ms).
//
// Buffers are only captured for panes whose foreground command passes
// isAllowed(opts) — buffer content is only ever consumed by the prompt
// and stuck signals, and both are gated on isAllowed anyway, so
// capturing shell/editor panes was pure wasted forks.
func collectPanes(opts WatchingConfig) (map[paneKey]paneState, error) {
	format := strings.Join([]string{
		"#{session_name}",
		"#{window_id}",
		"#{pane_index}",
		"#{pane_current_path}",
		"#{pane_current_command}",
		"#{pane_pid}",
		"#{pane_dead}",
		"#{pane_dead_time}",
		"#{pane_tty}",
		"#{pane_id}",
	}, "\t")

	lines, err := runLines("tmux", "list-panes", "-a", "-F", format)
	if err != nil {
		return nil, err
	}

	out := make(map[paneKey]paneState, len(lines))
	for _, l := range lines {
		parts := strings.Split(l, "\t")
		if len(parts) < 10 {
			continue
		}
		idx, _ := strconv.Atoi(parts[2])
		pid, _ := strconv.Atoi(parts[5])
		dead := parts[6] == "1"
		deadAt, _ := strconv.ParseInt(parts[7], 10, 64)

		k := paneKey{session: parts[0], window: parts[1], paneIndex: idx}
		out[k] = paneState{
			key:    k,
			dir:    parts[3],
			cmd:    parts[4],
			pid:    pid,
			dead:   dead,
			deadAt: deadAt,
			tty:    parts[8],
			paneID: parts[9],
		}
	}

	// Parallel capture. Each goroutine writes to a distinct paneKey,
	// but Go's runtime considers all concurrent map writes as a data
	// race regardless of disjoint keys, so we serialize the writes
	// through a mutex (the actual capture-pane fork is what we want
	// to parallelize, not the map store).
	var bufs sync.Map
	var wg sync.WaitGroup
	sem := make(chan struct{}, watchCaptureParallel)
	for k, s := range out {
		if !isAllowed(s.cmd, opts) {
			continue
		}
		wg.Add(1)
		go func(k paneKey) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			buf, err := runOut("tmux", "capture-pane", "-p", "-t",
				k.session+":"+k.window+"."+strconv.Itoa(k.paneIndex),
				"-S", "-"+strconv.Itoa(watchCaptureLines))
			if err == nil {
				bufs.Store(k, buf)
			}
		}(k)
	}
	wg.Wait()
	bufs.Range(func(k, v any) bool {
		s := out[k.(paneKey)]
		s.buf = v.(string)
		out[k.(paneKey)] = s
		return true
	})

	return out, nil
}

// isWaiting returns the signal that made the pane count as waiting, or
// "" if the pane is not waiting. Possible signals:
//
//	"dead"   - pane_dead && deadAt recent (foreground process ended cleanly)
//	"prompt" - capture buffer matches a known agent prompt pattern
//	"stuck"  - buffer hasn't changed for opts.Idle (agent looks hung)
//	"idle"   - tty mtime older than opts.Idle (last-resort catch-all)
//
// All signals are gated by isAllowed: a pane whose foreground command
// is neither in opts.Commands nor opts.IdleShells is never reported as
// waiting, regardless of how compelling the underlying signals look.
func isWaiting(s paneState, opts WatchingConfig, now time.Time, prevBuf string) string {
	if s.cmd == "" {
		return ""
	}
	if !isAllowed(s.cmd, opts) {
		return ""
	}
	if s.dead {
		if s.deadAt > 0 {
			t := time.Unix(s.deadAt, 0)
			if now.Sub(t) <= opts.Idle*4 {
				return "dead"
			}
		} else {
			return "dead"
		}
	}
	// Stat the tty once; all three remaining signals consume the same
	// idle duration (each ttyIdle call is an os.Stat).
	idle := ttyIdle(s.tty, now)
	if idle >= 1*time.Second && matchesAnyAgentPrompt(s.buf, opts.compiledPrompts()) {
		return "prompt"
	}
	// "stuck": buffer is unchanged from the previous tick AND the
	// tty has been idle at least opts.Idle. This catches agents
	// that are still alive but producing no output and not
	// matching any prompt regex (e.g. a thinking spinner).
	if prevBuf != "" && s.buf == prevBuf && idle >= opts.Idle {
		return "stuck"
	}
	if idle >= opts.Idle {
		return "idle"
	}
	return ""
}

// isAllowed reports whether the foreground command is in the union of the
// configured AI-agent list and the opt-in interactive shell list.
func isAllowed(cmd string, opts WatchingConfig) bool {
	for _, c := range opts.Commands {
		if c == cmd {
			return true
		}
	}
	for _, c := range opts.IdleShells {
		if c == cmd {
			return true
		}
	}
	return false
}

// ttyIdle returns how long the tty has been untouched by writes. Falls back
// to 0 (treated as "just active") if stat fails.
func ttyIdle(tty string, now time.Time) time.Duration {
	if tty == "" {
		return 0
	}
	st, err := os.Stat(tty)
	if err != nil {
		return 0
	}
	return now.Sub(st.ModTime())
}

func matchesAnyAgentPrompt(buf string, patterns []*regexp.Regexp) bool {
	if buf == "" || len(patterns) == 0 {
		return false
	}
	// Split by newline and check only the last 5 lines to avoid matching
	// prompts left in historical scrollback.
	lines := strings.Split(strings.TrimSpace(buf), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	lastLinesBuf := strings.Join(lines, "\n")
	for _, re := range patterns {
		if re.MatchString(lastLinesBuf) {
			return true
		}
	}
	return false
}

// aggregateWaiting builds a waitingInfo from a set of pane snapshots, dropping
// self (the tmux-qs popup's own pane) and any pane without a recognized
// process name. Processes are deduped within each session and path, with
// their repeat counts preserved in order of first appearance. paths is a
// session-name -> session_path snapshot used by lookup() to avoid spawning
// `tmux list-sessions` on every UI render. prevBufs lets the aggregation
// detect "stuck" panes (buffer unchanged between ticks).
func aggregateWaiting(states map[paneKey]paneState, self paneKey, opts WatchingConfig, paths map[string]string, prevBufs map[paneKey]string, now time.Time) waitingInfo {
	info := waitingInfo{
		bySession: map[string][]procCount{},
		byPath:    map[string][]procCount{},
		paths:     paths,
	}
	// Sort pane keys so aggregation is deterministic across runs.
	keys := make([]paneKey, 0, len(states))
	for k := range states {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].session != keys[j].session {
			return keys[i].session < keys[j].session
		}
		if keys[i].window != keys[j].window {
			return keys[i].window < keys[j].window
		}
		return keys[i].paneIndex < keys[j].paneIndex
	})
	for _, k := range keys {
		s := states[k]
		if k == self {
			continue
		}
		prevBuf := ""
		if prevBufs != nil {
			prevBuf = prevBufs[k]
		}
		signal := isWaiting(s, opts, now, prevBuf)
		if signal == "" {
			continue
		}
		name := strings.TrimSpace(s.cmd)
		if name == "" {
			continue
		}
		appendCountWithSignal(info.bySession, k.session, name, signal)
		if s.dir != "" {
			appendCountWithSignal(info.byPath, s.dir, name, signal)
		}
		info.panes = append(info.panes, waitingPane{
			session: k.session,
			window:  k.window,
			index:   k.paneIndex,
			paneID:  s.paneID,
			cmd:     name,
			signal:  signal,
			ttyIdle: ttyIdle(s.tty, now),
		})
	}
	for _, list := range info.bySession {
		if len(list) > 0 {
			info.totalWaiting++
		}
	}
	// Sort panes (session, window, index) so detail output is stable.
	sort.Slice(info.panes, func(i, j int) bool {
		if info.panes[i].session != info.panes[j].session {
			return info.panes[i].session < info.panes[j].session
		}
		if info.panes[i].window != info.panes[j].window {
			return info.panes[i].window < info.panes[j].window
		}
		return info.panes[i].index < info.panes[j].index
	})
	return info
}

func appendCount(m map[string][]procCount, key, name string) {
	appendCountWithSignal(m, key, name, "")
}

func appendCountWithSignal(m map[string][]procCount, key, name, signal string) {
	list := m[key]
	for i := range list {
		if list[i].name == name {
			list[i].count++
			// First non-empty signal wins — keep the most
			// informative reason on the aggregated entry.
			if list[i].signal == "" && signal != "" {
				list[i].signal = signal
			}
			m[key] = list
			return
		}
	}
	m[key] = append(list, procCount{name: name, count: 1, signal: signal})
}

// lookup returns the deduped waiting-process list for a list entry, using
// the paths snapshot captured at aggregation time. It does NOT call
// `tmux list-sessions`, so it is safe to invoke inside a UI render hot
// path.
//
// Resolution rules (strict — no fallthrough between the two namespaces):
//
//   - entry is a path ("/foo" or "~/foo")             -> byPath[expandPath(entry)]
//   - otherwise (session names, config names, …)     -> bySession[entry]
//
// The "session-name entries should not fall through to byPath" rule is
// important: tmux-qs itself runs an `opencode` agent in cwd
// /home/kjelly/github/tmux-qs, and every qs-* test session also
// happens to have that same cwd. Without the strict rule, every
// qs-* row would falsely inherit tmux-qs's opencode via the
// paths[qs-*] -> byPath[/home/.../tmux-qs] chain.
func (w waitingInfo) lookup(entry string) []procCount {
	if w.bySession == nil && w.byPath == nil {
		return nil
	}
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}
	if looksLikePath(entry) {
		if w.byPath == nil {
			return nil
		}
		return w.byPath[expandPath(entry)]
	}
	if w.bySession == nil {
		return nil
	}
	if sessions, ok := w.bySession[entry]; ok {
		return sessions
	}
	return nil
}

// panesFor returns the individual waiting panes (not deduped) for an
// entry. Resolution mirrors lookup(): path entries are matched by
// expanded path; everything else must match a session name exactly.
// No cross-namespace fallthrough (see lookup for the rationale).
func (w waitingInfo) panesFor(entry string) []waitingPane {
	entry = strings.TrimSpace(entry)
	if entry == "" || len(w.panes) == 0 {
		return nil
	}
	if looksLikePath(entry) {
		if w.byPath == nil {
			return nil
		}
		dir := expandPath(entry)
		if _, ok := w.byPath[dir]; !ok {
			return nil
		}
		// For path entries we don't store per-pane->path mapping
		// in detail form, so we can't return individual panes
		// here. Return nil and let the UI fall back to the
		// bySession lookup (which is also nil for path entries
		// not in bySession). This is intentional — the "detail
		// per pane" feature is session-scoped by design.
		return nil
	}
	if _, ok := w.bySession[entry]; !ok {
		return nil
	}
	var out []waitingPane
	for _, p := range w.panes {
		if p.session == entry {
			out = append(out, p)
		}
	}
	return out
}

// selfPaneCmd returns a tea.Cmd that resolves the current tmux-qs popup's
// own pane id so it can be excluded from waiting detection.
func selfPaneCmd() tea.Cmd {
	return func() tea.Msg {
		out, err := runOut("tmux", "display-message", "-p",
			"#{session_name}\t#{window_id}\t#{pane_index}")
		if err != nil || out == "" {
			return selfPaneMsg{}
		}
		parts := strings.Split(out, "\t")
		if len(parts) < 3 {
			return selfPaneMsg{}
		}
		idx, _ := strconv.Atoi(parts[2])
		return selfPaneMsg{key: paneKey{session: parts[0], window: parts[1], paneIndex: idx}}
	}
}

// watchCmd returns a tea.Cmd that, on its tick, collects all pane state
// and emits a watchMsg describing which sessions contain waiting processes.
// self is forwarded into aggregation; pass zero value before the first
// selfPaneCmd has resolved.
//
// prevBufs is the per-pane capture-buffer snapshot from the PREVIOUS tick
// (nil on the first tick). It must come from the model (the watchMsg.bufs
// the previous tick produced): watchCmd is re-created after every tick, so
// any state kept in a closure here would be reset each time — that's
// exactly the bug that silently disabled "stuck" detection.
//
// opts should already have its prompt patterns compiled (see
// WatchingConfig.compilePatterns); newModel does this once so ticks don't
// recompile the regexes.
func watchCmd(self paneKey, opts WatchingConfig, prevBufs map[paneKey]string) tea.Cmd {
	return tea.Tick(opts.Poll, func(t time.Time) tea.Msg {
		states, err := collectPanes(opts)
		if err != nil {
			return watchMsg{err: err}
		}
		// Single tmux fork for both session paths (the "paths"
		// snapshot used by lookup()) and any future needs. We
		// only need paths here, so flatten the sessionInfo map.
		si := tmuxSessionInfo()
		paths := make(map[string]string, len(si))
		for name, info := range si {
			paths[name] = info.path
		}
		info := aggregateWaiting(states, self, opts, paths, prevBufs, t)
		// Snapshot the current buffers for the next tick's "stuck" check.
		next := make(map[paneKey]string, len(states))
		for k, s := range states {
			next[k] = s.buf
		}
		info.lastUpdated = t
		info.version++
		_ = writeWaitingCache(info)
		return watchMsg{info: info, bufs: next}
	})
}

// now is a seam for tests so the idle threshold can be exercised
// deterministically.
var now = time.Now

// aggregateWaitingForTest is the test-only entry point that takes a clock.
func aggregateWaitingForTest(states map[paneKey]paneState, self paneKey, t time.Time) waitingInfo {
	return aggregateWaitingForTestWith(states, self, defaultConfig.toWatchingConfig(), t)
}

// aggregateWaitingForTestWith is the test-only entry point that takes an
// explicit WatchingConfig. Used by tests that need to exercise detection
// against a custom command/regex set.
func aggregateWaitingForTestWith(states map[paneKey]paneState, self paneKey, opts WatchingConfig, t time.Time) waitingInfo {
	opts = opts.compilePatterns()
	return aggregateWaiting(states, self, opts, nil, nil, t)
}

// toWatchingConfig converts a WaitingConfig from the TOML config into the
// watcher-ready WatchingConfig (with parsed durations and compiled regex
// deferred to compilePatterns).
func (c Config) toWatchingConfig() WatchingConfig {
	w := c.Waiting
	return WatchingConfig{
		Commands:   w.Commands,
		IdleShells: w.IdleShells,
		Prompts:    w.PromptRegex,
		Idle:       parseDuration(w.IdleThreshold, 30*time.Second),
		Poll:       parseDuration(w.PollInterval, 5*time.Second),
	}
}

// formatProcs renders the deduped waiting-process list for an entry,
// truncated to fit the remaining column width. Returns the rendered string
// plus the number of (process, count) pairs that were omitted.
//
// Each segment is followed by a tiny signal tag in parentheses that hints
// at WHY the pane was flagged (dead / prompt / stuck / idle). This is
// the cheap "V" deliverable — debugging the watcher without a debugger.
func formatProcs(parts []procCount, maxWidth int) (string, int) {
	if len(parts) == 0 || maxWidth <= 0 {
		return "", 0
	}
	var b strings.Builder
	used := 0
	shown := 0
	for _, p := range parts {
		seg := "· " + p.name
		if p.count > 1 {
			seg += " ×" + strconv.Itoa(p.count)
		}
		if p.signal != "" {
			seg += "[" + p.signal + "]"
		}
		w := lipglossWidth(seg)
		if used+w+1 > maxWidth {
			break
		}
		if shown > 0 {
			b.WriteByte(' ')
			used++
		}
		b.WriteString(seg)
		used += w
		shown++
	}
	more := len(parts) - shown
	if more > 0 {
		tail := " +" + strconv.Itoa(more)
		if used+lipglossWidth(tail) <= maxWidth {
			b.WriteString(tail)
		} else {
			b.WriteString("…")
		}
	}
	return b.String(), more
}
