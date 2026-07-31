package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestSmoke exercises the non-TUI plumbing against the real environment.
// Run manually with: go test -run TestSmoke -v
func TestSmoke(t *testing.T) {
	for _, k := range []sourceKind{srcDefault, srcTmux, srcZoxide, srcFind} {
		items, err := loadSource(k)
		if err != nil {
			t.Logf("src=%d err=%v", k, err)
			continue
		}
		head := ""
		if len(items) > 0 {
			head = items[0]
		}
		t.Logf("src=%d n=%d first=%q", k, len(items), head)
	}

	t.Logf("branch of sesh repo: %q", branchOfDir("/home/kjelly/github/sesh"))
	bs, err := listBranches("/home/kjelly/github/sesh")
	t.Logf("branches err=%v n=%d", err, len(bs))
	for i, b := range bs {
		if i > 5 {
			break
		}
		t.Logf("  %+v", b)
	}

	annots := resolveBranches([]string{"~/github/sesh", "~/github/tmux-qs", "/tmp"}, nil)
	t.Logf("annots: %v", annots)
}

// TestSessionBranch verifies that a tmux session name resolves to its
// working directory's git branch, and non-directory names are skipped.
func TestSessionBranch(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-branch-test"
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot get working directory: %v", err)
	}
	if err := tmuxRun("new-session", "-d", "-s", session, "-c", wd); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}

	annots := resolveBranches([]string{session, "no-such-session-xyz"}, nil)
	if annots[session] == "" {
		t.Errorf("expected branch for session %q, got none (annots=%v)", session, annots)
	}
	if _, ok := annots["no-such-session-xyz"]; ok {
		t.Errorf("non-session entry should be skipped, got %v", annots)
	}
	t.Logf("session %s -> branch %q", session, annots[session])
}

func TestFuzzyMatch(t *testing.T) {
	cases := []struct {
		s, q string
		want bool
	}{
		{"~/github/sesh", "ghse", true},
		{"~/github/sesh", "sesh", true},
		// Smart-case: a lowercase query is case-insensitive, so it
		// matches a path with uppercase letters...
		{"~/GitHub/sesh", "github", true},
		// ...but an uppercase query is case-sensitive, so "SESH" does
		// NOT match the all-lowercase "sesh".
		{"~/github/sesh", "SESH", false},
		{"~/GITHUB/sesh", "GITHUB", true}, // uppercase query matches uppercase text
		{"~/github/sesh", "xyz", false},
		{"anything", "", true},
		{"~/github/sesh", "gh se", true},
		{"~/github/sesh", "sesh gh", true}, // order-independent term matching
		{"~/github/sesh", "se gh xyz", false},
	}
	for _, c := range cases {
		if got := fuzzyMatch(c.s, c.q); got != c.want {
			t.Errorf("fuzzyMatch(%q,%q)=%v want %v", c.s, c.q, got, c.want)
		}
	}
}

func TestExpandPath(t *testing.T) {
	if p := expandPath("~/github"); p == "~/github" {
		t.Errorf("expandPath did not expand: %s", p)
	}
	if p := expandPath("/tmp"); p != "/tmp" {
		t.Errorf("expandPath mangled absolute path: %s", p)
	}
}

// TestWatchE2E exercises collectPanes + aggregateWaiting against the real
// tmux server. Skips when no server is available. Validates that idle
// nu shells with stale tty mtimes are reported as waiting.
func TestWatchE2E(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-watch-test"
	// Run a foreground command that's in the default AI-agent list so the
	// watcher treats the pane as an agent; then age the tty mtime to push
	// it over the idle threshold.
	if err := tmuxRun("new-session", "-d", "-s", session, "opencode"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}

	// Touch the tty to a far-past mtime so it counts as idle. tty mtime
	// updates on writes, so the default shell has a recent mtime -> not
	// waiting. We synthetically age it.
	paneInfo, err := tmuxRunOut("list-panes", "-t", session,
		"-F", "#{pane_tty}")
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	tty := paneInfo
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(tty, old, old); err != nil {
		t.Skipf("cannot chtimes %s: %v", tty, err)
	}

	states, err := collectPanes(defaultConfig.toWatchingConfig(), nil)
	if err != nil {
		t.Fatalf("collectPanes: %v", err)
	}
	info := aggregateWaitingForTest(states, paneKey{}, time.Now())

	procs, ok := info.bySession[session]
	if !ok {
		t.Fatalf("session %s should be reported as waiting, info=%+v", session, info.bySession)
	}
	if len(procs) == 0 {
		t.Errorf("expected at least one waiting process in %s, got empty", session)
	}
	t.Logf("session %s -> %+v", session, procs)
}

