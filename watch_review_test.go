package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWatchSourceSignaturesMatchDisplayedFields(t *testing.T) {
	fields := make([]string, 28)
	fields[0], fields[2], fields[3], fields[4], fields[9] = "alpha", "0", "/work/one", "sh", "%1"
	fields[19], fields[25], fields[26], fields[27] = "123:456", "0", "/rooted", "1"
	before, _ := watchSourceTopologiesFromLines([]string{strings.Join(fields, "\t")})
	fields[3], fields[4] = "/work/two", "vim"
	after, _ := watchSourceTopologiesFromLines([]string{strings.Join(fields, "\t")})
	for _, src := range []sourceKind{srcDefault, srcAll} {
		if before[src] != after[src] {
			t.Fatal("transient command/cwd reloaded sessions")
		}
	}
	for _, src := range []sourceKind{srcPanes, srcTmux} {
		if before[src] == after[src] {
			t.Fatal("pane display change was missed")
		}
	}
	fields[27] = "2"
	changed, _ := watchSourceTopologiesFromLines([]string{strings.Join(fields, "\t")})
	if changed[srcDefault] == after[srcDefault] {
		t.Fatal("window count change was missed")
	}
	fields[13] = "1"
	internal, _ := watchSourceTopologiesFromLines([]string{strings.Join(fields, "\t")})
	if internal[srcPanes] != "0\n" || internal[srcDefault] == changed[srcDefault] {
		t.Fatal("internal picker counted as a user pane")
	}
}

