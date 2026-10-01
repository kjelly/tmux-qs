package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseTmuxVersionSuffix(t *testing.T) {
	cases := []struct {
		raw        string
		wantMaj    int
		wantMin    int
		wantSuffix string
	}{
		{"3.7c", 3, 7, "c"},
		{"3.7", 3, 7, ""},
		{"3.6a", 3, 6, "a"},
		{"3.2-rc3", 3, 2, "-rc3"},
		{"master", 0, 0, ""},
		{"", 0, 0, ""},
	}
	for _, tc := range cases {
		v := parseTmuxVersion(tc.raw)
		if v.Major != tc.wantMaj || v.Minor != tc.wantMin || v.Suffix != tc.wantSuffix || v.Raw != tc.raw {
			t.Errorf("parseTmuxVersion(%q) = %+v, want Maj=%d Min=%d Suffix=%q Raw=%q",
				tc.raw, v, tc.wantMaj, tc.wantMin, tc.wantSuffix, tc.raw)
		}
	}
}

type fakeCapabilityRunner struct {
	linesFunc func(server tmuxServerSpec, args ...string) ([]string, error)
	outFunc   func(server tmuxServerSpec, args ...string) (string, error)
}

func (f fakeCapabilityRunner) runLines(server tmuxServerSpec, args ...string) ([]string, error) {
	if f.linesFunc != nil {
		return f.linesFunc(server, args...)
	}
	return nil, nil
}

func (f fakeCapabilityRunner) runOut(server tmuxServerSpec, args ...string) (string, error) {
	if f.outFunc != nil {
		return f.outFunc(server, args...)
	}
	return "", nil
}

func TestCapabilitiesParseTmux37Commands(t *testing.T) {
	resetCapabilityCacheForTest()
	runner := fakeCapabilityRunner{
		linesFunc: func(server tmuxServerSpec, args ...string) ([]string, error) {
			cmd := args[0]
			if cmd == "list-panes" {
				return []string{"%1\t3.7c\thidden"}, nil
			}
			if cmd == "list-commands" {
				return []string{
					"new-pane (newp) [-bdefhIklPvZ] [-c start-directory] ...",
					"refresh-client (refresh) [-cDlLRSU] [-A pane:state] ...",
				}, nil
			}
			return nil, nil
		},
		outFunc: func(server tmuxServerSpec, args ...string) (string, error) {
			if args[0] == "show-options" {
				return "buffer\n", nil
			}
			return "", nil
		},
	}
	server := tmuxServerSpec{flag: "-L", value: "test-37"}
	caps := getTmuxCapabilitiesWithRunner(server, runner)
	if caps.Version.Raw != "3.7c" || caps.Version.Major != 3 || caps.Version.Minor != 7 || caps.Version.Suffix != "c" {
		t.Errorf("unexpected version: %+v", caps.Version)
	}
	if !caps.ProgressBar {
		t.Errorf("expected ProgressBar == true")
	}
	if !caps.ClipboardQuery {
		t.Errorf("expected ClipboardQuery == true")
	}
	if !caps.GetClipboardOption || caps.GetClipboardMode != "buffer" {
		t.Errorf("expected GetClipboardOption == true and mode == buffer, got %v, %q", caps.GetClipboardOption, caps.GetClipboardMode)
	}
	if !caps.FloatingPane {
		t.Errorf("expected FloatingPane == true")
	}
}

