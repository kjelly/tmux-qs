package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBackendConfigDefaultAndFallback(t *testing.T) {
	// Default config
	cfg := defaultConfig
	capsTrue := TmuxCapabilities{FloatingPane: true}
	capsFalse := TmuxCapabilities{FloatingPane: false}

	b1 := resolvePickerBackend(cfg, capsTrue)
	if _, ok := b1.(*popupBackend); !ok {
		t.Errorf("default backend = %T, want *popupBackend", b1)
	}

	// Unknown value fallback
	cfgUnknown := Config{UI: UIConfig{Backend: "invalid-backend"}}
	b2 := resolvePickerBackend(cfgUnknown, capsTrue)
	if _, ok := b2.(*popupBackend); !ok {
		t.Errorf("unknown backend fallback = %T, want *popupBackend", b2)
	}

	// Floating requested but capability false -> popup
	cfgFloat := Config{UI: UIConfig{Backend: "floating"}}
	b3 := resolvePickerBackend(cfgFloat, capsFalse)
	if _, ok := b3.(*popupBackend); !ok {
		t.Errorf("floating backend without capability = %T, want *popupBackend", b3)
	}

	// Floating requested and capability true -> floating
	b4 := resolvePickerBackend(cfgFloat, capsTrue)
	if _, ok := b4.(*floatingBackend); !ok {
		t.Errorf("floating backend with capability = %T, want *floatingBackend", b4)
	}
}

func TestMetadataLineParserAndExactClientMatching(t *testing.T) {
	lines := []string{
		"%1\t1\t1\tclient-A\tnonce-1",
		"%2\t1\t0\tclient-A\tnonce-2", // not tmux-qs internal
		"%3\t0\t1\tclient-A\tnonce-3", // not floating
		"%4\t1\t1\tclient-B\tnonce-4", // different client
		"%5\t1\t1\tclient-A\tnonce-5", // matching
	}

	targetClient := "client-A"
	var matching []string
	for _, l := range lines {
		parts := strings.Split(l, "\t")
		if len(parts) >= 4 && parts[1] == "1" && parts[2] == "1" && parts[3] == targetClient {
			matching = append(matching, parts[0])
		}
	}

	want := []string{"%1", "%5"}
	if !reflect.DeepEqual(matching, want) {
		t.Fatalf("matching panes = %v, want %v", matching, want)
	}

	// Empty client name must never match
	var emptyMatches []string
	for _, l := range lines {
		parts := strings.Split(l, "\t")
		if len(parts) >= 4 && parts[1] == "1" && parts[2] == "1" && parts[3] == "" {
			emptyMatches = append(emptyMatches, parts[0])
		}
	}
	if len(emptyMatches) != 0 {
		t.Fatalf("empty client matched panes: %v", emptyMatches)
	}
}

func TestDuplicateInternalPaneRecovery(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-dup-recovery"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingPane, "floating panes")

	client := "test-dup-client"
	// Create two duplicate floating panes for this client
	out1, err1 := tmuxRunOut("new-pane", "-t", session, "-x", "30", "-y", "10", "-X", "0", "-Y", "0", "-P", "-F", "#{pane_id}", "sleep", "30")
	out2, err2 := tmuxRunOut("new-pane", "-t", session, "-x", "30", "-y", "10", "-X", "0", "-Y", "0", "-P", "-F", "#{pane_id}", "sleep", "30")
	if err1 != nil || err2 != nil {
		t.Fatalf("failed to create floating panes: %v, %v", err1, err2)
	}
	p1 := strings.TrimSpace(out1)
	p2 := strings.TrimSpace(out2)
	defer func() {
		_ = tmuxRun("kill-pane", "-t", p1)
		_ = tmuxRun("kill-pane", "-t", p2)
	}()

	_ = tmuxRun("set-option", "-p", "-t", p1, "@tmux_qs", "1")
	_ = tmuxRun("set-option", "-p", "-t", p1, "@tmux_qs_client", client)
	_ = tmuxRun("set-option", "-p", "-t", p2, "@tmux_qs", "1")
	_ = tmuxRun("set-option", "-p", "-t", p2, "@tmux_qs_client", client)

	backend := &floatingBackend{}
	req := PickerToggleRequest{
		Context: popupContext{client: client, pane: p1, cwd: "/tmp"},
	}

	res, err := backend.Toggle(req)
	if err != nil {
		t.Fatalf("Toggle error: %v", err)
	}
	if !res.Consumed || !res.Closed {
		t.Fatalf("Toggle result = %+v, want Consumed=true, Closed=true", res)
	}

	// Both duplicate panes should be killed
	if tmuxPaneExists(getTmuxServer(), p1) {
		t.Errorf("pane %s should have been killed", p1)
	}
	if tmuxPaneExists(getTmuxServer(), p2) {
		t.Errorf("pane %s should have been killed", p2)
	}
}