func TestAggregateWaiting(t *testing.T) {
	now := time.Now()
	old := now.Add(-5 * time.Minute) // long idle
	tmp := t.TempDir()
	oldTty := tmp + "/old"
	newTty := tmp + "/new"
	writeTtyMtime(t, oldTty, old)
	writeTtyMtime(t, newTty, now)
	states := map[paneKey]paneState{
		{session: "A", window: "@1", paneIndex: 0}: {key: paneKey{session: "A", window: "@1", paneIndex: 0}, cmd: "claude", tty: oldTty, dir: "/p/a"},
		{session: "A", window: "@1", paneIndex: 1}: {key: paneKey{session: "A", window: "@1", paneIndex: 1}, cmd: "claude", tty: oldTty, dir: "/p/a"},
		{session: "A", window: "@1", paneIndex: 2}: {key: paneKey{session: "A", window: "@1", paneIndex: 2}, cmd: "opencode", tty: oldTty, dir: "/p/a"},
		// B has both panes recently active -> not waiting
		{session: "B", window: "@2", paneIndex: 0}: {key: paneKey{session: "B", window: "@2", paneIndex: 0}, cmd: "nu", tty: newTty, dir: "/p/b"},
		{session: "B", window: "@2", paneIndex: 1}: {key: paneKey{session: "B", window: "@2", paneIndex: 1}, cmd: "nu", tty: newTty, dir: "/p/b"},
	}
	info := aggregateWaitingForTest(states, paneKey{}, now)

	wantA := []procCount{{name: "claude", count: 2, signal: "idle"}, {name: "opencode", count: 1, signal: "idle"}}
	if !reflect.DeepEqual(info.bySession["A"], wantA) {
		t.Errorf("A = %+v, want %+v", info.bySession["A"], wantA)
	}
	if len(info.bySession["B"]) != 0 {
		t.Errorf("B should be empty, got %+v", info.bySession["B"])
	}
	wantPathA := []procCount{{name: "claude", count: 2, signal: "idle"}, {name: "opencode", count: 1, signal: "idle"}}
	if !reflect.DeepEqual(info.byPath["/p/a"], wantPathA) {
		t.Errorf("byPath[/p/a] = %+v, want %+v", info.byPath["/p/a"], wantPathA)
	}
	if info.totalWaiting != 1 {
		t.Errorf("totalWaiting = %d, want 1", info.totalWaiting)
	}
}

func TestAggregateWaitingDropsEinkSession(t *testing.T) {
	now := time.Now()
	state := paneState{cmd: "claude", dir: "/tmp", tty: "", buf: "waiting"}
	states := map[paneKey]paneState{
		{session: "work-eink", window: "0", paneIndex: 0}: state,
	}
	info := aggregateWaitingForTest(states, paneKey{}, now)
	if info.totalWaiting != 0 || len(info.bySession) != 0 {
		t.Fatalf("eink session appeared in waiting list: total=%d bySession=%v", info.totalWaiting, info.bySession)
	}
}

func TestAggregateWaitingDropsSelf(t *testing.T) {
	now := time.Now()
	old := now.Add(-5 * time.Minute)
	tmp := t.TempDir()
	oldTty := tmp + "/old"
	writeTtyMtime(t, oldTty, old)
	self := paneKey{session: "self", window: "@0", paneIndex: 0}
	states := map[paneKey]paneState{
		self: {key: self, cmd: "claude", tty: oldTty, dir: "/p/self"},
		{session: "x", window: "@1", paneIndex: 0}: {key: paneKey{session: "x", window: "@1", paneIndex: 0}, cmd: "claude", tty: oldTty, dir: "/p/x"},
	}
	info := aggregateWaitingForTest(states, self, now)
	if _, ok := info.bySession["self"]; ok {
		t.Errorf("self pane should be excluded, got %+v", info.bySession["self"])
	}
	if _, ok := info.byPath["/p/self"]; ok {
		t.Errorf("self path should be excluded, got %+v", info.byPath["/p/self"])
	}
	if len(info.bySession["x"]) != 1 {
		t.Errorf("x should have one entry, got %+v", info.bySession["x"])
	}
}

