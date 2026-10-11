package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestWatchRefreshPreservesDirectoryThroughRealLoader(t *testing.T) {
	for _, src := range []sourceKind{srcDefault, srcAll} {
		t.Run(fmt.Sprint(src), func(t *testing.T) {
			withTestTmuxServer(t)
			withIsolatedEnv(t)
			resetConfigCache()
			t.Cleanup(resetConfigCache)
			dirs := []string{filepath.Join(t.TempDir(), "nongit"), filepath.Join(t.TempDir(), "git"), filepath.Join(t.TempDir(), "removed")}
			for _, dir := range dirs {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			bin := t.TempDir()
			zoxide := filepath.Join(bin, "zoxide")
			writeZoxide := func(includeRemoved bool) {
				t.Helper()
				data := "100 " + dirs[0] + "\n1 " + dirs[1] + "\n"
				if includeRemoved {
					data += "1 " + dirs[2] + "\n"
				}
				if err := os.WriteFile(zoxide, []byte("#!/bin/sh\ncat <<'DATA'\n"+data+"DATA\n"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			writeZoxide(true)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			m := newModel(false, false, false)
			loaded := loadCmd(src, m.loadGeneration, "", nil)().(itemsMsg)
			next, _ := m.Update(loaded)
			m = next.(model)
			next, _ = m.Update(zoxideEnrichmentCmd(loaded.info, m.loadGeneration)())
			m = next.(model)
			m.restoreSelected(dirs[0])
			if selected, _ := m.selected(); selected != dirs[0] {
				t.Fatalf("initial directory missing: %q", selected)
			}
			if err := tmuxRun("new-session", "-d", "-s", "zz_new"); err != nil {
				t.Fatal(err)
			}
			writeZoxide(false)
			refreshed := watchSourceRefreshCmd(src, m.loadGeneration, "", m.sessionInfo, "changed")().(itemsMsg)
			if slices.Contains(refreshed.items, dirs[0]) {
				t.Fatal("fast loader unexpectedly included delayed directory")
			}
			next, _ = m.Update(refreshed)
			m = next.(model)
			assertSelected := func() {
				t.Helper()
				if selected, _ := m.selected(); selected != dirs[0] {
					t.Fatalf("refresh moved selected directory to %q", selected)
				}
				chosen, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				if chosen.(model).result != dirs[0] {
					t.Fatal("Enter connected to a different target")
				}
			}
			assertSelected()
			next, _ = m.Update(annotMsg{generation: m.loadGeneration, entries: m.items, values: map[string]string{dirs[1]: "main"}})
			m = next.(model)
			assertSelected()
			// Optional enrichment failures keep the selected row available.
			if err := os.WriteFile(zoxide, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
				t.Fatal(err)
			}
			failed, _ := m.Update(zoxideEnrichmentCmd(refreshed.info, m.loadGeneration)())
			m = failed.(model)
			assertSelected()
			writeZoxide(false)
			assertSelected()
			next, _ = m.Update(zoxideEnrichmentCmd(refreshed.info, m.loadGeneration)())
			m = next.(model)
			assertSelected()
			if slices.Contains(m.items, dirs[2]) || !slices.Contains(m.items, "zz_new") {
				t.Fatalf("refresh failed to reconcile directory/session rows: %v", m.items)
			}
		})
	}
}

func TestWatchRefreshPreservesPaneIdentityAndBoundsReloads(t *testing.T) {
	withIsolatedEnv(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	m := newModel(false, false, false)
	m.src, m.mode, m.loading = srcTmux, modeList, false
	m.watchOpt.Poll = 5 * time.Second
	old := "old title\twork\t%2\t/work"
	other := "other\twork\t%1\t/work"
	m.items = []string{old, other}
	m.refilter()
	m.restoreSelected(old)
	updated := "new title\twork\t%2\t/work"
	next, _ := m.Update(itemsMsg{src: srcTmux, generation: m.loadGeneration, items: []string{other, updated}, backgroundTopology: "new title"})
	m = next.(model)
	if selected, _ := m.selected(); selected != updated {
		t.Fatalf("changed title moved selection: %q", selected)
	}
	chosen, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if chosen.(model).result != "work" || chosen.(model).resultPaneID != "%2" {
		t.Fatal("Enter targeted another pane")
	}
	next, _ = m.Update(watchMsg{info: waitingInfo{sourceTopologies: map[sourceKind]string{srcTmux: "another title"}}})
	if next.(model).watchSourceLoading {
		t.Fatal("rapid title updates caused another reload before poll interval")
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
	if !getTmuxCapabilities(getTmuxServer()).EventMonitors || watchOwnerScope() == "" {
		t.Skip("requires native monitors and local owner scope")
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
		return watchMonitorName(pid, start, strings.Repeat("a", 32), strings.Repeat("b", 32))
	}
	dead, live := name(child.Process.Pid, start), name(os.Getpid(), selfStart)
	reused := name(os.Getpid(), selfStart+"1")
	legacy := "@tmux_qs_watch_other_instance"
	foreign := strings.Replace(dead, watchOwnerScope(), strings.Repeat("f", 64), 1)
	custom := dead + "_custom"
	for _, hook := range []string{dead, live, reused, legacy, custom, foreign} {
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
	for _, hook := range []string{live, legacy, custom, foreign} {
		if !strings.Contains(hooks, hook+"::") {
			t.Fatalf("removed another owner/custom hook: %s", hooks)
		}
	}
}
