package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWatchEventsCoalesceAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	raw := make(chan struct{}, 1000)
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { defer close(done); coalesceWatchEvents(ctx, raw, out, 10*time.Millisecond) }()
	for range 1000 {
		raw <- struct{}{}
	}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("events not delivered")
	}
	select {
	case <-out:
		t.Fatal("burst caused duplicate scan")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coalescer leaked")
	}
}

func TestWatchEventPeriodicCorrectionAndCancellation(t *testing.T) {
	if !eventWait(t.Context(), nil, time.Millisecond) {
		t.Fatal("periodic correction did not run")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if eventWait(ctx, nil, time.Hour) {
		t.Fatal("cancelled watcher ran")
	}
	changed := make(chan struct{}, 1)
	changed <- struct{}{}
	start := time.Now()
	if !eventWait(t.Context(), changed, time.Hour) || time.Since(start) > time.Second {
		t.Fatal("event did not bypass poll")
	}
}

func TestEventMonitorCapabilities(t *testing.T) {
	for _, tc := range []struct {
		generation, hook, wait string
		want                   bool
	}{
		{"0", "set-hook [-g] [-B name:what:format]", "wait-for [-ELSUv] [-F format]", true},
		{"", "set-hook [-g] [-B name:what:format]", "wait-for [-ELSUv] [-F format]", false},
		{"0", "set-hook [-g]", "wait-for [-ELSUv] [-F format]", false},
		{"0", "set-hook [-g] [-B name:what:format]", "wait-for [-LSU]", false},
	} {
		r := fakeCapabilityRunner{linesFunc: func(_ tmuxServerSpec, args ...string) ([]string, error) {
			if args[0] == "list-panes" {
				return []string{"%1\t3.8\thidden\t" + tc.generation}, nil
			}
			return []string{tc.hook, tc.wait}, nil
		}}
		caps, err := probeServerCapabilitiesWithRunner(tmuxServerSpec{}, r)
		if err != nil {
			t.Fatal(err)
		}
		if caps.EventMonitors != tc.want {
			t.Fatalf("%+v -> %+v", tc, caps)
		}
	}
}

func TestMonitorFormatIncludesExplicitAgentStateAndExcludesInternal(t *testing.T) {
	format := watchMonitorFormat("%1")
	for _, part := range []string{"#{S:", "#{W:", "#{P:", "pane_output_generation", "@tmux_qs_agent_event", "?@tmux_qs", "#{==:#{pane_id},%1}", "pane_last_prompt_time"} {
		if !strings.Contains(format, part) {
			t.Fatalf("missing %s in %s", part, format)
		}
	}
}

func TestAllServerPreviewSignalsDoNotMixPaneIDs(t *testing.T) {
	a := tmuxServerSpec{flag: "-L", value: "a"}
	b := tmuxServerSpec{flag: "-L", value: "b"}
	m := model{}
	info := waitingInfo{outputByServer: map[string]map[string]string{a.cacheKey(): {"%0": "first"}, b.cacheKey(): {"%0": "second"}}}
	if got := m.outputPreviewSignal(info, "[a] pane\t[a] session\t%0"); got != "first" {
		t.Fatalf("a signal %q", got)
	}
	if got := m.outputPreviewSignal(info, "[b] pane\t[b] session\t%0"); got != "second" {
		t.Fatalf("b signal %q", got)
	}
}

func TestMonitorNativeEventsReconnectAndCleanup(t *testing.T) {
	withTestTmuxServer(t)
	server := getTmuxServer()
	if !getTmuxCapabilities(server).EventMonitors {
		t.Skip("requires monitor hooks and event waits")
	}
	hub := newWatchEventHub(t.Context())
	defer hub.close()
	hub.configure([]tmuxServerEndpoint{{spec: server}}, "")
	wait := func() {
		t.Helper()
		select {
		case <-hub.changed:
		case <-time.After(5 * time.Second):
			t.Fatal("no monitor event before next 30s poll")
		}
	}
	wait()
	format, err := tmuxRunOut("display-message", "-p", watchMonitorFormat(""))
	if err != nil || format == "" || strings.Contains(format, "#{") {
		t.Fatalf("aggregate format not expanded: %q %v", format, err)
	}
	pane, _ := tmuxRunOut("display-message", "-p", "#{pane_id}")
	// Monitors seed their initial value on the first one-second sample.
	// Keep changing a test-only option until a subsequent sample emits.
	deadline := time.Now().Add(4 * time.Second)
	seen := false
	for time.Now().Before(deadline) {
		if err := tmuxRun("set-option", "-p", "-t", pane, agentEventOption, fmt.Sprintf(`{"state":"approval","sample":%d}`, time.Now().UnixNano())); err != nil {
			t.Fatal(err)
		}
		select {
		case <-hub.changed:
			seen = true
		case <-time.After(100 * time.Millisecond):
		}
		if seen {
			break
		}
	}
	if !seen {
		t.Fatal("no Agent state monitor event")
	}
	// Cancellation must remove only this instance's hooks and waiter.
	other := "@tmux_qs_watch_other_instance"
	if err := tmuxRun("set-hook", "-g", "-B", other+"::#{session_name}"); err != nil {
		t.Fatal(err)
	}
	hub.close()
	hooks, _ := tmuxRunOut("show-hooks", "-g", "-B")
	if strings.Contains(hooks, hub.nonce) || !strings.Contains(hooks, other) {
		t.Fatalf("hook ownership cleanup %s", hooks)
	}
	// Reconnection is also exercised against a restarted private server.
	hub = newWatchEventHub(t.Context())
	defer hub.close()
	hub.configure([]tmuxServerEndpoint{{spec: server}}, "")
	wait()
	if err := tmuxRun("kill-server"); err != nil {
		t.Fatal(err)
	}
	if err := tmuxRun("new-session", "-d", "-s", "reconnected", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	wait()
}

func TestMonitorNativeOutputWakesWatchBeforePoll(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	withTestTmuxServer(t)
	if !getTmuxCapabilities(getTmuxServer()).EventMonitors {
		t.Skip("requires native monitors")
	}
	hub := newWatchEventHub(t.Context())
	defer hub.close()
	opts := WatchingConfig{Poll: 30 * time.Second, Idle: 30 * time.Second}
	cmd := watchCmd(paneKey{}, opts, nil, 30*time.Second, 0, nil, hub)
	done := make(chan any, 1)
	go func() { done <- cmd() }()
	select {
	case result := <-done:
		msg, ok := result.(watchMsg)
		if !ok || msg.err != nil {
			t.Fatalf("watch result %#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event did not wake watcher")
	}
	// Drain installation notification before changing pane output.
	select {
	case <-hub.changed:
	default:
	}
	pane, _ := tmuxRunOut("display-message", "-p", "#{pane_id}")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if err := tmuxRun("send-keys", "-t", pane, "echo event-test", "Enter"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-hub.changed:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("output event missing")
}

func TestWatchBackgroundRefreshPreservesInputAndSelection(t *testing.T) {
	withIsolatedEnv(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	m := newModel()
	m.src = srcDefault
	m.mode = modeList
	m.items = []string{"alpha", "beta"}
	m.input.SetValue("b")
	m.refilter()
	m.loadGeneration = 4
	next, _ := m.Update(itemsMsg{src: srcDefault, generation: 4, items: []string{"alpha", "beta", "bravo"}, backgroundTopology: "new panes"})
	got := next.(model)
	selected, _ := got.selected()
	if got.input.Value() != "b" || selected != "beta" || got.watchSourceTopology != "new panes" {
		t.Fatalf("refresh clobbered input/selection %+v %q", got.input.Value(), selected)
	}
	got.mode = modeSnippetSelect
	got.items = []string{"Continue"}
	next, _ = got.Update(itemsMsg{src: srcDefault, generation: 4, items: []string{"late"}, backgroundTopology: "newer panes"})
	if next.(model).items[0] != "Continue" {
		t.Fatal("background refresh replaced snippet view")
	}
}

func TestWatchBackgroundRefreshRemovesLastVisiblePane(t *testing.T) {
	withIsolatedEnv(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	m := newModel()
	m.src, m.mode, m.loading = srcTmux, modeList, false
	m.items = []string{"removed-session"}
	m.refilter()
	m.watchSourceTopology = "previous panes"
	topology := watchTopology(map[paneKey]paneState{})
	next, _ := m.Update(watchMsg{info: waitingInfo{topology: topology}})
	got := next.(model)
	if !got.watchSourceLoading {
		t.Fatal("last visible pane removal did not request a source refresh")
	}
	next, _ = got.Update(itemsMsg{src: srcTmux, generation: got.loadGeneration, backgroundTopology: topology})
	if len(next.(model).items) != 0 || next.(model).watchSourceLoading {
		t.Fatal("source refresh retained the removed session")
	}
}

func TestWatchEventsCanBeDisabled(t *testing.T) {
	off := false
	if defaultConfig.toWatchingConfig().DisableEvents {
		t.Fatal("events unexpectedly disabled by default")
	}
	cfg := defaultConfig
	cfg.Waiting.EventUpdates = &off
	if !cfg.toWatchingConfig().DisableEvents {
		t.Fatal("event_updates=false ignored")
	}
	hub := newWatchEventHub(t.Context())
	defer hub.close()
	hub.configure(nil, "")
	if len(hub.workers) != 0 {
		t.Fatal("disabled events started subprocess")
	}
}

func TestAllServerNativeSnapshotsIsolateIdenticalPaneIDs(t *testing.T) {
	withTestTmuxServer(t)
	a := getTmuxServer()
	b := tmuxServerSpec{flag: "-L", value: a.value + "-second"}
	if err := tmuxRunOn(b, "new-session", "-d", "-s", "qs-test-root", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tmuxRunOn(b, "kill-server") })
	// Make the first panes stable and distinct while both keep ID %0.
	if err := tmuxRunOn(a, "respawn-pane", "-k", "-t", "qs-test-root", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	paneA, _ := tmuxRunOutOn(a, "display-message", "-p", "#{pane_id}")
	paneB, _ := tmuxRunOutOn(b, "display-message", "-p", "#{pane_id}")
	if paneA != paneB {
		t.Fatalf("test requires identical IDs: %s %s", paneA, paneB)
	}
	// Wait until shell startup output has settled before stamping the event.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cmd, _ := tmuxRunOutOn(a, "display-message", "-p", "#{pane_current_command}")
		if cmd == "sh" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	event := agentEvent{Agent: "codex", State: "approval", At: time.Now(), Task: "server-a"}
	if err := writePaneAgentEvent(a, paneA, event); err != nil {
		t.Fatal(err)
	}
	endpoints := []tmuxServerEndpoint{{label: "a", spec: a}, {label: "b", spec: b}}
	opts := WatchingConfig{Commands: []string{"sh"}, Poll: 30 * time.Second, Idle: time.Hour}
	result := collectAllWatchMessages(endpoints, paneKey{}, opts, nil, 0, nil, time.Now())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.info.bySession["[a] qs-test-root"]) == 0 || len(result.info.bySession["[b] qs-test-root"]) != 0 {
		t.Fatalf("cross-server Waiting leak: %+v", result.info.bySession)
	}
	if len(result.captures) != 2 || len(result.info.outputByServer) != 2 {
		t.Fatalf("merged same-ID snapshots: captures=%d signals=%d", len(result.captures), len(result.info.outputByServer))
	}
}

func TestMonitorOSCCommandEndDoesNotCompleteAgent(t *testing.T) {
	withTestTmuxServer(t)
	if !getTmuxCapabilities(getTmuxServer()).EventMonitors {
		t.Skip("requires native OSC formats")
	}
	pane, err := tmuxRunOut("new-window", "-d", "-t", "qs-test-root", "-P", "-F", "#{pane_id}", "/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := tmuxRun("send-keys", "-t", pane, `printf '\033]133;C\007\033]133;D;0\007\033]133;A\007'`, "Enter"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ended, _ := tmuxRunOut("display-message", "-p", "-t", pane, "#{pane_command_end_time}")
		if ended != "" && ended != "0" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	msg := collectWatchMessage(getTmuxServer(), paneKey{}, WatchingConfig{Commands: []string{"codex", "claude"}, Idle: time.Hour}, nil, 0, nil, time.Now())
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	for _, p := range msg.info.panes {
		if p.paneID == pane {
			t.Fatalf("OSC command end became Agent state: %+v", p)
		}
	}
}

func TestWatchBackgroundRefreshErrorAllowsRetry(t *testing.T) {
	m := model{watchSourceLoading: true, loadGeneration: 9, watchOpt: WatchingConfig{Poll: time.Second}}
	next, _ := m.Update(watchSourceRefreshErrMsg{generation: 9, err: errors.New("source unavailable")})
	got := next.(model)
	if got.watchSourceLoading || got.errText != "source unavailable" || !got.watchSourceRetryAfter.After(time.Now()) {
		t.Fatalf("background refresh cannot recover: loading=%v err=%q", got.watchSourceLoading, got.errText)
	}
}