func TestWatchOrdinaryLoadSeedsSourceBaseline(t *testing.T) {
	withIsolatedEnv(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	m := newModel()
	for _, src := range []sourceKind{srcDefault, srcTmux, srcPanes} {
		m.loadGeneration++
		next, _ := m.Update(itemsMsg{src: src, generation: m.loadGeneration, items: []string{"alpha"}, sourceTopology: "stable"})
		m = next.(model)
		next, _ = m.Update(watchMsg{info: waitingInfo{sourceTopologies: map[sourceKind]string{src: "stable"}}})
		m = next.(model)
		if m.watchSourceLoading {
			t.Fatal("unchanged source reloaded after ordinary load")
		}
	}
}

func TestWatchLoadBaselineMatchesNextCollection(t *testing.T) {
	withTestTmuxServer(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	if !getTmuxCapabilities(getTmuxServer()).EventMonitors {
		t.Skip("requires native source refresh")
	}
	for _, src := range []sourceKind{srcDefault, srcTmux, srcPanes} {
		loaded, ok := loadCmd(src, 1, "", nil)().(itemsMsg)
		if !ok || loaded.sourceTopology == "" {
			t.Fatal("ordinary load omitted baseline")
		}
		collected := collectWatchMessage(getTmuxServer(), paneKey{}, WatchingConfig{}, nil, 0, nil, time.Now())
		if collected.err != nil {
			t.Fatal(collected.err)
		}
		if loaded.sourceTopology != collected.info.sourceTopologies[src] {
			t.Fatal("ordinary load and periodic collection used different signatures")
		}
	}
}

func TestWatchRefreshKeepsSelectionThroughLateAnnotations(t *testing.T) {
	withIsolatedEnv(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	m := newModel()
	m.src, m.mode, m.loading = srcDefault, modeList, false
	m.items = []string{"/a/nongit", "/b/git"}
	m.zoxideScores = map[string]float64{"/a/nongit": 100, "/b/git": 1}
	m.annots["/b/git"] = "main"
	m.dirty["/b/git"] = true
	m.errText = "previous diagnostic"
	m.refilter()
	m.restoreSelected("/a/nongit")
	next, _ := m.Update(itemsMsg{src: srcDefault, generation: m.loadGeneration, items: m.items, zoxideScores: m.zoxideScores, backgroundTopology: "new"})
	m = next.(model)
	if m.annots["/b/git"] != "main" || !m.dirty["/b/git"] || m.errText != "previous diagnostic" {
		t.Fatal("background reload cleared annotations, dirty marks or diagnostic")
	}
	next, _ = m.Update(annotMsg{generation: m.loadGeneration, entries: m.items, values: map[string]string{"/b/git": "topic"}})
	m = next.(model)
	selected, _ := m.selected()
	if selected != "/a/nongit" {
		t.Fatalf("late sorting moved selection to %q", selected)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if next.(model).result != "/a/nongit" {
		t.Fatal("Enter connected to a different directory")
	}
	next, _ = m.Update(dirtyMsg{generation: m.loadGeneration, entries: []string{"/b/git"}})
	if next.(model).dirty["/b/git"] {
		t.Fatal("fresh clean result retained stale dirty mark")
	}
}

func TestWatchStaleRefreshDoesNotWedgeAfterBranches(t *testing.T) {
	withIsolatedEnv(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	m := newModel()
	m.mode, m.src, m.loading = modeList, srcDefault, false
	m.items = []string{"/work/repo"}
	m.refilter()
	m.loadGeneration, m.watchSourceGeneration, m.watchSourceLoading = 7, 7, true
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(model)
	if m.loadGeneration != 8 {
		t.Fatal("Ctrl-b did not supersede background load")
	}
	next, _ = m.Update(branchesMsg{generation: 8, repo: "/work/repo", branches: []branchEntry{{name: "main"}}})
	m = next.(model)
	next, _ = m.Update(itemsMsg{src: srcDefault, generation: 7, items: []string{"late"}, backgroundTopology: "new"})
	m = next.(model)
	if m.watchSourceLoading || m.items[0] != "/work/repo" || m.mode != modeBranch {
		t.Fatal("stale refresh wedged loading or replaced branches")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	next, _ = m.Update(watchMsg{info: waitingInfo{sourceTopologies: map[sourceKind]string{srcDefault: "changed"}}})
	if !next.(model).watchSourceLoading {
		t.Fatal("refresh did not resume after Esc")
	}
}

func TestWatchEventDoesNotBypassErrorBackoff(t *testing.T) {
	withTestTmuxServer(t)
	withIsolatedEnv(t)
	hub := newWatchEventHub(t.Context())
	defer hub.close()
	hub.changed <- struct{}{}
	started := time.Now()
	watchCmd(paneKey{}, WatchingConfig{Poll: time.Second}, nil, 100*time.Millisecond, 1, nil, hub)()
	if time.Since(started) < 100*time.Millisecond {
		t.Fatal("event bypassed error retry delay")
	}
}

func TestWatchDisabledOrOldServerDoesNotRefreshSources(t *testing.T) {
	withTestTmuxServer(t)
	for _, disabled := range []bool{false, true} {
		msg := collectWatchMessage(getTmuxServer(), paneKey{}, WatchingConfig{DisableEvents: disabled}, nil, 0, nil, time.Now())
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		if (disabled || !getTmuxCapabilities(getTmuxServer()).EventMonitors) && len(msg.info.sourceTopologies) != 0 {
			t.Fatal("disabled/unsupported events caused automatic source refresh")
		}
	}
}

func TestMonitorStartupRemovesOnlyDeadOwners(t *testing.T) {
	withTestTmuxServer(t)
	if !getTmuxCapabilities(getTmuxServer()).EventMonitors {
		t.Skip("requires native monitors")
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	start, _ := watchProcessIdentity(child.Process.Pid)
	_ = child.Process.Kill()
	_ = child.Wait()
	selfStart, _ := watchProcessIdentity(os.Getpid())
	name := func(pid int, start string) string {
		return fmt.Sprintf("@tmux_qs_watch_v1_%d_%s_%s_%s", pid, start, strings.Repeat("a", 32), strings.Repeat("b", 32))
	}
	dead, live := name(child.Process.Pid, start), name(os.Getpid(), selfStart)
	reused := name(os.Getpid(), selfStart+"1")
	legacy := "@tmux_qs_watch_other_instance"
	custom := dead + "_custom"
	for _, hook := range []string{dead, live, reused, legacy, custom} {
		if err := tmuxRun("set-hook", "-g", "-B", hook+"::#{session_name}"); err != nil {
			t.Fatal(err)
		}
	}
	hub := newWatchEventHub(t.Context())
	defer hub.close()
	hub.configure([]tmuxServerEndpoint{{spec: getTmuxServer()}}, "")
	select {
	case <-hub.changed:
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not start")
	}
	hub.close()
	hooks, err := tmuxRunOut("show-hooks", "-g", "-B")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hooks, dead+"::") || strings.Contains(hooks, reused+"::") {
		t.Fatalf("dead or reused PID owner remained: %s", hooks)
	}
	for _, hook := range []string{live, legacy, custom} {
		if !strings.Contains(hooks, hook+"::") {
			t.Fatalf("removed another owner/custom hook: %s", hooks)
		}
	}
}