// TestAggregateWaitingPinnedOnlyDropsUnpinned verifies that with
// opts.PinnedOnly=true, panes whose session is not in the pinned set
// are dropped before signal evaluation. The test sets up three
// sessions — A (pinned) and B (pinned) both have a waiting claude
// pane, while C (not pinned) has an identical setup. The expectation
// is that only A and B appear in bySession, byPath, and panes, and
// C is invisible to the rest of the UI.
func TestAggregateWaitingPinnedOnlyDropsUnpinned(t *testing.T) {
	now := time.Now()
	old := now.Add(-5 * time.Minute)
	tmp := t.TempDir()
	oldTty := tmp + "/old"
	writeTtyMtime(t, oldTty, old)
	states := map[paneKey]paneState{
		{session: "A", window: "@1", paneIndex: 0}: {key: paneKey{session: "A", window: "@1", paneIndex: 0}, cmd: "claude", tty: oldTty, dir: "/p/a"},
		{session: "B", window: "@2", paneIndex: 0}: {key: paneKey{session: "B", window: "@2", paneIndex: 0}, cmd: "claude", tty: oldTty, dir: "/p/b"},
		{session: "C", window: "@3", paneIndex: 0}: {key: paneKey{session: "C", window: "@3", paneIndex: 0}, cmd: "claude", tty: oldTty, dir: "/p/c"},
	}
	opts := defaultConfig.toWatchingConfig()
	opts.PinnedOnly = true
	pinned := map[string]bool{"A": true, "B": true}
	info := aggregateWaitingForTestWithOptsPinned(states, paneKey{}, opts, pinned, now)

	if _, ok := info.bySession["A"]; !ok {
		t.Errorf("A (pinned) should be in bySession, got %+v", info.bySession)
	}
	if _, ok := info.bySession["B"]; !ok {
		t.Errorf("B (pinned) should be in bySession, got %+v", info.bySession)
	}
	if _, ok := info.bySession["C"]; ok {
		t.Errorf("C (not pinned) must NOT be in bySession, got %+v", info.bySession["C"])
	}
	if _, ok := info.byPath["/p/a"]; !ok {
		t.Errorf("byPath should contain /p/a (pinned), got %+v", info.byPath)
	}
	if _, ok := info.byPath["/p/c"]; ok {
		t.Errorf("byPath should NOT contain /p/c (unpinned), got %+v", info.byPath["/p/c"])
	}
	for _, p := range info.panes {
		if p.session == "C" {
			t.Errorf("panes list should not contain C, got %+v", p)
		}
	}
	if info.totalWaiting != 2 {
		t.Errorf("totalWaiting = %d, want 2 (A+B)", info.totalWaiting)
	}
}

// TestAggregateWaitingPinnedOnlyFalseBackwardCompat verifies that
// when PinnedOnly is false, the watcher behaves as before: every
// session's panes are eligible, regardless of the pinned snapshot.
// This protects users who explicitly set pinned_only = false in
// their config.
func TestAggregateWaitingPinnedOnlyFalseBackwardCompat(t *testing.T) {
	now := time.Now()
	old := now.Add(-5 * time.Minute)
	tmp := t.TempDir()
	oldTty := tmp + "/old"
	writeTtyMtime(t, oldTty, old)
	states := map[paneKey]paneState{
		{session: "X", window: "@1", paneIndex: 0}: {key: paneKey{session: "X", window: "@1", paneIndex: 0}, cmd: "claude", tty: oldTty, dir: "/p/x"},
		{session: "Y", window: "@2", paneIndex: 0}: {key: paneKey{session: "Y", window: "@2", paneIndex: 0}, cmd: "claude", tty: oldTty, dir: "/p/y"},
	}
	// Empty pinned set + PinnedOnly=false: the classic behavior must
	// still report both sessions as waiting.
	opts := defaultConfig.toWatchingConfig()
	opts.PinnedOnly = false
	info := aggregateWaitingForTestWithOptsPinned(states, paneKey{}, opts, nil, now)

	if _, ok := info.bySession["X"]; !ok {
		t.Errorf("X should be in bySession even with empty pinned, got %+v", info.bySession)
	}
	if _, ok := info.bySession["Y"]; !ok {
		t.Errorf("Y should be in bySession even with empty pinned, got %+v", info.bySession)
	}
	if info.totalWaiting != 2 {
		t.Errorf("totalWaiting = %d, want 2 (X+Y)", info.totalWaiting)
	}
}

