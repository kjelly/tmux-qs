package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/pelletier/go-toml/v2"
)

func TestModalPickerConfigurationAndFlags(t *testing.T) {
	var cfg Config
	if err := toml.Unmarshal([]byte("[ui]\nbackend = 'floating'\nfloating_modal = true\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if defaultConfig.UI.FloatingModal {
		t.Fatal("modal must be opt-in")
	}
	for _, modal := range []bool{false, true} {
		caps := TmuxCapabilities{FloatingPane: true, FloatingModal: modal}
		b := resolvePickerBackend(cfg, caps)
		if f, ok := b.(*floatingBackend); ok {
			if !modal || !f.modal {
				t.Fatalf("unexpected backend %+v", f)
			}
		} else if modal {
			t.Fatalf("modal supported but got %T", b)
		}
	}
	for _, usage := range []string{"new-pane [-AbCDefhIkKLMOPvWZ] [-B border-lines] [-T title]", "new-pane [-O]"} {
		if !commandSupportsFlag(usage, 'O') {
			t.Fatalf("missing O: %s", usage)
		}
	}
	if commandSupportsFlag("new-pane (O) [-x width] [OTHER]", 'O') {
		t.Fatal("matched usage words")
	}
	for _, tc := range []struct {
		caps  TmuxCapabilities
		modal bool
		want  []string
	}{
		{TmuxCapabilities{}, false, nil},
		{TmuxCapabilities{FloatingModal: true}, true, []string{"-O"}},
		{TmuxCapabilities{FloatingModal: true, FloatingTitle: true, FloatingBorder: true}, true, []string{"-O", "-T", "tmux-qs", "-B", "single"}},
		{TmuxCapabilities{FloatingOverZoom: true, FloatingTitle: true, FloatingBorder: true}, false, nil},
	} {
		got := floatingPresentationArgs(tc.caps, tc.modal)
		if !slices.Equal(got, tc.want) {
			t.Fatalf("got %v want %v", got, tc.want)
		}
		for _, flag := range []string{"-D", "-K", "-C"} {
			if slices.Contains(got, flag) {
				t.Fatalf("unsafe flag %s", flag)
			}
		}
	}
}

func TestModalCapabilitiesProbeFlagsIndependently(t *testing.T) {
	for _, usage := range []string{"new-pane [-P]", "new-pane [-O] [-T title]", "new-pane [-A] [-B border-lines]"} {
		r := fakeCapabilityRunner{linesFunc: func(_ tmuxServerSpec, args ...string) ([]string, error) {
			if args[0] == "list-panes" {
				return []string{"%1\t3.8\thidden"}, nil
			}
			return []string{usage}, nil
		}}
		caps, err := probeServerCapabilitiesWithRunner(tmuxServerSpec{}, r)
		if err != nil {
			t.Fatal(err)
		}
		if caps.FloatingModal != strings.Contains(usage, "-O") || caps.FloatingOverZoom != strings.Contains(usage, "-A") || caps.FloatingTitle != strings.Contains(usage, "-T") || caps.FloatingBorder != strings.Contains(usage, "-B") {
			t.Fatalf("%s: %+v", usage, caps)
		}
	}
}

func TestModalOpenFailureCleanupAndConflict(t *testing.T) {
	restore := setCapabilitiesForTest(getTmuxServer(), TmuxCapabilities{FloatingPane: true, FloatingModal: true})
	defer restore()
	for _, stage := range []string{"new-pane", "@tmux_qs", "@tmux_qs_client", "@tmux_qs_instance", "remain-on-exit", "conflict", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			killed := false
			r := fakeCapabilityRunner{outFunc: func(_ tmuxServerSpec, args ...string) (string, error) {
				switch args[0] {
				case "display-message":
					if args[len(args)-1] == "#{pane_id}" {
						if stage == "cleanup" {
							return "%42", nil
						}
						return "", errors.New("no pane")
					}
					return "100\t30", nil
				case "new-pane":
					if !slices.Contains(args, "-O") || slices.Contains(args, "-D") || slices.Contains(args, "-K") {
						t.Fatalf("bad flags %v", args)
					}
					if stage == "new-pane" {
						return "", errors.New("create failed")
					}
					if stage == "conflict" {
						return "", errors.New("window already has a modal pane")
					}
					return "%42", nil
				case "set-option":
					if args[4] == stage || stage == "cleanup" {
						return "", errors.New("metadata failed")
					}
				case "kill-pane":
					killed = true
				}
				return "", nil
			}}
			f := floatingBackend{modal: true, runner: r}
			err := f.Open(PickerOpenRequest{Context: popupContext{client: "A", pane: "%1", cwd: "/tmp"}, PopupSpec: "80%,80%"})
			if stage == "conflict" || stage == "cleanup" {
				if err == nil || errors.Is(err, errFallbackPopup) {
					t.Fatalf("expected fatal/conflict error got %v", err)
				}
			} else if !errors.Is(err, errFallbackPopup) {
				t.Fatalf("expected fallback got %v", err)
			}
			wantKill := stage != "new-pane" && stage != "conflict"
			if killed != wantKill {
				t.Fatalf("killed=%v want %v", killed, wantKill)
			}
		})
	}
}

