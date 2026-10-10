package main

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeOutputCapabilityPerServer(t *testing.T) {
	resetCapabilityCacheForTest()
	t.Cleanup(resetCapabilityCacheForTest)
	for _, tc := range []struct {
		generation string
		want       bool
	}{
		{"0", true}, {"12", true}, {"", false}, {"-1", false},
		{"+1", false}, {"bad", false}, {"18446744073709551616", false},
	} {
		t.Run("generation="+tc.generation, func(t *testing.T) {
			server := tmuxServerSpec{flag: "-L", value: "output-" + tc.generation}
			runner := fakeCapabilityRunner{linesFunc: func(got tmuxServerSpec, args ...string) ([]string, error) {
				if got != server {
					t.Errorf("probe server = %+v, want %+v", got, server)
				}
				if args[0] == "list-panes" {
					return []string{"%1\tmaster\thidden\t" + tc.generation}, nil
				}
				return nil, nil
			}}
			caps := getTmuxCapabilitiesWithRunner(server, runner)
			if caps.NativeOutputState != tc.want {
				t.Fatalf("NativeOutputState = %v, want %v", caps.NativeOutputState, tc.want)
			}
			if cached := getTmuxCapabilitiesWithRunner(server, fakeCapabilityRunner{}); cached.NativeOutputState != tc.want {
				t.Fatalf("cached capabilities = %+v", cached)
			}
		})
	}
}

func TestNativeOutputIdleAndInvalidTimeFallback(t *testing.T) {
	now := time.Unix(1700000100, 0)
	tty := t.TempDir() + "/tty"
	writeTtyMtime(t, tty, now.Add(-time.Minute))
	opts := WatchingConfig{Commands: []string{"codex"}, Idle: 30 * time.Second}
	for _, tc := range []struct {
		name, raw, tty string
		want           time.Duration
	}{
		{"native without tty", "1700000040", "", time.Minute},
		{"native takes precedence", "1700000100", tty, 0},
		{"empty", "", tty, time.Minute},
		{"zero", "0", tty, time.Minute},
		{"negative", "-1", tty, time.Minute},
		{"future", "1700000101", tty, time.Minute},
		{"overflow", "18446744073709551615", tty, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := paneState{cmd: "codex", tty: tc.tty, lastOutputTime: parseLastOutputTime(tc.raw, now)}
			if got := s.outputIdle(now); got != tc.want {
				t.Fatalf("idle = %v, want %v", got, tc.want)
			}
			want := ""
			if tc.want >= opts.Idle {
				want = "idle"
			}
			if got := isWaiting(s, opts, now, ""); got != want {
				t.Fatalf("waiting signal = %q, want %q", got, want)
			}
			if state := agentState(want, ""); state == "turn complete" {
				t.Fatal("inactivity reported completion")
			}
		})
	}
}

// Metadata fixture uses the real list-panes field order. The fake runner
// exercises collection, buffer reuse and capture failure, not just equality.
func outputPaneFields(now time.Time) []string {
	return []string{"work", "@1", "0", "/work", "codex", "101", "0", "0", "", "%1", "codex", "hidden", "0", "", "1", "1", "", strconv.FormatInt(now.Add(-time.Minute).Unix(), 10), "1", "500:1700000000", "80", "24", "0", "0", "0"}
}

func outputFixtureRunner(fields *[]string, captures *atomic.Int64, captureErr *error, server tmuxServerSpec) fakeCapabilityRunner {
	return fakeCapabilityRunner{
		linesFunc: func(got tmuxServerSpec, args ...string) ([]string, error) {
			if got != server {
				return nil, errors.New("wrong server")
			}
			return []string{strings.Join(*fields, "\t")}, nil
		},
		outFunc: func(got tmuxServerSpec, args ...string) (string, error) {
			if got != server || args[0] != "capture-pane" || args[3] != (*fields)[9] {
				return "", errors.New("wrong capture target")
			}
			captures.Add(1)
			return "output " + (*fields)[18], *captureErr
		},
	}
}

func TestOutputCaptureReuseAndInvalidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		field  int
		value  string
		reused bool
	}{
		{"unchanged", 18, "1", true},
		{"new output", 18, "2", false},
		{"generation reset", 18, "0", false},
		{"invalid generation", 18, "bad", false},
		{"replaced pane", 9, "%2", false},
		{"respawn", 5, "102", false},
		{"server restarted", 19, "501:1700000001", false},
		{"missing server identity", 19, "", false},
		{"width", 20, "100", false},
		{"height", 21, "30", false},
		{"alternate screen", 22, "1", false},
		{"history cleared", 23, "1", false},
		{"mode", 24, "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1700000100, 0)
			fields := outputPaneFields(now)
			server := tmuxServerSpec{flag: "-L", value: "output-test"}
			var captures atomic.Int64
			var captureErr error
			runner := outputFixtureRunner(&fields, &captures, &captureErr, server)
			opts := WatchingConfig{Commands: []string{"codex"}}
			key := paneKey{session: "work", window: "@1"}
			states, _, err := collectPanesWithRunner(server, opts, nil, now, TmuxCapabilities{NativeOutputState: true}, runner)
			if err != nil || !states[key].captureValid || captures.Load() != 1 {
				t.Fatalf("initial capture: states=%+v count=%d err=%v", states, captures.Load(), err)
			}
			previous := map[paneKey]paneCapture{key: states[key].captureSnapshot()}
			fields[tc.field] = tc.value
			states, stats, err := collectPanesWithRunner(server, opts, previous, now, TmuxCapabilities{NativeOutputState: true}, runner)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := int64(2)
			if tc.reused {
				wantCount = 1
			}
			if captures.Load() != wantCount || (stats.CaptureReusedNative == 1) != tc.reused {
				t.Fatalf("captures=%d stats=%+v, reuse=%v", captures.Load(), stats, tc.reused)
			}
			if states[key].buf != "output "+fields[18] {
				t.Fatalf("unexpected buffer %q", states[key].buf)
			}
		})
	}
}

func TestOutputCaptureFallbackFailureAndProgress(t *testing.T) {
	now := time.Unix(1700000100, 0)
	fields := outputPaneFields(now)
	server := tmuxServerSpec{flag: "-S", value: "/tmp/output-server"}
	var captures atomic.Int64
	var captureErr error
	runner := outputFixtureRunner(&fields, &captures, &captureErr, server)
	opts := WatchingConfig{Commands: []string{"codex"}}
	key := paneKey{session: "work", window: "@1"}
	tick := func(previous map[paneKey]paneCapture) paneState {
		t.Helper()
		states, _, err := collectPanesWithRunner(server, opts, previous, now, TmuxCapabilities{ProgressBar: true}, runner)
		if err != nil {
			t.Fatal(err)
		}
		return states[key]
	}

	// A failed capture must never become a reusable empty buffer.
	captureErr = errors.New("capture failed")
	s := tick(nil)
	if s.captureValid {
		t.Fatal("failed capture was marked valid")
	}
	captureErr = nil
	s = tick(map[paneKey]paneCapture{key: s.captureSnapshot()})
	if captures.Load() != 2 || !s.captureValid {
		t.Fatal("failed capture was not retried")
	}

	// Busy progress retains the stamp of the old captured screen. Once
	// progress stops changing, generation 2 must be captured, not reused.
	pc := s.captureSnapshot()
	pc.progressState, pc.progress, pc.progressValid = "normal", 50, true
	fields[11], fields[12], fields[18] = "normal", "70", "2"
	s = tick(map[paneKey]paneCapture{key: pc})
	if !s.progressBusy || s.capturedOutput.generation != 1 || captures.Load() != 2 {
		t.Fatalf("progress skip advanced capture stamp: %+v", s)
	}
	pc = s.captureSnapshot()
	pc.progressState, pc.progress, pc.progressValid = "normal", 70, true
	s = tick(map[paneKey]paneCapture{key: pc})
	if s.progressBusy || s.buf != "output 2" || captures.Load() != 3 {
		t.Fatalf("progress completion reused old buffer: %+v", s)
	}

	// Unsupported formats use the legacy mtime, with the same identity
	// and geometry safeguards; TTY writes invalidate that fallback cache.
	tty := t.TempDir() + "/tty"
	writeTtyMtime(t, tty, now.Add(-time.Minute))
	fields[8], fields[17], fields[18], fields[11] = tty, "", "", "hidden"
	s = tick(nil)
	count := captures.Load()
	s = tick(map[paneKey]paneCapture{key: s.captureSnapshot()})
	if captures.Load() != count || s.output.native {
		t.Fatal("legacy mtime cache was not reused")
	}
	if err := os.Chtimes(tty, now, now); err != nil {
		t.Fatal(err)
	}
	s = tick(map[paneKey]paneCapture{key: s.captureSnapshot()})
	if captures.Load() != count+1 {
		t.Fatal("legacy TTY write did not invalidate capture")
	}
}

func TestNativeOutputInvalidatesAgentEventsWithinSameSecond(t *testing.T) {
	now := time.Unix(1700000100, 0)
	generation := uint64(10)
	opts := WatchingConfig{Commands: []string{"codex"}, Idle: 30 * time.Second}
	for _, state := range []string{"reviewed", "quiet"} {
		t.Run(state, func(t *testing.T) {
			s := paneState{cmd: "codex", pid: 101, lastOutputTime: now.Unix(), output: paneOutputStamp{native: true, generation: 10, serverIdentity: "500:1"}, agentEvent: agentEvent{State: state, At: now, OutputGeneration: &generation, OutputServer: "500:1", OutputPID: 101}}
			if state == "quiet" {
				s.agentEvent.SnoozeUntil = now.Add(time.Minute)
			}
			want := "event"
			if state == "reviewed" || state == "quiet" {
				want = ""
			}
			if got := isWaiting(s, opts, now, ""); got != want {
				t.Fatalf("current event = %q, want %q", got, want)
			}
			s.output.generation++
			if s.agentEventCurrent() || isWaiting(s, opts, now, "") != "" {
				t.Fatal("same-second output did not invalidate event")
			}
			s.lastOutputTime = now.Add(-time.Minute).Unix()
			if got := isWaiting(s, opts, now, ""); got != "idle" {
				t.Fatalf("stale event suppressed reevaluation: %q", got)
			}
		})
	}
}

