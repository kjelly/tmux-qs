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

	// Backoff bounds for the watcher. When collectPanes fails (NFS
	// stall, tmux hang, …) we exponentially back off to avoid pegging
	// the CPU and spamming the user with errors. The delay is capped
	// at watchMaxBackoff regardless of consecutive failure count.
	watchInitialBackoff = 2 * time.Second
	watchMaxBackoff     = 60 * time.Second
	watchBackoffMult    = 2
)

// WatchingConfig bundles the user-configurable knobs that drive waiting-agent
// detection. The values are sourced from the TOML config (see config.go).
type WatchingConfig struct {
	Commands   []string      // foreground command names that count as an AI agent
	IdleShells []string      // foreground command names that count as a "waiting" interactive shell/REPL (opt-in)
	Prompts    []string      // regex source; compiled lazily
	Idle       time.Duration // tty mtime threshold
	Poll       time.Duration // polling interval
	// PinnedOnly, when true, restricts waiting detection to panes
	// whose session is in the caller's pinned set. The watcher reads
	// the pinned snapshot at tick creation time (see watchCmd /
	// aggregateWaiting). Default behavior is pinned-only; users who
	// want to monitor agents across every session can flip this in
	// the TOML config.
	PinnedOnly bool
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
	key      paneKey
	dir      string
	cmd      string
	pid      int
	dead     bool
	deadAt   int64
	tty      string // e.g. /dev/pts/3
	buf      string
	paneID   string
	ttyMtime int64 // tty mtime (unix nanos) at collection time; 0 if unknown
}

// paneCapture is the previous tick's capture for one pane: the tty mtime
// at capture time plus the captured buffer. Carried on the model and fed
// into the next collectPanes so a pane whose tty hasn't been written
// since the last tick can reuse its buffer instead of forking
// capture-pane again — idle agents (the common waiting case) then cost
// zero forks per tick.
type paneCapture struct {
	ttyMtime int64
	buf      string
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
	// captures holds each allowed pane's tty mtime + capture buffer from
	// this tick. The UI stores it on the model and feeds it back into the
	// next watchCmd so (a) "stuck" detection can compare buffers against
	// the previous tick and (b) collectPanes can skip re-capturing panes
	// whose tty hasn't changed. Keeping this state on the model — rather
	// than in a closure — matters because watchCmd is re-created after
	// every tick.
	captures map[paneKey]paneCapture
	err      error
	// consecutiveErrs is the number of consecutive watcher failures
	// preceding this message (0 on the first message or after a
	// success). Surfaced in the UI for "watcher is struggling" hints.
	consecutiveErrs int
}

// selfPaneMsg reports the pane id of the tmux-qs popup itself; consumers
// should drop this pane from any aggregation.
type selfPaneMsg struct{ key paneKey }

