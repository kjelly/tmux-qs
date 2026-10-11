package main

import (
	"strings"
	"testing"
	"time"
)

func TestOutputCaptureRealTmux(t *testing.T) {
	withTestTmuxServer(t)
	withCleanCacheEnv(t)
	server := getTmuxServer()
	resetCapabilityCacheForTest()
	t.Cleanup(resetCapabilityCacheForTest)
	caps := getTmuxCapabilities(server)
	t.Logf("tmux %s, native output state: %v", caps.Version.Raw, caps.NativeOutputState)
	if err := tmuxRun("new-session", "-d", "-s", "native-output", "-x", "80", "-y", "24", "cat"); err != nil {
		t.Fatal(err)
	}
	paneID, err := tmuxRunOut("display-message", "-p", "-t", "native-output", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	opts := WatchingConfig{Commands: []string{"cat"}, Idle: 30 * time.Second}
	collect := func(previous *paneState) (paneState, watchStats) {
		t.Helper()
		var captures map[paneKey]paneCapture
		if previous != nil {
			captures = map[paneKey]paneCapture{previous.key: previous.captureSnapshot()}
		}
		states, stats, err := collectPanesOn(server, opts, captures, time.Now(), caps)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range states {
			if s.paneID == paneID {
				return s, stats
			}
		}
		t.Fatalf("pane %s absent from snapshot", paneID)
		return paneState{}, watchStats{}
	}
	await := func(condition func(paneState) bool) paneState {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			s, _ := collect(nil)
			if condition(s) {
				return s
			}
			if time.Now().After(deadline) {
				t.Fatalf("pane did not reach expected state: %+v", s)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	send := func(text string) {
		t.Helper()
		if err := tmuxRun("send-keys", "-t", paneID, "-l", text); err != nil {
			t.Fatal(err)
		}
		if err := tmuxRun("send-keys", "-t", paneID, "Enter"); err != nil {
			t.Fatal(err)
		}
	}
	send("FIRST-OUTPUT-MARKER")
	s := await(func(s paneState) bool { return s.captureValid && strings.Contains(s.buf, "FIRST-OUTPUT-MARKER") })
	if caps.NativeOutputState && (!s.output.native || s.lastOutputTime == 0) {
		t.Fatalf("native format values not collected: %+v", s)
	}
	// Wait for the echoed input to settle, bounded by the same deadline.
	deadline := time.Now().Add(3 * time.Second)
	for {
		next, stats := collect(&s)
		if stats.CaptureReusedNative+stats.CaptureReusedMtime > 0 {
			s = next
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("quiet pane capture was never reused")
		}
		s = next
	}
	old := s

	send("SECOND-OUTPUT-MARKER")
	s = await(func(s paneState) bool { return strings.Contains(s.buf, "SECOND-OUTPUT-MARKER") })
	if caps.NativeOutputState && s.canReuseCapture(old.captureSnapshot()) {
		t.Fatal("native new output reused old capture")
	}
	if err := tmuxRun("resize-window", "-t", "native-output", "-x", "90", "-y", "30"); err != nil {
		t.Fatal(err)
	}
	resized := await(func(s paneState) bool { return s.output.width == 90 && s.output.height == 30 })
	if resized.canReuseCapture(s.captureSnapshot()) {
		t.Fatal("resize reused old capture")
	}
	if err := tmuxRun("respawn-pane", "-k", "-t", paneID, "cat"); err != nil {
		t.Fatal(err)
	}
	respawned := await(func(s paneState) bool { return s.pid != resized.pid && s.cmd == "cat" })
	if respawned.canReuseCapture(resized.captureSnapshot()) {
		t.Fatal("respawn reused old capture")
	}
	if !caps.NativeOutputState {
		return // The same test above verifies the real older-tmux fallback.
	}
	for _, state := range []string{"turn complete", "approval needed"} {
		at := time.Now()
		if err := writePaneAgentEvent(server, paneID, agentEvent{Agent: "codex", State: state, At: at}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		send("HOOK-REDRAW-" + state)
		hook := await(func(s paneState) bool {
			return strings.Contains(s.buf, "HOOK-REDRAW-"+state) && s.agentEvent.OutputGeneration != nil && s.output.generation > *s.agentEvent.OutputGeneration
		})
		if got := isWaiting(hook, opts, at.Add(3*time.Second), ""); got != "event" {
			t.Fatalf("hook %q lost after redraw: %q", state, got)
		}
	}
	send("BEFORE-REVIEW")
	s = await(func(s paneState) bool { return strings.Contains(s.buf, "BEFORE-REVIEW") })
	if err := writePaneAgentEvent(server, paneID, agentEvent{Agent: "codex", State: "reviewed", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s = await(func(s paneState) bool { return s.agentEvent.OutputGeneration != nil && s.agentEventCurrent() })
	if s.agentEvent.OutputServer != s.output.serverIdentity || s.agentEvent.OutputPID != s.pid {
		t.Fatal("event stamp lost server/process identity")
	}
	send("AFTER-REVIEW")
	await(func(s paneState) bool { return strings.Contains(s.buf, "AFTER-REVIEW") && !s.agentEventCurrent() })
}