// writeTtyMtime creates a file at path with the given mtime, mimicking a
// tty whose last write was at that moment. Used to deterministically
// exercise the ttyIdle check without touching a real /dev/pts device.
func writeTtyMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	f.Close()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestFormatProcsNoTruncate(t *testing.T) {
	parts := []procCount{{name: "claude", count: 1}, {name: "opencode", count: 1}}
	s, more := formatProcs(parts, 200)
	if more != 0 {
		t.Errorf("more = %d, want 0", more)
	}
	if !strings.Contains(s, "· claude") || !strings.Contains(s, "· opencode") {
		t.Errorf("missing process names in %q", s)
	}
}

func TestFormatProcsTruncate(t *testing.T) {
	parts := []procCount{{name: "claude", count: 2}, {name: "opencode", count: 1}, {name: "aider", count: 1}}
	// Width 14: fits "· claude ×2" (width 11) but not "· opencode" (next 10)
	s, more := formatProcs(parts, 14)
	if !strings.HasPrefix(s, "· claude") {
		t.Errorf("expected first segment visible, got %q", s)
	}
	if more != 2 {
		t.Errorf("more = %d, want 2", more)
	}
	if !strings.Contains(s, "+2") {
		t.Errorf("expected +2 tail in %q", s)
	}
}

func TestFormatProcsTooNarrow(t *testing.T) {
	parts := []procCount{{name: "claude", count: 2}, {name: "opencode", count: 1}}
	// Width 3: not even one segment fits, but the "+N" tail still renders.
	s, more := formatProcs(parts, 3)
	if more != 2 {
		t.Errorf("more = %d, want 2", more)
	}
	if !strings.Contains(s, "+2") {
		t.Errorf("expected +2 tail in %q", s)
	}
}

func TestFormatProcsCountSuffix(t *testing.T) {
	parts := []procCount{{name: "claude", count: 3}}
	s, more := formatProcs(parts, 80)
	if more != 0 {
		t.Errorf("more = %d, want 0", more)
	}
	if !strings.Contains(s, "×3") {
		t.Errorf("expected ×3 suffix in %q", s)
	}
}

func TestFormatProcsEmpty(t *testing.T) {
	s, more := formatProcs(nil, 100)
	if s != "" || more != 0 {
		t.Errorf("empty input should yield empty output, got %q more=%d", s, more)
	}
}

// TestWatchRespectsConfigCommands verifies that the user-configured commands
// list filters waiting detection: panes whose command isn't in the config's
// command list are not reported as waiting, even when their tty is idle.
func TestWatchRespectsConfigCommands(t *testing.T) {
	now := time.Now()
	old := now.Add(-5 * time.Minute)
	tmp := t.TempDir()
	oldTty := tmp + "/old"
	writeTtyMtime(t, oldTty, old)

	// Two panes, both with old tty mtime. One runs "claude" (in config),
	// the other runs "bash" (not in config).
	states := map[paneKey]paneState{
		{session: "A", window: "@1", paneIndex: 0}: {key: paneKey{session: "A", window: "@1", paneIndex: 0}, cmd: "claude", tty: oldTty, dir: "/p/a"},
		{session: "B", window: "@2", paneIndex: 0}: {key: paneKey{session: "B", window: "@2", paneIndex: 0}, cmd: "bash", tty: oldTty, dir: "/p/b"},
	}

	// Config that only allows "claude".
	cfg := Config{Waiting: WaitingConfig{Commands: []string{"claude"}}}
	opts := cfg.toWatchingConfig()

	info := aggregateWaitingForTestWith(states, paneKey{}, opts, now)

	if _, ok := info.bySession["A"]; !ok {
		t.Errorf("A (claude) should be reported as waiting, got %+v", info.bySession)
	}
	if _, ok := info.bySession["B"]; ok {
		t.Errorf("B (bash) should NOT be reported when not in config, got %+v", info.bySession["B"])
	}
}