// paneDeadSupported reports whether the running tmux exposes pane_dead and
// pane_dead_time format variables (tmux 1.7+).
func paneDeadSupported() bool {
	_, err := tmuxRunOut("list-panes", "-a", "-F", "#{pane_dead}")
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
func collectPanes(opts WatchingConfig, prev map[paneKey]paneCapture) (map[paneKey]paneState, error) {
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

	lines, err := tmuxRunLines("list-panes", "-a", "-F", format)
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
			key:      k,
			dir:      parts[3],
			cmd:      parts[4],
			pid:      pid,
			dead:     dead,
			deadAt:   deadAt,
			tty:      parts[8],
			paneID:   parts[9],
			ttyMtime: ttyMtimeNano(parts[8]),
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
		// Reuse the previous tick's buffer when the pane's tty hasn't
		// been written since (mtime unchanged) — no tty write means
		// the visible content is identical, so a fresh capture-pane
		// fork would return the same bytes. This is the common case
		// for an agent sitting at a prompt.
		if pc, ok := prev[k]; ok && pc.ttyMtime != 0 && pc.ttyMtime == s.ttyMtime {
			s.buf = pc.buf
			out[k] = s
			continue
		}
		wg.Add(1)
		go func(k paneKey) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			buf, err := tmuxRunOut("capture-pane", "-p", "-t",
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
//
// prevBuf is the previous tick's capture buffer (full text, not hash).
// Stuck detection uses FromChange to compare line-by-line, which is
// more tolerant of scrollback shifts than a raw string comparison.
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
	idle := ttyIdle(s.tty, now)
	if idle >= 1*time.Second && matchesAnyAgentPrompt(s.buf, opts.compiledPrompts()) {
		return "prompt"
	}
	// "stuck": buffer is unchanged from the previous tick AND the
	// tty has been idle at least opts.Idle. Uses FromChange for
	// line-by-line comparison — more tolerant of scrollback shifts
	// than a raw string comparison.
	if prevBuf != "" && idle >= opts.Idle {
		startIdx, _ := fromChange(prevBuf, s.buf)
		if startIdx == -1 {
			return "stuck"
		}
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

// ttyMtimeNano returns the tty's mtime in unix nanoseconds, or 0 when the
// tty is empty or cannot be stat'd. Used by collectPanes to decide whether
// a pane's capture buffer can be reused from the previous tick.
func ttyMtimeNano(tty string) int64 {
	if tty == "" {
		return 0
	}
	st, err := os.Stat(tty)
	if err != nil {
		return 0
	}
	return st.ModTime().UnixNano()
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
//
// When opts.PinnedOnly is true, panes whose session is not in the pinned
// set are dropped before any signal evaluation. This is the "only watch
// agents in workspaces I care about" mode — see WatchingConfig.PinnedOnly
// for the rationale. Pinned is read by snapshot from the model on each
// tick; see watchCmd.
func aggregateWaiting(states map[paneKey]paneState, self paneKey, opts WatchingConfig, paths map[string]string, prevBufs map[paneKey]string, pinned map[string]bool, now time.Time) waitingInfo {
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
		if k == self || isEinkSessionName(k.session) {
			continue
		}
		if opts.PinnedOnly && !pinned[k.session] {
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
		out, err := tmuxRunOut("display-message", "-p",
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
// prevCaptures is the per-pane capture snapshot (tty mtime + buffer) from
// the PREVIOUS tick (nil on the first tick). It must come from the model
// (the watchMsg.captures the previous tick produced): watchCmd is
// re-created after every tick, so any state kept in a closure here would
// be reset each time — that's exactly the bug that silently disabled
// "stuck" detection. The snapshot also lets collectPanes skip
// re-capturing panes whose tty hasn't changed.
//
// opts should already have its prompt patterns compiled (see
// WatchingConfig.compilePatterns); newModel does this once so ticks don't
// recompile the regexes.
//
// delay overrides opts.Poll for this tick only. The UI uses it to apply
// exponential backoff after watcher errors: the normal poll interval on
// success, an exponentially growing interval on consecutive failures (capped
// at watchMaxBackoff). prevErrs feeds the consecutiveErrs field of the
// emitted watchMsg; pass 0 on the first tick.
//
// pinned is a snapshot of the user's pinned session set, captured by the
// caller (the Update handler) at tick creation time. It is read once
// here and threaded into aggregateWaiting, which uses it to drop non-
// pinned sessions when opts.PinnedOnly is set. A snapshot — rather than
// a live reference — is safe because m.pinned is only mutated on the
// bubbletea goroutine (same goroutine that creates watchCmd), so a
// read at tick creation is always the latest value.
func watchCmd(self paneKey, opts WatchingConfig, prevCaptures map[paneKey]paneCapture, delay time.Duration, prevErrs int, pinned map[string]bool) tea.Cmd {
	if delay <= 0 {
		delay = opts.Poll
	}
	return tea.Tick(delay, func(t time.Time) tea.Msg {
		states, err := collectPanes(opts, prevCaptures)
		if err != nil {
			return watchMsg{err: err, consecutiveErrs: prevErrs + 1}
		}
		si := tmuxSessionInfo()
		paths := make(map[string]string, len(si))
		for name, info := range si {
			paths[name] = info.path
		}
		// Feed the previous tick's full buffers into "stuck" detection:
		// aggregateWaiting compares them line-by-line against the current
		// buffer (fromChange) — an unchanged buffer over an idle tty is
		// what flags a pane as stuck.
		prevText := make(map[paneKey]string, len(prevCaptures))
		for k, pc := range prevCaptures {
			prevText[k] = pc.buf
		}
		info := aggregateWaiting(states, self, opts, paths, prevText, pinned, t)
		// Snapshot the allowed panes' tty mtime + buffer for the next
		// tick (only allowed panes are ever captured, so this stays
		// small even on large servers).
		next := make(map[paneKey]paneCapture, len(states))
		for k, s := range states {
			if !isAllowed(s.cmd, opts) {
				continue
			}
			next[k] = paneCapture{ttyMtime: s.ttyMtime, buf: s.buf}
		}
		info.lastUpdated = t
		info.version++
		_ = writeWaitingCache(info)
		return watchMsg{info: info, captures: next, consecutiveErrs: 0}
	})
}

// nextWatchDelay returns the delay to use for the next watcher tick.
// On success (errs == 0) it returns opts.Poll. On consecutive failures
// it returns an exponentially growing delay starting at watchInitialBackoff
// and capped at watchMaxBackoff. Pure function; safe to call from
// message handlers.
func nextWatchDelay(opts WatchingConfig, errs int) time.Duration {
	if errs <= 0 {
		return opts.Poll
	}
	d := watchInitialBackoff
	for i := 1; i < errs; i++ {
		d *= watchBackoffMult
		if d >= watchMaxBackoff {
			return watchMaxBackoff
		}
	}
	if d > watchMaxBackoff {
		return watchMaxBackoff
	}
	return d
}

// fromChange compares prev and curr line-by-line and returns the index
// of the first differing line. Returns (startIdx, text) where:
//
//	startIdx >= 0  → change found; text is curr[startIdx:]
//	startIdx == -1 → no change
//
// If curr has more lines than prev, the appended lines count as a
// change starting at len(prevLines).
func fromChange(prev, curr string) (startIdx int, text string) {
	prevLines := splitLines(prev)
	currLines := splitLines(curr)

	minLen := len(prevLines)
	if len(currLines) < minLen {
		minLen = len(currLines)
	}

	for i := 0; i < minLen; i++ {
		if prevLines[i] != currLines[i] {
			return i, strings.Join(currLines[i:], "\n")
		}
	}

	if len(currLines) > len(prevLines) {
		start := len(prevLines)
		return start, strings.Join(currLines[start:], "\n")
	}

	return -1, ""
}

// splitLines splits s by newline and drops the trailing empty string
// from a newline-terminated input.
func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// now is a seam for tests so the idle threshold can be exercised
// deterministically.
var now = time.Now

// aggregateWaitingForTest is the test-only entry point that takes a clock.
// It forces PinnedOnly=false so the tests can assert the full aggregation
// signal set without first having to populate a pinned snapshot; tests
// that specifically exercise the pinned-only behavior go through
// aggregateWaitingForTestWithOptsPinned.
func aggregateWaitingForTest(states map[paneKey]paneState, self paneKey, t time.Time) waitingInfo {
	opts := defaultConfig.toWatchingConfig()
	opts.PinnedOnly = false
	return aggregateWaitingForTestWith(states, self, opts, t)
}

// aggregateWaitingForTestWith is the test-only entry point that takes an
// explicit WatchingConfig. Used by tests that need to exercise detection
// against a custom command/regex set. PinnedOnly is forced off for
// backward compatibility — see the comment on aggregateWaitingForTest.
func aggregateWaitingForTestWith(states map[paneKey]paneState, self paneKey, opts WatchingConfig, t time.Time) waitingInfo {
	opts = opts.compilePatterns()
	opts.PinnedOnly = false
	return aggregateWaiting(states, self, opts, nil, nil, nil, t)
}

// aggregateWaitingForTestWithOptsPinned is the test-only entry point
// that exercises the pinned-only behavior with a caller-supplied
// pinned snapshot and opts. Used by tests that need to verify the
// filtering contract directly.
func aggregateWaitingForTestWithOptsPinned(states map[paneKey]paneState, self paneKey, opts WatchingConfig, pinned map[string]bool, t time.Time) waitingInfo {
	opts = opts.compilePatterns()
	return aggregateWaiting(states, self, opts, nil, nil, pinned, t)
}

// toWatchingConfig converts a WaitingConfig from the TOML config into the
// watcher-ready WatchingConfig (with parsed durations and compiled regex
// deferred to compilePatterns).
func (c Config) toWatchingConfig() WatchingConfig {
	w := c.Waiting
	pinnedOnly := true
	if w.PinnedOnly != nil {
		pinnedOnly = *w.PinnedOnly
	}
	return WatchingConfig{
		Commands:   w.Commands,
		IdleShells: w.IdleShells,
		Prompts:    w.PromptRegex,
		Idle:       parseDuration(w.IdleThreshold, 30*time.Second),
		Poll:       parseDuration(w.PollInterval, 5*time.Second),
		PinnedOnly: pinnedOnly,
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
