package main

import (
	"testing"
	"time"
)

func TestProgressIncreasingIsBusy(t *testing.T) {
	opts := WatchingConfig{ProgressStaleAfter: 5 * time.Minute}
	now := time.Now()

	prev := paneCapture{
		progressState: "normal",
		progress:      30,
		progressValid: true,
	}
	curr := paneState{
		progressState: "normal",
		progress:      40,
		progressValid: true,
	}

	eval := evaluateProgress(curr, prev, opts, now)
	if !eval.isBusy {
		t.Errorf("expected isBusy == true when progress increases, got %+v", eval)
	}
}

func TestProgressStateTransitionIsBusy(t *testing.T) {
	opts := WatchingConfig{ProgressStaleAfter: 5 * time.Minute}
	now := time.Now()

	prev := paneCapture{
		progressState: "indeterminate",
		progressSince: now.Add(-10 * time.Second),
	}
	curr := paneState{
		progressState: "normal",
		progress:      10,
		progressValid: true,
	}

	eval := evaluateProgress(curr, prev, opts, now)
	if !eval.isBusy {
		t.Errorf("expected isBusy == true on state transition, got %+v", eval)
	}
}

func TestFirstObservedNormalFallsBackToLegacyDetection(t *testing.T) {
	opts := WatchingConfig{ProgressStaleAfter: 5 * time.Minute}
	now := time.Now()

	// No previous sample
	prev := paneCapture{}
	curr := paneState{
		progressState: "normal",
		progress:      50,
		progressValid: true,
	}

	eval := evaluateProgress(curr, prev, opts, now)
	if eval.isBusy {
		t.Errorf("first observed normal should not be busy (falls back to legacy), got %+v", eval)
	}
	if !eval.saveSample {
		t.Errorf("first observed normal should save sample for next tick")
	}
}

func TestInvalidNormalProgressFallsBackToLegacyDetection(t *testing.T) {
	opts := WatchingConfig{ProgressStaleAfter: 5 * time.Minute}
	now := time.Now()

	prev := paneCapture{
		progressState: "normal",
		progress:      30,
		progressValid: true,
	}
	curr := paneState{
		progressState: "normal",
		progress:      0,
		progressValid: false, // invalid progress
	}

	eval := evaluateProgress(curr, prev, opts, now)
	if eval.isBusy {
		t.Errorf("invalid normal progress must not be busy, got %+v", eval)
	}
	if eval.saveSample {
		t.Errorf("invalid normal progress should clear progress sample")
	}
}

func TestIndeterminateProgressIsBusyWithinLease(t *testing.T) {
	opts := WatchingConfig{ProgressStaleAfter: 5 * time.Minute}
	now := time.Now()

	prev := paneCapture{
		progressState: "indeterminate",
		progressSince: now.Add(-2 * time.Minute), // within 5m
	}
	curr := paneState{
		progressState: "indeterminate",
	}

	eval := evaluateProgress(curr, prev, opts, now)
	if !eval.isBusy {
		t.Errorf("indeterminate within lease should be busy, got %+v", eval)
	}
	if eval.nextSince != prev.progressSince {
		t.Errorf("should retain progressSince")
	}
}

func TestStaleIndeterminateFallsBackToIdleDetection(t *testing.T) {
	tmp := t.TempDir()
	tty := tmp + "/tty"
	now := time.Now()
	writeTtyMtime(t, tty, now.Add(-6*time.Minute))

	opts := defaultConfig.toWatchingConfig()
	opts.Idle = 30 * time.Second
	opts.ProgressStaleAfter = 5 * time.Minute

	prev := paneCapture{
		progressState: "indeterminate",
		progressSince: now.Add(-6 * time.Minute), // stale (>5m)
	}
	curr := paneState{
		cmd:           "claude",
		tty:           tty,
		progressState: "indeterminate",
	}

	eval := evaluateProgress(curr, prev, opts, now)
	if eval.isBusy {
		t.Errorf("stale indeterminate should not be busy, got %+v", eval)
	}
	curr.progressBusy = eval.isBusy
	sig := isWaiting(curr, opts, now, "")
	if sig != "idle" {
		t.Errorf("expected stale indeterminate to fall back to idle signal, got %q", sig)
	}
}

