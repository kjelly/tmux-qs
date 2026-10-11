package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestResurrectJSONLayoutExcludesPicker(t *testing.T) {
	raw := `{"V":2,"L":{"t":"v","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":80,"h":24,"x":0,"y":0,"i":0,"I":"%1"},{"t":"p","w":30,"h":10,"x":5,"y":5,"i":1,"z":0,"a":true,"I":"%2"}]}}`
	filtered, err := filterResurrectLayout(raw, []ResurrectPane{{ID: "%1"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(filtered, "%2") {
		t.Fatal("picker ID retained")
	}
	var layout resurrectLayout
	if err := json.Unmarshal([]byte(filtered), &layout); err != nil {
		t.Fatal(err)
	}
	if layout.Root.Type != "p" || !strings.Contains(filtered, `"i":0`) {
		t.Fatalf("bad collapsed layout %s", filtered)
	}
	if _, err := filterResurrectLayout(`{"V":99}`, nil); err == nil {
		t.Fatal("unsupported version accepted")
	}
}

func TestResurrectPaneMetadataRoundTrip(t *testing.T) {
	index := 7
	state := ResurrectPane{ID: "%45", Index: &index, Path: "/tmp/a", Command: "nvim", Floating: true, Width: 30, Height: 10, X: -2, Y: 3}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var got ResurrectPane
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("%+v != %+v", got, state)
	}
}

func TestResurrectRollbackOnCreationOrCommandFailure(t *testing.T) {
	for _, stage := range []string{"split-window", "new-window", "select-layout", "send-keys", "kill-session"} {
		t.Run(stage, func(t *testing.T) {
			caps := TmuxCapabilities{JSONLayout: true, FloatingPane: true}
			restore := setCapabilitiesForTest(getTmuxServer(), caps)
			defer restore()
			w := ResurrectWindow{Index: 0, Name: "main", Layout: "saved", Panes: []ResurrectPane{{Path: "/a", Command: "nvim"}, {Path: "/b"}}}
			s := ResurrectSession{Name: "restore-owned", Windows: []ResurrectWindow{w, {Index: 2, Name: "other"}}}
			rollback := false
			r := fakeCapabilityRunner{outFunc: func(_ tmuxServerSpec, args ...string) (string, error) {
				if args[0] == "kill-session" {
					rollback = true
					if args[2] != "=restore-owned" {
						t.Fatalf("wrong cleanup %v", args)
					}
					if stage == "kill-session" {
						return "", errors.New("cleanup failed")
					}
					return "", nil
				}
				if args[0] == stage || (stage == "kill-session" && args[0] == "split-window") {
					return "", errors.New("injected failure")
				}
				switch args[0] {
				case "new-session":
					return "@1\t%50\t0", nil
				case "new-window":
					return "@2\t%80\t2", nil
				case "split-window":
					return "%70", nil
				}
				return "", nil
			}}
			err := restoreSessionWithRunner(s, map[string]bool{"nvim": true}, getTmuxServer(), r)
			if err == nil || !rollback {
				t.Fatalf("expected error/rollback got %v cleanup=%v", err, rollback)
			}
		})
	}
}

func TestResurrectUsesCreatedIDsForCommands(t *testing.T) {
	restore := setCapabilitiesForTest(getTmuxServer(), TmuxCapabilities{})
	defer restore()
	var sent []string
	r := fakeCapabilityRunner{outFunc: func(_ tmuxServerSpec, args ...string) (string, error) {
		switch args[0] {
		case "new-session":
			return "@8\t%70\t0", nil
		case "split-window":
			return "%12", nil
		case "send-keys":
			if slices.Contains(args, "-l") {
				sent = append(sent, args[2]+":"+args[len(args)-1])
			}
		}
		return "", nil
	}}
	s := ResurrectSession{Name: "ids", Windows: []ResurrectWindow{{Name: "main", Panes: []ResurrectPane{{Command: "nvim"}, {Command: "htop"}, {Command: "unapproved"}}}}}
	if err := restoreSessionWithRunner(s, map[string]bool{"nvim": true, "htop": true}, getTmuxServer(), r); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sent, []string{"%70:nvim", "%12:htop"}) {
		t.Fatalf("command mapping %v", sent)
	}
}

func TestResurrectNativeMixedLayoutRoundTrip(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	if !caps.FloatingPane {
		t.Skip("requires floating panes")
	}
	if err := tmuxRun("set-option", "-g", "default-shell", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	_ = tmuxRun("set-option", "-g", "base-index", "3")
	_ = tmuxRun("set-option", "-g", "pane-base-index", "7")
	paths := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	name := "qs-mixed-save"
	first, err := tmuxRunOut("new-session", "-d", "-s", name, "-c", paths[0], "-P", "-F", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	second, err := tmuxRunOut("split-window", "-d", "-t", first, "-c", paths[1], "-P", "-F", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	_ = second
	floating, err := tmuxRunOut("new-pane", "-d", "-t", first, "-c", paths[2], "-x", "30", "-y", "10", "-X", "6", "-Y", "4", "-P", "-F", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	_ = floating
	// The temporary picker shares the layout but must be excluded.
	picker, err := tmuxRunOut("new-pane", "-d", "-t", first, "-x", "25", "-y", "8", "-P", "-F", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	if err := tmuxRun("set-option", "-p", "-t", picker, "@tmux_qs", "1"); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := captureResurrectSession(name)
	if !ok {
		t.Fatal("capture failed")
	}
	w := snapshot.Windows[0]
	if len(w.Panes) != 3 || !w.Panes[2].Floating || strings.Contains(w.Layout, `"I":"`+picker+`"`) {
		t.Fatalf("bad snapshot %+v", w)
	}
	// Each allowed command leaves a distinct marker in its own cwd.
	for i := range w.Panes {
		cmd := fmt.Sprintf("touch marker%d", i)
		w.Panes[i].Command = cmd
	}
	snapshot.Windows[0] = w
	allowed := map[string]bool{}
	for _, p := range w.Panes {
		allowed[p.Command] = true
	}
	if err := tmuxRun("kill-session", "-t", name); err != nil {
		t.Fatal(err)
	}
	if err := restoreSessionDetailedOn(snapshot, allowed, getTmuxServer()); err != nil {
		t.Fatal(err)
	}
	restored, ok := captureResurrectSession(name)
	if !ok {
		t.Fatal("restored capture failed")
	}
	got := restored.Windows[0]
	if got.Index != w.Index || len(got.Panes) != 3 {
		t.Fatalf("wrong window/pane count %+v", got)
	}
	for i, p := range got.Panes {
		old := w.Panes[i]
		if p.Path != old.Path || (caps.JSONLayout && (p.Floating != old.Floating || p.Width != old.Width || p.Height != old.Height || p.X != old.X || p.Y != old.Y)) {
			t.Fatalf("pane %d geometry/path got %+v want %+v", i, p, old)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for i, path := range paths {
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(path, fmt.Sprintf("marker%d", i))); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := os.Stat(filepath.Join(path, fmt.Sprintf("marker%d", i))); err != nil {
			t.Fatalf("pane %d command cwd: %v", i, err)
		}
	}
}

func TestResurrectLegacyLayoutOnCurrentTmux(t *testing.T) {
	withTestTmuxServer(t)
	_ = tmuxRun("set-option", "-g", "default-shell", "/bin/sh")
	first, err := tmuxRunOut("new-session", "-d", "-s", "legacy", "-P", "-F", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	if err := tmuxRun("split-window", "-d", "-t", first); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := captureResurrectSession("legacy")
	if !ok {
		t.Fatal("capture failed")
	}
	if getTmuxCapabilities(getTmuxServer()).JSONLayout {
		// select-layout accepts the historical checksum format in 3.8.
		// Historical two-row 80x24 layout with a verified checksum.
		body := "80x24,0,0[80x11,0,0,0,80x12,0,12,1]"
		var sum uint16
		for _, b := range []byte(body) {
			sum = (sum >> 1) | (sum << 15)
			sum += uint16(b)
		}
		snapshot.Windows[0].Layout = fmt.Sprintf("%04x,%s", sum, body)
	}
	_ = tmuxRun("kill-session", "-t", "legacy")
	if err := restoreSessionDetailedOn(snapshot, nil, getTmuxServer()); err != nil {
		t.Fatal(err)
	}
}

func TestResurrectInterleavedFloatingPanes(t *testing.T) {
	for _, beforeFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("before-first=", beforeFirst), func(t *testing.T) {
			withTestTmuxServer(t)
			if !getTmuxCapabilities(getTmuxServer()).JSONLayout {
				t.Skip("requires JSON layout")
			}
			_ = tmuxRun("set-option", "-g", "default-shell", "/bin/sh")
			_ = tmuxRun("set-option", "-g", "pane-base-index", "7")
			a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
			first, err := tmuxRunOut("new-session", "-d", "-s", "interleaved", "-c", a, "-P", "-F", "#{pane_id}")
			if err != nil {
				t.Fatal(err)
			}
			second, err := tmuxRunOut("split-window", "-d", "-t", first, "-c", b, "-P", "-F", "#{pane_id}")
			if err != nil {
				t.Fatal(err)
			}
			target := second
			if beforeFirst {
				target = first
			}
			_, err = tmuxRunOut("new-pane", "-b", "-d", "-t", target, "-c", c, "-x", "30", "-y", "10", "-X", "6", "-Y", "4", "-P", "-F", "#{pane_id}")
			if err != nil {
				t.Fatal(err)
			}
			snap, ok := captureResurrectSession("interleaved")
			if !ok {
				t.Fatal("capture failed")
			}
			floatIndex := 1
			if beforeFirst {
				floatIndex = 0
			}
			if !snap.Windows[0].Panes[floatIndex].Floating {
				t.Fatalf("fixture is not interleaved: %+v", snap)
			}
			allowed := map[string]bool{}
			original := map[string]ResurrectPane{}
			for i := range snap.Windows[0].Panes {
				p := &snap.Windows[0].Panes[i]
				p.Command = fmt.Sprintf("touch marker%d", i)
				allowed[p.Command] = true
				original[p.Path] = *p
			}
			if err := tmuxRun("kill-session", "-t", "interleaved"); err != nil {
				t.Fatal(err)
			}
			if err := restoreSessionDetailedOn(snap, allowed, getTmuxServer()); err != nil {
				t.Fatal(err)
			}
			restored, ok := captureResurrectSession("interleaved")
			if !ok || len(restored.Windows[0].Panes) != 3 {
				t.Fatal("restore lost panes")
			}
			for _, p := range restored.Windows[0].Panes {
				old, ok := original[p.Path]
				if !ok || p.Floating != old.Floating || p.Width != old.Width || p.Height != old.Height || p.X != old.X || p.Y != old.Y {
					t.Fatalf("wrong pane/geometry mapping: got %+v want %+v", p, old)
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			for _, p := range snap.Windows[0].Panes {
				marker := filepath.Join(p.Path, strings.TrimPrefix(p.Command, "touch "))
				for time.Now().Before(deadline) {
					if _, err := os.Stat(marker); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("wrong command cwd %s: %v", marker, err)
				}
			}
		})
	}
}

func TestNormalizeLegacyFloatingLayoutFourFieldZList(t *testing.T) {
	raw := "0000,80x24,0,0{40x24,0,0,1,39x24,41,0,2,28x8,7,5,3,23x6,2,2,4}<28x8,7,5,3,23x6,2,2,4>"
	indices := []int{7, 8, 9}
	panes := []ResurrectPane{{ID: "%1", Index: &indices[0]}, {ID: "%2", Index: &indices[1]}, {ID: "%3", Index: &indices[2], Floating: true, Width: 28, Height: 8, X: 7, Y: 5}}
	got, err := normalizeLegacyFloatingLayout(raw, panes)
	if err != nil {
		t.Fatal(err)
	}
	var layout resurrectLayout
	if err := json.Unmarshal([]byte(got), &layout); err != nil {
		t.Fatalf("not converted: %s", got)
	}
	leaves := resurrectLeaves(layout.Root)
	if len(leaves) != 3 {
		t.Fatalf("wrong leaves: %s", got)
	}
	for _, p := range leaves {
		if p.ID == "%4" {
			t.Fatal("internal picker retained")
		}
		if p.ID == "%3" && (p.Z == nil || *p.Z != 0 || p.Index != 9 || p.Width != 28) {
			t.Fatalf("bad float: %+v", p)
		}
		if (p.ID == "%1" || p.ID == "%2") && p.Z != nil {
			t.Fatal("tile changed type")
		}
	}
	tiled, err := normalizeLegacyFloatingLayout(raw, panes[:2])
	_, body, _ := strings.Cut(tiled, ",")
	if err != nil || body != "80x24,0,0{40x24,0,0,1,39x24,41,0,2}" {
		t.Fatalf("picker-only layout not pruned: %s %v", tiled, err)
	}
}

func TestResurrectLegacyFloatingSnapshotWithoutMetadata(t *testing.T) {
	withTestTmuxServer(t)
	_ = tmuxRun("set-option", "-g", "default-shell", "/bin/sh")
	panes := []ResurrectPane{{Path: t.TempDir()}, {Path: t.TempDir()}}
	s := ResurrectSession{Name: "old-floating", Windows: []ResurrectWindow{{Name: "main", Layout: "0000,80x24,0,0{80x24,0,0,1,28x8,7,5,2,23x6,2,2,3}<28x8,7,5,2,23x6,2,2,3>", Panes: panes}}}
	if err := restoreSessionDetailedOn(s, nil, getTmuxServer()); err != nil {
		t.Fatal(err)
	}
	got, ok := captureResurrectSession(s.Name)
	if !ok || len(got.Windows[0].Panes) != 2 {
		t.Fatal("old snapshot did not restore")
	}
	for i, p := range got.Windows[0].Panes {
		if p.Floating || p.Path != panes[i].Path {
			t.Fatalf("legacy pane: %+v", p)
		}
	}
}

func TestWorkspaceAndUndoRestoreContinueAfterFailure(t *testing.T) {
	withTestTmuxServer(t)
	withIsolatedEnv(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	bad := ResurrectSession{Name: "broken", Windows: []ResurrectWindow{{Name: "bad", Layout: `{"V":99}`}}}
	good := ResurrectSession{Name: "good", Path: t.TempDir()}
	data, err := json.Marshal(ResurrectState{Sessions: []ResurrectSession{bad, good}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFilePath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := resurrectRestore(); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("missing aggregated error: %v", err)
	}
	if _, err := tmuxRunOut("list-panes", "-t", "=good"); err != nil {
		t.Fatal("later healthy session not restored")
	}
	good.Name = "undo-good"
	msg := undoKillCmd([]serverResurrectSnapshot{{session: bad, server: getTmuxServer()}, {session: good, server: getTmuxServer()}})()
	if _, ok := msg.(uiErrMsg); !ok {
		t.Fatalf("missing undo error: %#v", msg)
	}
	if _, err := tmuxRunOut("list-panes", "-t", "=undo-good"); err != nil {
		t.Fatal("later undo session not restored")
	}
	m := newModel()
	if err := m.captureForUndo([]string{"missing-session"}); err == nil {
		t.Fatal("snapshot failure not reported")
	}
}

func TestKillAbortsWhenUndoSnapshotCannotBeCaptured(t *testing.T) {
	withTestTmuxServer(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	pane, err := tmuxRunOut("display-message", "-p", "-t", "qs-test-root", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	if err := tmuxRun("set-option", "-p", "-t", pane, "@tmux_qs", "1"); err != nil {
		t.Fatal(err)
	}
	m := newModel()
	m.src, m.mode = srcTmux, modeList
	m.items = []string{"qs-test-root"}
	m.refilter()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if !strings.Contains(next.(model).errText, "session was not killed") {
		t.Fatalf("capture failure not shown: %q", next.(model).errText)
	}
	if _, err := tmuxRunOut("list-panes", "-t", "=qs-test-root"); err != nil {
		t.Fatal("session killed without undo snapshot")
	}
}

func TestResurrectUnsupportedJSONRestoresPathsAndCommandsAsTiled(t *testing.T) {
	restore := setCapabilitiesForTest(getTmuxServer(), TmuxCapabilities{})
	defer restore()
	raw := `{"V":2,"L":{"t":"v","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":80,"h":24,"x":0,"y":0,"i":1,"I":"%1"},{"t":"p","w":30,"h":10,"x":5,"y":5,"i":0,"z":0,"I":"%2"}]}}`
	w := ResurrectWindow{Name: "first", Layout: raw, Panes: []ResurrectPane{{Path: "/a", Command: "first"}, {Path: "/b", Command: "second"}}}
	var paths, commands []string
	notices := 0
	runner := fakeCapabilityRunner{outFunc: func(_ tmuxServerSpec, args ...string) (string, error) {
		switch args[0] {
		case "new-session":
			return "@1\t%50\t0", nil
		case "new-window":
			return "@2\t%60\t1", nil
		case "split-window":
			paths = append(paths, args[5])
			return "%70", nil
		case "display-message":
			if args[1] != "-p" {
				notices++
			}
		case "send-keys":
			if args[3] == "-l" {
				commands = append(commands, args[2]+":"+args[4])
			}
		case "new-pane", "kill-session":
			t.Fatalf("fallback should retain tiled panes: %v", args)
		case "select-layout":
			if args[3] != "tiled" {
				t.Fatalf("unsupported layout applied: %v", args)
			}
		}
		return "", nil
	}}
	other := w
	other.Index, other.Name = 1, "second"
	err := restoreSessionWithRunner(ResurrectSession{Name: "fallback", Windows: []ResurrectWindow{w, other}}, map[string]bool{"first": true, "second": true}, getTmuxServer(), runner)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"/a", "/b"}) || !slices.Equal(commands, []string{"%70:first", "%50:second", "%60:first", "%70:second"}) || notices != 2 {
		t.Fatalf("fallback paths=%v commands=%v notices=%d", paths, commands, notices)
	}
}

func TestResurrectInvalidLegacyLayoutKeepsRebuiltPanes(t *testing.T) {
	restore := setCapabilitiesForTest(getTmuxServer(), TmuxCapabilities{})
	defer restore()
	var layouts []string
	sent, notice := false, false
	runner := fakeCapabilityRunner{outFunc: func(_ tmuxServerSpec, args ...string) (string, error) {
		switch args[0] {
		case "new-session":
			return "@1\t%50\t0", nil
		case "select-layout":
			layouts = append(layouts, args[3])
			if args[3] == "legacy-overlap" {
				return "size mismatch after applying layout", errors.New("invalid layout")
			}
		case "send-keys":
			sent = true
		case "display-message":
			notice = notice || args[1] != "-p"
		case "kill-session":
			t.Fatal("legacy layout failure destroyed the rebuilt session")
		}
		return "", nil
	}}
	w := ResurrectWindow{Name: "main", Layout: "legacy-overlap", Panes: []ResurrectPane{{Path: "/a", Command: "nvim"}}}
	if err := restoreSessionWithRunner(ResurrectSession{Name: "legacy", Windows: []ResurrectWindow{w}}, map[string]bool{"nvim": true}, getTmuxServer(), runner); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(layouts, []string{"legacy-overlap", "tiled"}) || !sent || !notice {
		t.Fatalf("layouts=%v sent=%v notice=%v", layouts, sent, notice)
	}
}

func TestKillUndoNativeFloatingAndZoomedSessions(t *testing.T) {
	for _, zoom := range []bool{false, true} {
		t.Run(fmt.Sprintf("zoom=%v", zoom), func(t *testing.T) {
			withTestTmuxServer(t)
			resetConfigCache()
			t.Cleanup(resetConfigCache)
			if !getTmuxCapabilities(getTmuxServer()).FloatingPane {
				t.Skip("requires floating panes")
			}
			_ = tmuxRun("set-option", "-g", "default-shell", "/bin/sh")
			paths := []string{t.TempDir(), t.TempDir(), t.TempDir()}
			first, err := tmuxRunOut("new-session", "-d", "-s", "undo-floating", "-c", paths[0], "-P", "-F", "#{pane_id}")
			if err != nil {
				t.Fatal(err)
			}
			if err := tmuxRun("split-window", "-d", "-t", first, "-c", paths[1]); err != nil {
				t.Fatal(err)
			}
			if err := tmuxRun("new-pane", "-d", "-t", first, "-c", paths[2], "-x", "30", "-y", "10", "-X", "6", "-Y", "4"); err != nil {
				t.Fatal(err)
			}
			if zoom {
				if err := tmuxRun("resize-pane", "-Z", "-t", first); err != nil {
					t.Fatal(err)
				}
			}
			m := newModel()
			m.src, m.mode = srcDefault, modeList
			m.items = []string{"undo-floating"}
			m.refilter()
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
			next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyCtrlD})
			m = next.(model)
			if len(m.killedUndo) != 1 {
				t.Fatalf("kill did not save undo: %s", m.errText)
			}
			if msg := undoKillCmd(m.killedUndo)(); msg != nil {
				t.Fatalf("undo failed: %+v", msg)
			}
			restored, ok := captureResurrectSession("undo-floating")
			if !ok || len(restored.Windows) != 1 || len(restored.Windows[0].Panes) != 3 {
				t.Fatalf("undo lost panes: %+v", restored)
			}
			for i, pane := range restored.Windows[0].Panes {
				if pane.Path != paths[i] {
					t.Fatalf("pane %d cwd=%s want=%s", i, pane.Path, paths[i])
				}
			}
		})
	}
}