func TestCapabilitiesUnknownFormatIsUnsupported(t *testing.T) {
	resetCapabilityCacheForTest()
	runner := fakeCapabilityRunner{
		linesFunc: func(server tmuxServerSpec, args ...string) ([]string, error) {
			cmd := args[0]
			if cmd == "list-panes" {
				// Tmux 3.6 format with empty progress field
				return []string{"%1\t3.6\t"}, nil
			}
			if cmd == "list-commands" {
				return []string{
					"split-window (split) ...",
					"refresh-client (refresh) [-CDlRSU] ...",
				}, nil
			}
			return nil, nil
		},
		outFunc: func(server tmuxServerSpec, args ...string) (string, error) {
			return "", errors.New("unknown option: get-clipboard")
		},
	}
	server := tmuxServerSpec{flag: "-L", value: "test-36"}
	caps := getTmuxCapabilitiesWithRunner(server, runner)
	if caps.ProgressBar {
		t.Errorf("expected ProgressBar == false for unknown format")
	}
	if caps.FloatingPane {
		t.Errorf("expected FloatingPane == false when new-pane absent")
	}
	if caps.GetClipboardOption {
		t.Errorf("expected GetClipboardOption == false")
	}
}

func TestCapabilitiesGetClipboardOffStillSupported(t *testing.T) {
	resetCapabilityCacheForTest()
	runner := fakeCapabilityRunner{
		linesFunc: func(server tmuxServerSpec, args ...string) ([]string, error) {
			if args[0] == "list-panes" {
				return []string{"%1\t3.7\tnormal"}, nil
			}
			if args[0] == "list-commands" {
				return []string{"new-pane [-l]"}, nil
			}
			return nil, nil
		},
		outFunc: func(server tmuxServerSpec, args ...string) (string, error) {
			return "off\n", nil
		},
	}
	server := tmuxServerSpec{flag: "-L", value: "test-clip-off"}
	caps := getTmuxCapabilitiesWithRunner(server, runner)
	if !caps.GetClipboardOption {
		t.Errorf("expected GetClipboardOption == true even when mode is off")
	}
	if caps.GetClipboardMode != "off" {
		t.Errorf("expected GetClipboardMode == off, got %q", caps.GetClipboardMode)
	}
}

func TestCapabilitiesAreCachedPerServer(t *testing.T) {
	resetCapabilityCacheForTest()
	server1Calls := 0
	server2Calls := 0
	runner := fakeCapabilityRunner{
		linesFunc: func(server tmuxServerSpec, args ...string) ([]string, error) {
			if server.value == "srv1" {
				server1Calls++
				if args[0] == "list-panes" {
					return []string{"%1\t3.7\thidden"}, nil
				}
				return []string{"new-pane"}, nil
			}
			server2Calls++
			if args[0] == "list-panes" {
				return []string{"%1\t3.6\t"}, nil
			}
			return []string{"split-window"}, nil
		},
	}
	s1 := tmuxServerSpec{flag: "-L", value: "srv1"}
	s2 := tmuxServerSpec{flag: "-L", value: "srv2"}

	c1 := getTmuxCapabilitiesWithRunner(s1, runner)
	c2 := getTmuxCapabilitiesWithRunner(s2, runner)

	// Call again, should be cached
	c1b := getTmuxCapabilitiesWithRunner(s1, runner)
	c2b := getTmuxCapabilitiesWithRunner(s2, runner)

	if c1.FloatingPane != true || c1b.FloatingPane != true {
		t.Errorf("srv1 should have FloatingPane true")
	}
	if c2.FloatingPane != false || c2b.FloatingPane != false {
		t.Errorf("srv2 should have FloatingPane false")
	}
	if server1Calls != 2 { // 1 list-panes + 1 list-commands
		t.Errorf("expected srv1 to be probed once (2 command calls), got %d", server1Calls)
	}
	if server2Calls != 2 {
		t.Errorf("expected srv2 to be probed once (2 command calls), got %d", server2Calls)
	}
}

