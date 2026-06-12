package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestIsWaitingSignalClassification verifies each signal type fires
// under the right conditions.
func TestIsWaitingSignalClassification(t *testing.T) {
	now := nowForTest()
	tmp := t.TempDir()
	oldTty := tmp + "/old"
	writeTtyMtime(t, oldTty, now.Add(-5*time.Minute))
	newTty := tmp + "/new"
	promptTty := tmp + "/prompt"
	writeTtyMtime(t, promptTty, now.Add(-2*time.Second)) // idle for 2s

	opts := defaultConfig.toWatchingConfig()

	// Signal "dead" — pane_dead=1, deadAt recent.
	sDead := paneState{cmd: "claude", tty: newTty, dead: true, deadAt: now.Add(-30 * time.Second).Unix()}
	if got := isWaiting(sDead, opts, now, ""); got != "dead" {
		t.Errorf("expected dead signal, got %q", got)
	}

	// Signal "prompt" — buffer matches a structural prompt regex.
	sPrompt := paneState{cmd: "claude", tty: promptTty, buf: "Continue? [Y/n]"}
	if got := isWaiting(sPrompt, opts, now, ""); got != "prompt" {
		t.Errorf("expected prompt signal, got %q", got)
	}

	// Signal "stuck" — buffer unchanged AND tty idle.
	sStuck := paneState{cmd: "claude", tty: oldTty, buf: "still thinking..."}
	if got := isWaiting(sStuck, opts, now, "still thinking..."); got != "stuck" {
		t.Errorf("expected stuck signal, got %q", got)
	}

	// Signal "idle" — tty idle but buffer changed (or no prev buf).
	sIdle := paneState{cmd: "claude", tty: oldTty, buf: "thinking..."}
	if got := isWaiting(sIdle, opts, now, ""); got != "idle" {
		t.Errorf("expected idle signal, got %q", got)
	}

	// No signal — fresh tty, no dead, no prompt match.
	sNone := paneState{cmd: "claude", tty: newTty}
	if got := isWaiting(sNone, opts, now, ""); got != "" {
		t.Errorf("expected no signal, got %q", got)
	}
}

// TestFormatProcsIncludesSignalHint verifies the per-process signal tag
// (e.g. "· claude[prompt]") is included when present.
func TestFormatProcsIncludesSignalHint(t *testing.T) {
	parts := []procCount{
		{name: "claude", count: 1, signal: "prompt"},
		{name: "opencode", count: 2, signal: "stuck"},
	}
	s, more := formatProcs(parts, 200)
	if more != 0 {
		t.Errorf("more = %d, want 0", more)
	}
	if !strings.Contains(s, "· claude[prompt]") {
		t.Errorf("missing [prompt] tag: %q", s)
	}
	if !strings.Contains(s, "· opencode ×2[stuck]") {
		t.Errorf("missing [stuck] tag: %q", s)
	}
}

// TestFormatProcsOmitsEmptySignalTag verifies no extra brackets when
// signal is empty (e.g. for older aggregation results on disk).
func TestFormatProcsOmitsEmptySignalTag(t *testing.T) {
	parts := []procCount{{name: "claude", count: 1}}
	s, _ := formatProcs(parts, 200)
	if strings.Contains(s, "[]") {
		t.Errorf("empty signal should not render, got %q", s)
	}
	if !strings.Contains(s, "· claude ") && !strings.HasSuffix(strings.TrimSpace(s), "claude") {
		t.Errorf("missing claude: %q", s)
	}
}

// TestAggregateWaitingPopulatesPanes verifies the per-pane list is
// populated alongside the deduped bySession/byPath maps.
func TestAggregateWaitingPopulatesPanes(t *testing.T) {
	now := nowForTest()
	old := now.Add(-5 * time.Minute)
	tmp := t.TempDir()
	oldTty := tmp + "/old"
	writeTtyMtime(t, oldTty, old)
	states := map[paneKey]paneState{
		{session: "A", window: "@1", paneIndex: 0}: {key: paneKey{session: "A", window: "@1", paneIndex: 0}, cmd: "claude", tty: oldTty, dir: "/p/a"},
		{session: "A", window: "@1", paneIndex: 1}: {key: paneKey{session: "A", window: "@1", paneIndex: 1}, cmd: "claude", tty: oldTty, dir: "/p/a"},
	}
	info := aggregateWaitingForTest(states, paneKey{}, now)
	if len(info.panes) != 2 {
		t.Errorf("panes length = %d, want 2", len(info.panes))
	}
	for i, p := range info.panes {
		if p.session != "A" {
			t.Errorf("panes[%d].session = %q, want A", i, p.session)
		}
		if p.cmd != "claude" {
			t.Errorf("panes[%d].cmd = %q, want claude", i, p.cmd)
		}
		if p.signal == "" {
			t.Errorf("panes[%d].signal should be set", i)
		}
	}
}

// TestPanesForSessionEntry verifies the detail lookup for a session
// entry returns the right panes.
func TestPanesForSessionEntry(t *testing.T) {
	now := nowForTest()
	old := now.Add(-5 * time.Minute)
	tmp := t.TempDir()
	oldTty := tmp + "/old"
	writeTtyMtime(t, oldTty, old)
	states := map[paneKey]paneState{
		{session: "A", window: "@1", paneIndex: 0}: {key: paneKey{session: "A", window: "@1", paneIndex: 0}, cmd: "claude", tty: oldTty},
		{session: "B", window: "@2", paneIndex: 0}: {key: paneKey{session: "B", window: "@2", paneIndex: 0}, cmd: "claude", tty: oldTty},
	}
	info := aggregateWaitingForTest(states, paneKey{}, now)
	if got := info.panesFor("A"); len(got) != 1 {
		t.Errorf("panesFor(A) = %d, want 1", len(got))
	}
	if got := info.panesFor("B"); len(got) != 1 {
		t.Errorf("panesFor(B) = %d, want 1", len(got))
	}
	if got := info.panesFor("nonexistent"); got != nil {
		t.Errorf("panesFor(nonexistent) = %+v, want nil", got)
	}
}

// TestCtrlSpaceTogglesShowDetail verifies the binding.
func TestCtrlSpaceTogglesShowDetail(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel()
	m.items = []string{"work"}
	m.filtered = []int{0}
	if m.showDetail {
		t.Fatal("showDetail should default to false")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlAt})
	m2 := updated.(model)
	if !m2.showDetail {
		t.Errorf("ctrl+@ should toggle showDetail on, got %v", m2.showDetail)
	}
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlAt})
	m3 := updated.(model)
	if m3.showDetail {
		t.Errorf("ctrl+@ again should toggle showDetail off, got %v", m3.showDetail)
	}
}

// nowForTest is a thin wrapper for time.Now used by tests that need
// a stable reference. (Tests for the watcher use time.Now() inline
// to keep the call sites self-documenting; this helper exists for
// shared use across multiple cases.)
func nowForTest() time.Time { return time.Now() }