func TestCapabilityFalsePopupFallback(t *testing.T) {
	restore := setCapabilitiesForTest(getTmuxServer(), TmuxCapabilities{FloatingPane: false})
	defer restore()

	backend := &floatingBackend{}
	req := PickerOpenRequest{
		Context: popupContext{client: "client-1", pane: "%1", cwd: "/tmp"},
	}
	err := backend.Open(req)
	if err != errFallbackPopup {
		t.Fatalf("Open returned %v, want errFallbackPopup", err)
	}
}

func TestInternalPaneWaitingExclusion(t *testing.T) {
	if !isInternalTmuxQSPane("1") {
		t.Error("isInternalTmuxQSPane('1') should be true")
	}
	if isInternalTmuxQSPane("0") {
		t.Error("isInternalTmuxQSPane('0') should be false")
	}
	if isInternalTmuxQSPane("") {
		t.Error("isInternalTmuxQSPane('') should be false")
	}
}

func TestResurrectExcludesOnlyInternalPanes(t *testing.T) {
	lines := []string{
		// Normal pane
		"work\t/w\t0\tedit\tlay0\t0\t/w\tnvim\t0\t0",
		// User's floating pane (pane_floating_flag=1, @tmux_qs=0) -> NOT excluded
		"work\t/w\t0\tedit\tlay0\t1\t/w\tterminal\t1\t0",
		// tmux-qs internal floating pane (@tmux_qs=1) -> EXCLUDED
		"work\t/w\t0\tedit\tlay0\t2\t/tmp\ttmux-qs\t1\t1",
	}

	sessions := buildResurrectSessions(lines)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	panes := sessions[0].Windows[0].Panes
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(panes))
	}
	if panes[0].Command != "nvim" {
		t.Errorf("pane 0 command = %q, want nvim", panes[0].Command)
	}
	if panes[1].Command != "terminal" {
		t.Errorf("pane 1 command = %q, want terminal", panes[1].Command)
	}
}

func TestLegacyResurrectFixturesStillParse(t *testing.T) {
	// 8-column legacy fixture without @tmux_qs columns
	lines := []string{
		"sessionA\t/pathA\t0\twinA\tlayoutA\t0\t/pathA\tbash",
		"sessionB\t/pathB\t0\twinB\tlayoutB\t0\t/pathB\tzsh",
	}
	sessions := buildResurrectSessions(lines)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions from legacy fixtures, got %d", len(sessions))
	}
	if sessions[0].Name != "sessionA" || sessions[1].Name != "sessionB" {
		t.Errorf("sessions = %+v", sessions)
	}
}

func TestFloatingNewPaneReturnsPaneID(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingPane, "floating panes")

	const session = "qs-float-newp"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	out, err := tmuxRunOut("new-pane", "-t", session, "-x", "40", "-y", "15", "-X", "5", "-Y", "5", "-P", "-F", "#{pane_id}\t#{pane_floating_flag}", "sleep", "30")
	if err != nil {
		t.Fatalf("new-pane error: %v", err)
	}
	fields := strings.Split(strings.TrimSpace(out), "\t")
	if len(fields) != 2 {
		t.Fatalf("unexpected output: %q", out)
	}
	paneID := fields[0]
	isFloating := fields[1]
	defer tmuxRun("kill-pane", "-t", paneID)

	if !strings.HasPrefix(paneID, "%") {
		t.Errorf("paneID = %q, want prefix '%%'", paneID)
	}
	if isFloating != "1" {
		t.Errorf("pane_floating_flag = %q, want 1", isFloating)
	}
}