func TestCapabilitiesConcurrentProbeRunsOnce(t *testing.T) {
	resetCapabilityCacheForTest()
	var runs int64
	runner := fakeCapabilityRunner{
		linesFunc: func(server tmuxServerSpec, args ...string) ([]string, error) {
			if args[0] == "list-panes" {
				atomic.AddInt64(&runs, 1)
				time.Sleep(30 * time.Millisecond)
				return []string{"%1\t3.7c\thidden"}, nil
			}
			return []string{"new-pane"}, nil
		},
	}
	server := tmuxServerSpec{flag: "-L", value: "concurrent-srv"}
	const count = 10
	var wg sync.WaitGroup
	wg.Add(count)
	results := make([]TmuxCapabilities, count)
	for i := 0; i < count; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = getTmuxCapabilitiesWithRunner(server, runner)
		}(i)
	}
	wg.Wait()

	if r := atomic.LoadInt64(&runs); r != 1 {
		t.Errorf("expected probe to run exactly once, ran %d times", r)
	}
	for i, r := range results {
		if !r.ProgressBar || !r.FloatingPane {
			t.Errorf("result %d missing capabilities: %+v", i, r)
		}
	}
}

func TestCapabilitiesFailureIsNonFatalAndRetryable(t *testing.T) {
	resetCapabilityCacheForTest()
	var failCount int
	runner := fakeCapabilityRunner{
		linesFunc: func(server tmuxServerSpec, args ...string) ([]string, error) {
			if failCount == 0 {
				failCount++
				return nil, errors.New("socket connection error")
			}
			if args[0] == "list-panes" {
				return []string{"%1\t3.7c\thidden"}, nil
			}
			return []string{"new-pane"}, nil
		},
	}
	server := tmuxServerSpec{flag: "-L", value: "retry-srv"}

	// First call fails
	caps1 := getTmuxCapabilitiesWithRunner(server, runner)
	if caps1.FloatingPane || caps1.ProgressBar {
		t.Errorf("failed probe should return empty/conservative caps, got %+v", caps1)
	}

	// Second call should retry and succeed
	caps2 := getTmuxCapabilitiesWithRunner(server, runner)
	if !caps2.FloatingPane || !caps2.ProgressBar {
		t.Errorf("retry should succeed and return valid caps, got %+v", caps2)
	}
}

func TestCapabilitiesFeatureNegativeIsCached(t *testing.T) {
	resetCapabilityCacheForTest()
	var optCalls int
	runner := fakeCapabilityRunner{
		linesFunc: func(server tmuxServerSpec, args ...string) ([]string, error) {
			if args[0] == "list-panes" {
				return []string{"%1\t3.7c\thidden"}, nil
			}
			return []string{"split-window"}, nil // no new-pane
		},
		outFunc: func(server tmuxServerSpec, args ...string) (string, error) {
			optCalls++
			return "", errors.New("unknown option: get-clipboard")
		},
	}
	server := tmuxServerSpec{flag: "-L", value: "neg-srv"}

	caps1 := getTmuxCapabilitiesWithRunner(server, runner)
	if caps1.FloatingPane || caps1.GetClipboardOption {
		t.Errorf("expected negative features")
	}

	caps2 := getTmuxCapabilitiesWithRunner(server, runner)
	if caps2.FloatingPane || caps2.GetClipboardOption {
		t.Errorf("expected negative features from cache")
	}
	if optCalls != 1 {
		t.Errorf("expected show-options to be called only once, got %d", optCalls)
	}
}

func TestCapabilitiesRealTmux37(t *testing.T) {
	withTestTmuxServer(t)
	resetCapabilityCacheForTest()
	caps := getTmuxCapabilities(getTmuxServer())
	if caps.Version.Raw != "3.7c" {
		t.Logf("tested against tmux %s", caps.Version.Raw)
		return
	}
	if !caps.ProgressBar {
		t.Errorf("expected ProgressBar == true on 3.7c")
	}
	if !caps.ClipboardQuery {
		t.Errorf("expected ClipboardQuery == true on 3.7c")
	}
	if !caps.GetClipboardOption {
		t.Errorf("expected GetClipboardOption == true on 3.7c")
	}
	if !caps.FloatingPane {
		t.Errorf("expected FloatingPane == true on 3.7c")
	}
}
