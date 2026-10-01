package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseAgentEvents(t *testing.T) {
	now := time.Now()
	codex, err := parseAgentEvent("codex", []byte(`{"type":"agent-turn-complete","thread-id":"abc","cwd":"/work","input-messages":["Fix build"],"last-assistant-message":"Tests pass"}`), now)
	if err != nil || codex.State != "turn complete" || codex.Conversation != "abc" || codex.Task != "Fix build" || codex.Summary != "Tests pass" {
		t.Fatalf("Codex event = %+v, %v", codex, err)
	}
	claude, err := parseAgentEvent("claude", []byte(`{"hook_event_name":"PermissionRequest","session_id":"xyz","cwd":"/work"}`), now)
	if err != nil || claude.State != "approval needed" || claude.Conversation != "xyz" {
		t.Fatalf("Claude event = %+v, %v", claude, err)
	}
	if _, err := parseAgentEvent("claude", []byte(`{"hook_event_name":"Notification","notification_type":"other"}`), now); err == nil {
		t.Fatal("unknown hook notification should not be stored")
	}
}

func TestAgentEventStoredOnPane(t *testing.T) {
	withTestTmuxServer(t)
	pane, err := tmuxRunOut("display-message", "-p", "-t", "qs-test-root", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	data := `{"type":"agent-turn-complete","thread-id":"thread-1","cwd":"/work","last-assistant-message":"Done"}`
	if err := runAgentEvent("codex", strings.NewReader(data), pane, getTmuxServer()); err != nil {
		t.Fatal(err)
	}
	got := readPaneAgentEvent(getTmuxServer(), pane)
	if got.Agent != "codex" || got.State != "turn complete" || got.Conversation != "thread-1" || got.Summary != "Done" {
		t.Fatalf("stored pane event = %+v", got)
	}
	opts := defaultConfig.toWatchingConfig()
	opts.PinnedOnly = false
	states, _, err := collectPanesOn(getTmuxServer(), opts, nil, time.Now(), getTmuxCapabilities(getTmuxServer()))
	if err != nil {
		t.Fatal(err)
	}
	info := aggregateWaiting(states, paneKey{}, opts, nil, nil, nil, time.Now())
	if len(info.panes) != 1 || info.panes[0].paneID != pane || info.panes[0].state != "turn complete" {
		t.Fatalf("watcher did not surface pane event: %+v", info.panes)
	}
}

func TestAcknowledgePaneSuppressesEventUntilNewOutput(t *testing.T) {
	withTestTmuxServer(t)
	target, err := resolvePaneTarget("qs-test-root")
	if err != nil {
		t.Fatal(err)
	}
	if err := acknowledgePaneAgent(target); err != nil {
		t.Fatal(err)
	}
	event := readPaneAgentEvent(target.server, target.paneID)
	if event.State != "reviewed" {
		t.Fatalf("stored state = %q", event.State)
	}
	state := paneState{cmd: target.command, ttyMtime: event.At.Add(-time.Minute).UnixNano(), agentEvent: event}
	if got := isWaiting(state, defaultConfig.toWatchingConfig(), event.At.Add(time.Second), ""); got != "" {
		t.Fatalf("reviewed pane reported waiting: %q", got)
	}
}

func TestSnoozePaneSuppressesThenReturnsToWaiting(t *testing.T) {
	withTestTmuxServer(t)
	target, err := resolvePaneTarget("qs-test-root")
	if err != nil {
		t.Fatal(err)
	}
	if err := snoozePaneAgent(target, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	event := readPaneAgentEvent(target.server, target.paneID)
	state := paneState{cmd: target.command, ttyMtime: event.At.Add(-time.Minute).UnixNano(), agentEvent: event}
	if got := isWaiting(state, defaultConfig.toWatchingConfig(), event.At.Add(5*time.Minute), ""); got != "" {
		t.Fatalf("snoozed pane reported waiting: %q", got)
	}
	if got := isWaiting(state, defaultConfig.toWatchingConfig(), event.At.Add(11*time.Minute), ""); got != "event" {
		t.Fatalf("snoozed pane did not reappear: %q", got)
	}
}

func TestResumeSnippetsUseSavedConversationOnlyInSameDirectory(t *testing.T) {
	target := paneTarget{command: "bash", path: "/work"}
	event := agentEvent{Agent: "codex", Conversation: "thread-1", Cwd: "/work"}
	got := resumeSnippetChoices(target, event)
	if len(got) != 3 || got[0].Text != "codex resume 'thread-1'" || !got[0].Submit {
		t.Fatalf("resume choices = %+v", got)
	}
	target.path = "/other"
	got = resumeSnippetChoices(target, event)
	if len(got) != 2 || strings.Contains(got[0].Name, "this Codex") {
		t.Fatalf("cross-directory conversation resume = %+v", got)
	}
	target.command = "nvim"
	if got := resumeSnippetChoices(target, event); len(got) != 0 {
		t.Fatalf("non-shell resume choices = %+v", got)
	}
}