func TestModalNativeZoomFocusAndClientOwnership(t *testing.T) {
	withTestTmuxServer(t)
	caps := getTmuxCapabilities(getTmuxServer())
	requireTmux38Capability(t, caps.FloatingModal, "modal floating panes")
	pane, err := tmuxRunOut("display-message", "-p", "-t", "qs-test-root", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	if err := tmuxRun("split-window", "-d", "-t", pane, "sleep", "30"); err != nil {
		t.Fatal(err)
	}
	if err := tmuxRun("resize-pane", "-Z", "-t", pane); err != nil {
		t.Fatal(err)
	}
	args := []string{"new-pane", "-t", pane, "-x", "40", "-y", "15", "-X", "5", "-Y", "5", "-P", "-F", "#{pane_id}"}
	args = append(args, floatingPresentationArgs(caps, true)...)
	args = append(args, "sleep", "30")
	modal, err := tmuxRunOut(args...)
	if err != nil {
		t.Fatalf("new-pane: %s: %v", modal, err)
	}
	for _, kv := range [][2]string{{"@tmux_qs", "1"}, {"@tmux_qs_client", "client-A"}, {"@tmux_qs_instance", "test"}} {
		if err := tmuxRun("set-option", "-p", "-t", modal, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := tmuxRunOut("display-message", "-p", "-t", modal, "#{pane_modal_flag}\t#{window_zoomed_flag}\t#{pane_active}")
	if state != "1\t1\t1" {
		t.Fatalf("modal zoom/active state %q", state)
	}
	if err := tmuxRun("select-pane", "-t", pane); err != nil {
		t.Fatal(err)
	}
	active, _ := tmuxRunOut("display-message", "-p", "-t", pane, "#{pane_active}")
	if active != "0" {
		t.Fatal("background pane became active")
	}
	other := floatingBackend{modal: true}
	err = other.Open(PickerOpenRequest{Context: popupContext{client: "client-B", pane: pane, cwd: "/tmp"}, PopupSpec: "80%,80%"})
	if err == nil || errors.Is(err, errFallbackPopup) {
		t.Fatalf("existing modal conflict=%v", err)
	}
	if !tmuxPaneExists(getTmuxServer(), modal) {
		t.Fatal("other client's modal was removed")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	result, err := other.Toggle(PickerToggleRequest{Context: popupContext{client: "client-A", pane: pane, cwd: "/tmp"}})
	if err != nil || !result.Closed {
		t.Fatalf("close=%+v err=%v", result, err)
	}
	state, _ = tmuxRunOut("display-message", "-p", "-t", pane, "#{pane_active}\t#{window_zoomed_flag}")
	if state != "1\t1" || tmuxPaneExists(getTmuxServer(), modal) {
		t.Fatalf("focus/zoom not restored %q", state)
	}
}

func TestSnippetRejectsLateSourceEnrichment(t *testing.T) {
	withIsolatedEnv(t)
	resetConfigCache()
	t.Cleanup(resetConfigCache)
	m := newModel()
	m.mode = modeSnippetSelect
	m.src = srcDefault
	m.items = []string{"Modal test"}
	m.savedItems = []string{"session"}
	m.refilter()
	next, _ := m.Update(sourceEnrichmentMsg{generation: m.loadGeneration, items: []string{"/late/path"}})
	if !slices.Equal(next.(model).items, m.items) {
		t.Fatal("late source results corrupted snippet rows")
	}
	got := next.(model)
	if !slices.Contains(got.savedItems, "/late/path") {
		t.Fatal("late source results lost from saved list")
	}
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(model).mode != modeList || !slices.Contains(next.(model).items, "/late/path") {
		t.Fatal("return to Sessions lost zoxide item")
	}
}

func TestModalRapidToggleDuringOpen(t *testing.T) {
	withTestTmuxServer(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restore := setCapabilitiesForTest(getTmuxServer(), TmuxCapabilities{FloatingPane: true, FloatingModal: true})
	defer restore()
	killed := false
	r := fakeCapabilityRunner{
		linesFunc: func(_ tmuxServerSpec, args ...string) ([]string, error) {
			if !slices.Contains(args, "%1") || slices.Contains(args, "-a") {
				t.Fatalf("close query not window scoped: %v", args)
			}
			if killed {
				return nil, nil
			}
			return nil, nil
		},
		outFunc: func(_ tmuxServerSpec, args ...string) (string, error) {
			switch args[0] {
			case "display-message":
				if args[len(args)-1] == "#{pane_id}" {
					return "", errors.New("no pane")
				}
				return "100\t30", nil
			case "new-pane":
				requestToggleClose("A")
				return "%42", nil
			case "kill-pane":
				killed = true
			}
			return "", nil
		},
	}
	// First listing is empty; the listing after open observes the new picker.
	listings := 0
	r.linesFunc = func(_ tmuxServerSpec, args ...string) ([]string, error) {
		if !slices.Contains(args, "%1") || slices.Contains(args, "-a") {
			t.Fatalf("not window scoped %v", args)
		}
		listings++
		if listings == 1 {
			return nil, nil
		}
		return []string{"%42\t1\t1\tA\tnonce"}, nil
	}
	f := floatingBackend{modal: true, runner: r}
	got, err := f.Toggle(PickerToggleRequest{Context: popupContext{client: "A", pane: "%1", cwd: "/tmp"}})
	if err != nil || !got.Closed || !killed {
		t.Fatalf("rapid close: %+v err=%v killed=%v", got, err, killed)
	}
}

func TestFloatingToggleScopesAndReportsCleanupFailure(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, modal := range []bool{false, true} {
		r := fakeCapabilityRunner{
			linesFunc: func(_ tmuxServerSpec, args ...string) ([]string, error) {
				if slices.Contains(args, "-a") == modal || slices.Contains(args, "-t") != modal {
					t.Fatalf("modal=%v wrong scope: %v", modal, args)
				}
				return []string{"%42\t1\t1\tA\tnonce"}, nil
			},
			outFunc: func(_ tmuxServerSpec, args ...string) (string, error) {
				if args[0] == "kill-pane" {
					return "", errors.New("kill failed")
				}
				return "%42", nil
			},
		}
		f := floatingBackend{modal: modal, runner: r}
		got, err := f.Toggle(PickerToggleRequest{Context: popupContext{client: "A", pane: "%1", cwd: "/tmp"}})
		if err == nil || !got.Consumed || got.Closed {
			t.Fatalf("cleanup failure hidden: %+v %v", got, err)
		}
	}
}