func TestFloatingMetadataWrittenToPaneScope(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingPane, "floating panes")

	const session = "qs-float-meta"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	out, err := tmuxRunOut("new-pane", "-t", session, "-x", "40", "-y", "15", "-X", "5", "-Y", "5", "-P", "-F", "#{pane_id}", "sleep", "30")
	if err != nil {
		t.Fatalf("new-pane error: %v", err)
	}
	paneID := strings.TrimSpace(out)
	defer tmuxRun("kill-pane", "-t", paneID)

	server := getTmuxServer()
	if err := tmuxRunOn(server, "set-option", "-p", "-t", paneID, "@tmux_qs", "1"); err != nil {
		t.Fatalf("set @tmux_qs: %v", err)
	}
	if err := tmuxRunOn(server, "set-option", "-p", "-t", paneID, "@tmux_qs_client", "exact-client-1"); err != nil {
		t.Fatalf("set @tmux_qs_client: %v", err)
	}
	if err := tmuxRunOn(server, "set-option", "-p", "-t", paneID, "@tmux_qs_instance", "nonce-123"); err != nil {
		t.Fatalf("set @tmux_qs_instance: %v", err)
	}

	val, err := tmuxRunOutOn(server, "show-options", "-p", "-v", "-t", paneID, "@tmux_qs")
	if err != nil || strings.TrimSpace(val) != "1" {
		t.Errorf("show @tmux_qs = %q (err %v), want 1", val, err)
	}
	clientVal, err := tmuxRunOutOn(server, "show-options", "-p", "-v", "-t", paneID, "@tmux_qs_client")
	if err != nil || strings.TrimSpace(clientVal) != "exact-client-1" {
		t.Errorf("show @tmux_qs_client = %q (err %v), want exact-client-1", clientVal, err)
	}
}

func TestFloatingChildExitCleansUpPane(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingPane, "floating panes")

	const session = "qs-float-exit"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	out, err := tmuxRunOut("new-pane", "-t", session, "-x", "30", "-y", "10", "-X", "0", "-Y", "0", "-P", "-F", "#{pane_id}", "sleep", "0.1")
	if err != nil {
		t.Fatalf("new-pane: %v", err)
	}
	paneID := strings.TrimSpace(out)

	// Wait for sleep 0.1 to finish and pane to disappear
	deadline := time.Now().Add(2 * time.Second)
	exited := false
	for time.Now().Before(deadline) {
		if !tmuxPaneExists(getTmuxServer(), paneID) {
			exited = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !exited {
		t.Errorf("pane %s did not exit automatically", paneID)
	}
}

func TestFloatingToggleKillsOnlyMatchingClientPanes(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingPane, "floating panes")

	const session = "qs-float-multi-client"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	outA, errA := tmuxRunOut("new-pane", "-t", session, "-x", "30", "-y", "10", "-X", "0", "-Y", "0", "-P", "-F", "#{pane_id}", "sleep", "30")
	outB, errB := tmuxRunOut("new-pane", "-t", session, "-x", "30", "-y", "10", "-X", "0", "-Y", "0", "-P", "-F", "#{pane_id}", "sleep", "30")
	if errA != nil || errB != nil {
		t.Fatalf("new-pane error: %v, %v", errA, errB)
	}
	paneA := strings.TrimSpace(outA)
	paneB := strings.TrimSpace(outB)
	defer func() {
		_ = tmuxRun("kill-pane", "-t", paneA)
		_ = tmuxRun("kill-pane", "-t", paneB)
	}()

	server := getTmuxServer()
	_ = tmuxRunOn(server, "set-option", "-p", "-t", paneA, "@tmux_qs", "1")
	_ = tmuxRunOn(server, "set-option", "-p", "-t", paneA, "@tmux_qs_client", "client-A")
	_ = tmuxRunOn(server, "set-option", "-p", "-t", paneB, "@tmux_qs", "1")
	_ = tmuxRunOn(server, "set-option", "-p", "-t", paneB, "@tmux_qs_client", "client-B")

	backend := &floatingBackend{}
	reqA := PickerToggleRequest{
		Context: popupContext{client: "client-A", pane: paneA, cwd: "/tmp"},
	}

	res, err := backend.Toggle(reqA)
	if err != nil {
		t.Fatalf("Toggle error: %v", err)
	}
	if !res.Consumed || !res.Closed {
		t.Fatalf("Toggle result = %+v, want Consumed=true, Closed=true", res)
	}

	// paneA should be killed
	if tmuxPaneExists(server, paneA) {
		t.Errorf("paneA (%s) should have been killed", paneA)
	}
	// paneB must STILL exist!
	if !tmuxPaneExists(server, paneB) {
		t.Errorf("paneB (%s) should NOT have been killed", paneB)
	}
}