func TestNativeWaitingPreservesScopesAndProgress(t *testing.T) {
	now := time.Unix(1700000100, 0)
	key := paneKey{session: "work", window: "@1"}
	base := paneState{key: key, cmd: "codex", paneID: "%1", lastOutputTime: now.Add(-time.Minute).Unix()}
	opts := WatchingConfig{Commands: []string{"codex"}, Idle: 30 * time.Second}
	for _, tc := range []struct {
		name   string
		change func(*paneState, *WatchingConfig)
		want   string
	}{
		{"quiet", func(*paneState, *WatchingConfig) {}, "quiet"},
		{"unmonitored", func(s *paneState, _ *WatchingConfig) { s.cmd = "sh" }, ""},
		{"unpinned", func(_ *paneState, o *WatchingConfig) { o.PinnedOnly = true }, ""},
		{"busy progress", func(s *paneState, _ *WatchingConfig) { s.progressBusy = true }, ""},
		{"error progress", func(s *paneState, _ *WatchingConfig) { s.progressState = "error" }, "error"},
		{"internal pane", func(s *paneState, _ *WatchingConfig) { s.key = paneKey{} }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, config := base, opts
			tc.change(&s, &config)
			info := aggregateWaiting(map[paneKey]paneState{s.key: s}, paneKey{}, config, nil, nil, nil, now)
			if tc.want == "" {
				if len(info.panes) != 0 {
					t.Fatalf("out-of-scope pane reported waiting: %+v", info.panes)
				}
				return
			}
			if len(info.panes) != 1 || info.panes[0].state != tc.want || info.panes[0].ttyIdle != time.Minute {
				t.Fatalf("waiting state = %+v, want %s with native age", info.panes, tc.want)
			}
		})
	}
}

func TestProgressTimeoutBeforeFirstCapture(t *testing.T) {
	now := time.Unix(1700000100, 0)
	fields := outputPaneFields(now)
	fields[11] = "indeterminate"
	server := tmuxServerSpec{flag: "-L", value: "progress-first-capture"}
	var captures atomic.Int64
	var captureErr error
	runner := outputFixtureRunner(&fields, &captures, &captureErr, server)
	opts := WatchingConfig{Commands: []string{"codex"}, ProgressStaleAfter: 30 * time.Second}
	caps := TmuxCapabilities{ProgressBar: true}
	key := paneKey{session: "work", window: "@1"}
	states, _, err := collectPanesWithRunner(server, opts, nil, now, caps, runner)
	if err != nil {
		t.Fatal(err)
	}
	s := states[key]
	if !s.progressBusy || s.captureValid || captures.Load() != 0 {
		t.Fatalf("first progress tick = %+v, captures=%d", s, captures.Load())
	}
	pc := s.captureSnapshot()
	pc.progressState, pc.progressSince = "indeterminate", now
	states, _, err = collectPanesWithRunner(server, opts, map[paneKey]paneCapture{key: pc}, now.Add(31*time.Second), caps, runner)
	if err != nil {
		t.Fatal(err)
	}
	if got := states[key]; got.progressBusy || !got.captureValid || captures.Load() != 1 {
		t.Fatalf("stale progress restarted instead of capturing: %+v", got)
	}
}

func TestNativeAgentHookRedrawTolerance(t *testing.T) {
	now := time.Unix(1700000100, 0)
	generation := uint64(10)
	opts := WatchingConfig{Commands: []string{"codex"}, Idle: 30 * time.Second}
	for _, state := range []string{"turn complete", "approval needed"} {
		t.Run(state, func(t *testing.T) {
			s := paneState{cmd: "codex", pid: 101, lastOutputTime: now.Unix(), output: paneOutputStamp{native: true, generation: 11, serverIdentity: "500:1"}, agentEvent: agentEvent{State: state, At: now, OutputGeneration: &generation, OutputServer: "500:1", OutputPID: 101}}
			if got := isWaiting(s, opts, now.Add(3*time.Second), ""); got != "event" {
				t.Fatalf("hook redraw lost waiting event: %q", got)
			}
			s.lastOutputTime = now.Add(3 * time.Second).Unix()
			if s.agentEventCurrent() {
				t.Fatal("later activity retained hook event")
			}
			s.lastOutputTime = now.Unix()
			s.pid++
			if s.agentEventCurrent() {
				t.Fatal("respawn retained hook event")
			}
			s.pid--
			s.output.serverIdentity = "501:1"
			if s.agentEventCurrent() {
				t.Fatal("server replacement retained hook event")
			}
		})
	}
}
