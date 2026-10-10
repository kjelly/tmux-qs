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

func TestResurrectRollbackAndVersionLimits(t *testing.T) {
	for _, stage := range []string{"unsupported-json", "unsupported-float", "split-window", "new-window", "select-layout", "send-keys", "kill-session"} {
		t.Run(stage, func(t *testing.T) {
			caps := TmuxCapabilities{JSONLayout: true, FloatingPane: true}
			if stage == "unsupported-json" {
				caps.JSONLayout = false
			}
			if stage == "unsupported-float" {
				caps.FloatingPane = false
			}
			restore := setCapabilitiesForTest(getTmuxServer(), caps)
			defer restore()
			w := ResurrectWindow{Index: 0, Name: "main", Layout: "saved", Panes: []ResurrectPane{{Path: "/a", Command: "nvim"}, {Path: "/b"}}}
			if stage == "unsupported-json" {
				w.Layout = `{"V":2}`
			}
			if stage == "unsupported-float" {
				w.Panes[1].Floating = true
			}
			s := ResurrectSession{Name: "restore-owned", Windows: []ResurrectWindow{w, {Index: 2, Name: "other"}}}
			rollback := false
			sent := false
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
				case "send-keys":
					sent = true
				}
				return "", nil
			}}
			err := restoreSessionWithRunner(s, map[string]bool{"nvim": true}, getTmuxServer(), r)
			if err == nil || !rollback {
				t.Fatalf("expected error/rollback got %v cleanup=%v", err, rollback)
			}
			if strings.HasPrefix(stage, "unsupported") && sent {
				t.Fatal("unsupported layout launched commands")
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
	if !caps.JSONLayout {
		if err := tmuxRun("kill-session", "-t", name); err != nil {
			t.Fatal(err)
		}
		if err := restoreSessionDetailedOn(snapshot, nil, getTmuxServer()); err == nil {
			t.Fatal("3.7 should reject a mixed v2 snapshot")
		}
		return
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
		if p.Path != old.Path || p.Floating != old.Floating || p.Width != old.Width || p.Height != old.Height || p.X != old.X || p.Y != old.Y {
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