func TestFloatingMetadataFailureCleansUpPane(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingPane, "floating panes")

	const session = "qs-float-cleanup"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	out, err := tmuxRunOut("new-pane", "-t", session, "-x", "30", "-y", "10", "-X", "0", "-Y", "0", "-P", "-F", "#{pane_id}", "sleep", "30")
	if err != nil {
		t.Fatalf("new-pane: %v", err)
	}
	paneID := strings.TrimSpace(out)

	server := getTmuxServer()
	// Simulate metadata failure and cleanup
	errClean := cleanupFailedFloatingPane(server, paneID)
	if errClean != nil {
		t.Fatalf("cleanupFailedFloatingPane error: %v", errClean)
	}
	if tmuxPaneExists(server, paneID) {
		t.Errorf("pane %s should have been cleaned up", paneID)
	}
}

func TestResurrectSnapshotExcludesInternalPane(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingPane, "floating panes")

	const session = "qs-float-resurrect"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	out, err := tmuxRunOut("new-pane", "-t", session, "-x", "30", "-y", "10", "-X", "0", "-Y", "0", "-P", "-F", "#{pane_id}", "sleep", "30")
	if err != nil {
		t.Fatalf("new-pane: %v", err)
	}
	paneID := strings.TrimSpace(out)
	defer tmuxRun("kill-pane", "-t", paneID)

	server := getTmuxServer()
	_ = tmuxRunOn(server, "set-option", "-p", "-t", paneID, "@tmux_qs", "1")

	state, err := captureResurrectState()
	if err != nil {
		t.Fatalf("captureResurrectState error: %v", err)
	}

	for _, s := range state.Sessions {
		if s.Name == session {
			for _, w := range s.Windows {
				for _, p := range w.Panes {
					if strings.Contains(p.Command, "sleep 30") && len(w.Panes) > 1 {
						// There should be only the 1 normal pane, not the internal floating pane
					}
				}
				if len(w.Panes) > 1 {
					t.Fatalf("window in session %s has %d panes, expected internal pane to be excluded", session, len(w.Panes))
				}
			}
		}
	}
}

func TestWaitingCollectorDoesNotCaptureInternalPane(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingPane, "floating panes")

	const session = "qs-float-waiting"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	out, err := tmuxRunOut("new-pane", "-t", session, "-x", "30", "-y", "10", "-X", "0", "-Y", "0", "-P", "-F", "#{pane_id}", "claude") // claude is allowed command
	if err != nil {
		t.Fatalf("new-pane: %v", err)
	}
	paneID := strings.TrimSpace(out)
	defer tmuxRun("kill-pane", "-t", paneID)

	server := getTmuxServer()
	_ = tmuxRunOn(server, "set-option", "-p", "-t", paneID, "@tmux_qs", "1")

	cfg := defaultConfig.toWatchingConfig().compilePatterns()
	states, _, err := collectPanesOn(server, cfg, nil, time.Now(), caps)
	if err != nil {
		t.Fatalf("collectPanesOn error: %v", err)
	}

	for _, st := range states {
		if st.paneID == paneID {
			t.Fatalf("internal pane %s should not be collected by waiting detector", paneID)
		}
	}
}

func TestInternalPaneSourceExclusion(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingPane, "floating panes")

	const session = "qs-src-excl"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	out, err := tmuxRunOut("new-pane", "-t", session, "-x", "30", "-y", "10", "-X", "0", "-Y", "0", "-P", "-F", "#{pane_id}", "sleep", "30")
	if err != nil {
		t.Fatalf("new-pane: %v", err)
	}
	internalPaneID := strings.TrimSpace(out)
	defer tmuxRun("kill-pane", "-t", internalPaneID)

	server := getTmuxServer()
	_ = tmuxRunOn(server, "set-option", "-p", "-t", internalPaneID, "@tmux_qs", "1")

	// 1. loadPanes
	panes, err := loadPanes()
	if err != nil {
		t.Fatalf("loadPanes: %v", err)
	}
	for _, p := range panes {
		if strings.Contains(p, internalPaneID) {
			t.Fatalf("internal pane %s found in loadPanes: %s", internalPaneID, p)
		}
	}

	// 2. loadTmuxPanes
	tmuxPanes, err := loadTmuxPanes()
	if err != nil {
		t.Fatalf("loadTmuxPanes: %v", err)
	}
	for _, p := range tmuxPanes {
		if strings.Contains(p, internalPaneID) {
			t.Fatalf("internal pane %s found in loadTmuxPanes: %s", internalPaneID, p)
		}
	}

	// 3. windowsCmd
	cmd := windowsCmd(session, server, "", 1)
	msg := cmd()
	if wMsg, ok := msg.(windowsMsg); ok {
		for _, item := range wMsg.items {
			if strings.Contains(item, internalPaneID) {
				t.Fatalf("internal pane %s found in windowsCmd: %s", internalPaneID, item)
			}
		}
	}
}