func TestErrorProgressIsWaitingSignal(t *testing.T) {
	opts := defaultConfig.toWatchingConfig()
	now := time.Now()

	s := paneState{
		cmd:           "claude",
		progressState: "error",
	}

	eval := evaluateProgress(s, paneCapture{}, opts, now)
	if !eval.isError {
		t.Errorf("expected isError == true, got %+v", eval)
	}
	sig := isWaiting(s, opts, now, "")
	if sig != "error" {
		t.Errorf("expected error waiting signal, got %q", sig)
	}
}

func TestPausedProgressFallsBackToPromptDetection(t *testing.T) {
	tmp := t.TempDir()
	promptTty := tmp + "/prompt"
	now := time.Now()
	writeTtyMtime(t, promptTty, now.Add(-2*time.Second))

	opts := defaultConfig.toWatchingConfig()
	s := paneState{
		cmd:           "claude",
		tty:           promptTty,
		buf:           "Do you want to continue? [Y/n]",
		progressState: "paused",
	}

	eval := evaluateProgress(s, paneCapture{}, opts, now)
	if eval.isBusy || eval.isError {
		t.Errorf("paused should not be busy or error, got %+v", eval)
	}
	s.progressBusy = eval.isBusy
	sig := isWaiting(s, opts, now, "")
	if sig != "prompt" {
		t.Errorf("expected prompt signal for paused progress with prompt buffer, got %q", sig)
	}
}

func TestHiddenProgressUsesLegacyDetection(t *testing.T) {
	opts := defaultConfig.toWatchingConfig()
	now := time.Now()

	s := paneState{
		cmd:           "claude",
		progressState: "hidden",
	}

	eval := evaluateProgress(s, paneCapture{}, opts, now)
	if eval.isBusy || eval.isError {
		t.Errorf("hidden should not be busy or error, got %+v", eval)
	}
}

func TestProgressBusySkipsCapturePane(t *testing.T) {
	withTestTmuxServer(t)
	server := getTmuxServer()
	caps := getTmuxCapabilities(server)

	// Create test session and pane
	_ = tmuxRun("new-session", "-d", "-s", "qs-busy-test")
	defer func() { _ = tmuxRun("kill-session", "-t", "qs-busy-test") }()

	opts := defaultConfig.toWatchingConfig()
	now := time.Now()

	prev := map[paneKey]paneCapture{
		{session: "qs-busy-test", window: "@1", paneIndex: 0}: {
			progressState: "normal",
			progress:      10,
			progressValid: true,
			buf:           "old-buffer",
		},
	}

	// Fake pane with progress increasing
	states := map[paneKey]paneState{
		{session: "qs-busy-test", window: "@1", paneIndex: 0}: {
			key:           paneKey{session: "qs-busy-test", window: "@1", paneIndex: 0},
			cmd:           "claude",
			progressState: "normal",
			progress:      20,
			progressValid: true,
		},
	}

	eval := evaluateProgress(states[paneKey{session: "qs-busy-test", window: "@1", paneIndex: 0}],
		prev[paneKey{session: "qs-busy-test", window: "@1", paneIndex: 0}], opts, now)
	if !eval.isBusy {
		t.Fatalf("expected isBusy == true")
	}
	_ = caps
}

func TestTmux36EmptyProgressFieldsUseLegacyDetection(t *testing.T) {
	opts := defaultConfig.toWatchingConfig()
	now := time.Now()

	s := paneState{
		cmd:           "claude",
		progressState: "",
		progress:      0,
		progressValid: false,
	}

	eval := evaluateProgress(s, paneCapture{}, opts, now)
	if eval.isBusy || eval.isError {
		t.Errorf("empty progress should not be busy or error, got %+v", eval)
	}
}

func TestWatchStatsCountEachDecision(t *testing.T) {
	stats := watchStats{
		PanesTotal:             5,
		CaptureTotal:           1,
		CaptureSkippedProgress: 2,
		CaptureReusedMtime:     1,
		WaitingPrompt:          1,
		WaitingStuck:           0,
		WaitingIdle:            1,
		WaitingError:           1,
	}
	if stats.PanesTotal != 5 {
		t.Errorf("unexpected PanesTotal: %d", stats.PanesTotal)
	}
	if stats.CaptureTotal+stats.CaptureSkippedProgress+stats.CaptureReusedMtime != 4 {
		t.Errorf("unexpected capture total sum: %d", stats.CaptureTotal+stats.CaptureSkippedProgress+stats.CaptureReusedMtime)
	}
	if stats.WaitingPrompt+stats.WaitingIdle+stats.WaitingError != 3 {
		t.Errorf("unexpected waiting sum: %d", stats.WaitingPrompt+stats.WaitingIdle+stats.WaitingError)
	}
}