// TestWaitingInfoLookupUsesSnapshot verifies that waitingInfo.lookup
// resolves entries without calling `tmux list-sessions`. Both
// session-name and path-typed entries work.
func TestWaitingInfoLookupUsesSnapshot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	workPath := home + "/work"
	info := waitingInfo{
		bySession: map[string][]procCount{"work": {{name: "claude", count: 1}}},
		byPath:    map[string][]procCount{workPath: {{name: "claude", count: 1}}},
		paths:     map[string]string{"work": workPath},
		version:   1,
	}

	if got := info.lookup("work"); len(got) != 1 || got[0].name != "claude" {
		t.Errorf("session-name lookup failed: %+v", got)
	}

	if got := info.lookup("~/work"); len(got) != 1 || got[0].name != "claude" {
		t.Errorf("path entry with ~ expansion failed: %+v", got)
	}

	// Unrelated entry: not in bySession, not a path → nil.
	if got := info.lookup("never-existed"); got != nil {
		t.Errorf("unknown entry should return nil, got %+v", got)
	}

	// Empty entry → nil.
	if got := info.lookup("   "); got != nil {
		t.Errorf("blank entry should return nil, got %+v", got)
	}

	// Zero-value waitingInfo: lookup must be a no-op.
	var zero waitingInfo
	if got := zero.lookup("anything"); got != nil {
		t.Errorf("zero waitingInfo.lookup should return nil, got %+v", got)
	}
}

// TestLookupDoesNotCrossNamespaces is the regression test for the
// bug where qs-* sessions (cwd = /home/kjelly/github/tmux-qs) all
// inherited the opencode agent running inside the tmux-qs session
// (also cwd = /home/kjelly/github/tmux-qs). The fix: session-name
// entries are looked up in bySession ONLY; path entries are looked
// up in byPath ONLY. No cross-fallthrough via the paths snapshot.
func TestLookupDoesNotCrossNamespaces(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	sharedCwd := home + "/github/tmux-qs"
	info := waitingInfo{
		// tmux-qs session has an opencode pane (this is the
		// "real" signal — correct, should show).
		bySession: map[string][]procCount{
			"tmux-qs":       {{name: "opencode", count: 1, signal: "prompt"}},
			"qs-1781252723": {}, // present in bySession but empty (no waiting)
		},
		// Both tmux-qs and the qs-* sessions share this cwd; the
		// aggregation put the opencode pane here too.
		byPath: map[string][]procCount{
			sharedCwd: {{name: "opencode", count: 1, signal: "prompt"}},
		},
		// The paths snapshot: qs-* sessions map to the shared cwd.
		paths: map[string]string{
			"tmux-qs":       sharedCwd,
			"qs-1781252723": sharedCwd,
		},
	}

	// tmux-qs lookup should find opencode (it's in bySession).
	if got := info.lookup("tmux-qs"); len(got) != 1 || got[0].name != "opencode" {
		t.Errorf("tmux-qs should report opencode via bySession, got %+v", got)
	}

	// qs-1781252723 lookup: entry is in bySession (empty list), so
	// the empty list is returned. Crucially, the opencode entry
	// from byPath[/home/.../tmux-qs] must NOT bleed through.
	if got := info.lookup("qs-1781252723"); len(got) != 0 {
		t.Errorf("qs-1781252723 should be empty (no waiting panes in this session), got %+v — this is the cross-namespace bug", got)
	}

	// Path lookup still works: ~/github/tmux-qs returns the byPath entry.
	if got := info.lookup(sharedCwd); len(got) != 1 || got[0].name != "opencode" {
		t.Errorf("path lookup should return byPath entry, got %+v", got)
	}
}

// TestIsWaitingFiltersByCommand verifies that isWaiting only returns true
// for panes whose foreground command is in opts.Commands ∪ opts.IdleShells.
// A pane running "nu" with all three signals firing (dead + idle tty +
// prompt regex hit in buffer) must still be ignored unless "nu" is in
// IdleShells.
func TestIsWaitingFiltersByCommand(t *testing.T) {
	now := time.Now()
	old := now.Add(-5 * time.Minute)
	tmp := t.TempDir()
	oldTty := tmp + "/old"
	writeTtyMtime(t, oldTty, old)

	// Build a state that fires ALL THREE signals for "nu":
	//   - pane_dead=1, deadAt within 30s (signal 1)
	//   - tty mtime 5 minutes ago (signal 2)
	//   - buffer matches [Y/n] prompt regex (signal 3)
	state := paneState{
		key:    paneKey{session: "S", window: "@1", paneIndex: 0},
		cmd:    "nu",
		tty:    oldTty,
		dead:   true,
		deadAt: now.Add(-30 * time.Second).Unix(),
		buf:    "Do you want to continue? [Y/n]",
	}

	// Case 1: nu is NOT in commands nor in idle_shells.
	// isWaiting must return "" even though all three signals fire.
	opts := Config{Waiting: WaitingConfig{
		Commands:   []string{"claude", "opencode"},
		IdleShells: nil,
	}}.toWatchingConfig()
	if got := isWaiting(state, opts, now, ""); got != "" {
		t.Errorf("nu with all 3 signals firing should NOT be waiting, got signal=%q", got)
	}

	// Case 2: nu is added to idle_shells. isWaiting must now return a
	// non-empty signal.
	opts2 := Config{Waiting: WaitingConfig{
		Commands:   []string{"claude", "opencode"},
		IdleShells: []string{"nu"},
	}}.toWatchingConfig()
	if got := isWaiting(state, opts2, now, ""); got == "" {
		t.Errorf("nu should be waiting when added to idle_shells")
	}

	// Case 3: claude in commands, all 3 signals fire — must be waiting
	// even without idle_shells.
	stateClaude := state
	stateClaude.cmd = "claude"
	if got := isWaiting(stateClaude, opts, now, ""); got == "" {
		t.Errorf("claude with all 3 signals firing should be waiting when in commands")
	}

	// Case 4: an unknown command (e.g. "weirdtool") — never waiting
	// regardless of signals.
	stateWeird := state
	stateWeird.cmd = "weirdtool"
	if got := isWaiting(stateWeird, opts2, now, ""); got != "" {
		t.Errorf("weirdtool should never be waiting, got signal=%q", got)
	}
}

// TestPromptRegex_OnlyStructuralPatterns verifies the default prompt
// regex set only matches STRUCTURAL prompt signals, not process-name
// substrings. This is the regression test for the bug where Claude
// Code's startup banner ("Claude Code v2.1.175", "Channeling...",
// "↑ 6.8k tokens") was being flagged as waiting because the buffer
// trivially contained the substring "claude".
func TestPromptRegex_OnlyStructuralPatterns(t *testing.T) {
	opts := defaultConfig.toWatchingConfig()
	patterns := opts.compiledPrompts()
	if len(patterns) == 0 {
		t.Fatal("default prompt regex set should not be empty")
	}

	// Each entry below is a buffer that USED to match the old
	// "claude"-as-regex default. They must all be ignored now.
	mustNotMatch := []string{
		"╭─── Claude Code v2.1.175 ───╮",
		"Welcome back jelly!",
		"Tip: Use /btw to ask a quick side question",
		"Channeling... (1m 55s · ↑ 6.8k tokens)",
		"Listing 1 directory… (ctrl+o to expand)",
		"Allowed by auto mode classifier",
		"Claude Team · Linkervision",
		"Fable 5 · Claude Team · Linkervision",
	}
	for _, buf := range mustNotMatch {
		if matchesAnyAgentPrompt(buf, patterns) {
			t.Errorf("prompt regex matched buffer that should be ignored: %q", buf)
		}
	}

	// These are structural prompt signals that MUST still match —
	// the user-facing prompts from various AI agents.
	mustMatch := []string{
		"Do you want to continue? [Y/n]",
		"Are you sure? [y/N]",
		"Press enter to continue?",
		"yes / no",           // very loose
		"human: hello there", // chat-style
		"assistant: hi",      // chat-style
		"continue?",          // bare "continue?" prompt
	}
	for _, buf := range mustMatch {
		if !matchesAnyAgentPrompt(buf, patterns) {
			t.Errorf("prompt regex should match structural prompt: %q", buf)
		}
	}
}
