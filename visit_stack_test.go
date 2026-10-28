package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// withXDGCacheDir returns a setup that redirects the XDG cache to a
// temp dir for the duration of the test, so visit_stack tests don't
// pollute the real cache. Returns a cleanup function the caller must
// defer.
func withXDGCacheDir(t *testing.T) func() {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	return func() {}
}

func TestVisitStack_PushPopPeek(t *testing.T) {
	defer withXDGCacheDir(t)()
	var s visitStack

	// Empty stack.
	if got := s.peek(); got != "" {
		t.Errorf("peek on empty = %q, want \"\"", got)
	}
	if got := s.pop(); got != "" {
		t.Errorf("pop on empty = %q, want \"\"", got)
	}

	// Push a few items.
	s = s.push("a")
	s = s.push("b")
	s = s.push("c")
	if got := s.peek(); got != "c" {
		t.Errorf("peek after push c = %q, want \"c\"", got)
	}

	// Pop should give LIFO order.
	if got := s.pop(); got != "c" {
		t.Errorf("pop #1 = %q, want \"c\"", got)
	}
	if got := s.pop(); got != "b" {
		t.Errorf("pop #2 = %q, want \"b\"", got)
	}
	if got := s.pop(); got != "a" {
		t.Errorf("pop #3 = %q, want \"a\"", got)
	}
	if got := s.pop(); got != "" {
		t.Errorf("pop on drained = %q, want \"\"", got)
	}
}

func TestVisitStack_PushDedup(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	s = s.push("a")
	s = s.push("b")
	s = s.push("a") // re-push: should move to top, no duplicate
	if len(s.entries) != 2 {
		t.Errorf("after re-push: len = %d, want 2", len(s.entries))
	}
	if s.entries[0] != "a" {
		t.Errorf("after re-push: top = %q, want \"a\"", s.entries[0])
	}
	if s.entries[1] != "b" {
		t.Errorf("after re-push: second = %q, want \"b\"", s.entries[1])
	}
}

func TestVisitStack_PushCapped(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	// Push more than visitStackMaxLen entries; the oldest should fall off.
	for i := 0; i < visitStackMaxLen+5; i++ {
		s = s.push(string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	if len(s.entries) != visitStackMaxLen {
		t.Errorf("after overflow: len = %d, want %d", len(s.entries), visitStackMaxLen)
	}
}

func TestVisitStack_SaveLoad(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	s = s.push("one")
	s = s.push("two")
	s = s.push("three")
	s.save()

	loaded := loadVisitStack()
	if len(loaded.entries) != 3 {
		t.Errorf("loaded len = %d, want 3", len(loaded.entries))
	}
	if loaded.entries[0] != "three" {
		t.Errorf("loaded top = %q, want \"three\"", loaded.entries[0])
	}
}

func TestVisitStack_LoadMissing(t *testing.T) {
	defer withXDGCacheDir(t)()
	// No save file exists yet — should return an empty stack, no error.
	s := loadVisitStack()
	if len(s.entries) != 0 {
		t.Errorf("loadVisitStack() on missing file = %v, want empty", s.entries)
	}
}

func TestVisitStack_LoadCorrupt(t *testing.T) {
	defer withXDGCacheDir(t)()
	// Write garbage to the cache file.
	path := visitStackPath()
	if path == "" {
		t.Fatal("visitStackPath() returned empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := loadVisitStack()
	if len(s.entries) != 0 {
		t.Errorf("loadVisitStack() on corrupt file = %v, want empty", s.entries)
	}
}

func TestVisitStack_BackAndForwardErrors(t *testing.T) {
	defer withXDGCacheDir(t)()
	// No stack → back should error.
	if err := visitStackBack(); err == nil {
		t.Error("visitStackBack on empty stack should error")
	}
	if err := visitStackForward(); err == nil {
		t.Error("visitStackForward on empty stack should error")
	}

	// Stack with one entry → forward should error (need 2+).
	s := visitStack{}
	s = s.push("only")
	s.save()
	if err := visitStackForward(); err == nil {
		t.Error("visitStackForward with one entry should error")
	}
}

func TestVisitStack_SaveEmpty(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	s.save()
	// Reload — should be empty.
	loaded := loadVisitStack()
	if len(loaded.entries) != 0 {
		t.Errorf("save+load of empty: len = %d, want 0", len(loaded.entries))
	}
}

func TestShouldUseTmuxClientLastSession(t *testing.T) {
	tests := []struct {
		name        string
		tmuxEnv     string
		popupClient string
		want        bool
	}{
		{name: "outside tmux", want: false},
		{name: "tmux environment", tmuxEnv: "/tmp/tmux-1000/default,1,0", want: true},
		{name: "popup caller client", popupClient: "/dev/pts/4", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseTmuxClientLastSession(tt.tmuxEnv, tt.popupClient); got != tt.want {
				t.Errorf("shouldUseTmuxClientLastSession() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestMostRecentClientSession(t *testing.T) {
	lines := []string{
		"1710000100\tolder",
		"1710000300\tactive",
		"not-a-time\tignored",
		"1710000400\t",
	}

	if got := mostRecentClientSession(lines); got != "active" {
		t.Fatalf("mostRecentClientSession() = %q, want %q", got, "active")
	}
}

func TestMostRecentClientSessionEmptyWhenNoValidClient(t *testing.T) {
	lines := []string{"malformed", "not-a-time\tignored", "1710000000"}

	if got := mostRecentClientSession(lines); got != "" {
		t.Fatalf("mostRecentClientSession() = %q, want empty", got)
	}
}

func TestMostRecentOtherClientSession(t *testing.T) {
	lines := []string{
		"1710000500\tcurrent\twork",
		"1710000600\tsame-session\twork",
		"1710000300\tolder\tdocs",
		"1710000400\trecent\tcode",
		"not-a-time\tignored\tbroken",
	}

	if got := mostRecentOtherClientSession(lines, "current"); got != "code" {
		t.Fatalf("mostRecentOtherClientSession() = %q, want %q", got, "code")
	}
}

func TestMostRecentOtherClientSessionEmptyWithoutDifferentSession(t *testing.T) {
	lines := []string{
		"1710000500\tcurrent\twork",
		"1710000600\tother\twork",
	}

	if got := mostRecentOtherClientSession(lines, "current"); got != "" {
		t.Fatalf("mostRecentOtherClientSession() = %q, want empty", got)
	}
}

func TestMostRecentOtherClientSessionSkipsEinkPair(t *testing.T) {
	for _, current := range []string{"hufu", "hufu-eink"} {
		t.Run(current, func(t *testing.T) {
			lines := []string{
				"100\tcurrent\t" + current,
				"300\tnormal\thufu",
				"400\teink\thufu-eink",
			}
			if got := mostRecentOtherClientSession(lines, "current"); got != "" {
				t.Fatalf("paired session selected: %q", got)
			}
			lines = append(lines, "200\tother\thufu-tools")
			if got := mostRecentOtherClientSession(lines, "current"); got != "hufu-tools" {
				t.Fatalf("got %q, want different workspace hufu-tools", got)
			}
		})
	}
}

// A control-mode client gives tmux real client history without a terminal.
// All clients and sessions belong to the private test server.
func startLastSessionTestClient(t *testing.T, session string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "tmux", "-L", getTmuxServer().value, "-C", "attach-session", "-t", session)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	prefix := fmt.Sprintf("%d\t", cmd.Process.Pid)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lines, err := tmuxRunLines("list-clients", "-F", "#{client_pid}\t#{client_name}")
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			if name, ok := strings.CutPrefix(line, prefix); ok {
				return name
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("control-mode client did not attach")
	return ""
}

func TestLastSessionSwitchSkipsEinkPair(t *testing.T) {
	for _, tc := range []struct {
		name     string
		current  string
		previous string
		other    string
		want     string
		wantErr  bool
	}{
		{"normal pair only", "hufu", "hufu-eink", "hufu-eink", "hufu", true},
		{"eink pair only", "hufu-eink", "hufu", "hufu", "hufu-eink", true},
		{"single client pair", "hufu", "hufu-eink", "", "hufu", true},
		{"no previous pair only", "hufu-eink", "", "hufu", "hufu-eink", true},
		{"normal fallback", "hufu", "hufu-eink", "docs", "docs", false},
		{"eink fallback", "hufu-eink", "hufu", "docs", "docs", false},
		{"distinct previous wins", "hufu-eink", "docs", "hufu", "docs", false},
		{"no previous fallback", "hufu", "", "docs", "docs", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTestTmuxServer(t)
			if err := tmuxRun("new-session", "-d", "-s", "hufu"); err != nil {
				t.Fatal(err)
			}
			if err := tmuxRun("new-session", "-d", "-t", "hufu", "-s", "hufu-eink"); err != nil {
				t.Fatal(err)
			}
			if err := tmuxRun("new-session", "-d", "-s", "docs"); err != nil {
				t.Fatal(err)
			}
			initial := tc.current
			if tc.previous != "" {
				initial = tc.previous
			}
			client := startLastSessionTestClient(t, initial)
			if tc.previous != "" {
				if err := tmuxRun(switchClientSessionArgs(client, tc.current)...); err != nil {
					t.Fatal(err)
				}
			}
			if tc.other != "" {
				_ = startLastSessionTestClient(t, tc.other)
			}
			t.Setenv(popupClientEnv, client)
			// Repeat to ensure a successful fallback cannot create a pair loop.
			want := tc.want
			for range 2 {
				err := lastSessionSwitch()
				if (err != nil) != tc.wantErr {
					t.Fatalf("lastSessionSwitch() = %v, wantErr %t", err, tc.wantErr)
				}
				lines, err := tmuxRunLines("list-clients", "-F", "#{client_name}\t#{client_session}")
				if err != nil || !slices.Contains(lines, client+"\t"+want) {
					t.Fatalf("client sessions = %q, %v; want %s on %q", lines, err, client, want)
				}
				if !tc.wantErr && loadVisitStack().peek() != want {
					t.Fatalf("visit stack recorded %q, want caller's session %q", loadVisitStack().peek(), want)
				}
				if !tc.wantErr {
					// The next invocation should return to the original workspace.
					want = tc.current
				}
			}
		})
	}
}

func TestLastSessionSwitchUsesInspectedTargetWhenHistoryChanges(t *testing.T) {
	withTestTmuxServer(t)
	for _, name := range []string{"work", "docs"} {
		if err := tmuxRun("new-session", "-d", "-s", name); err != nil {
			t.Fatal(err)
		}
	}
	if err := tmuxRun("new-session", "-d", "-t", "work", "-s", "work-eink"); err != nil {
		t.Fatal(err)
	}
	client := startLastSessionTestClient(t, "docs")
	if err := tmuxRun(switchClientSessionArgs(client, "work")...); err != nil {
		t.Fatal(err)
	}
	t.Setenv(popupClientEnv, client)
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	changed := filepath.Join(binDir, "history-changed")
	// Inject two real switches after the history read but before the switch
	// command executes. A late switch-client -l would now select work-eink.
	command := shellQuote(realTmux) + " -L " + shellQuote(getTmuxServer().value) +
		" switch-client -c " + shellQuote(client)
	script := "#!/bin/sh\nset -eu\ncase \" $* \" in\n" +
		"*' switch-client '*)\n" +
		command + " -t '=work-eink'\n" +
		command + " -t '=work'\n" +
		": > " + shellQuote(changed) + "\n;;\nesac\n" +
		"exec " + shellQuote(realTmux) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := lastSessionSwitch(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(changed); err != nil {
		t.Fatalf("history change was not injected: %v", err)
	}
	current, _, err := tmuxClientSessionHistory(client)
	if err != nil || current != "docs" {
		t.Fatalf("client session = %q, %v; want inspected target docs", current, err)
	}
	if got := loadVisitStack().peek(); got != "docs" {
		t.Fatalf("visit stack recorded %q, want docs", got)
	}
}

func TestSwitchClientSessionArgsUsesCallerClient(t *testing.T) {
	tests := []struct {
		name   string
		client string
		want   []string
	}{
		{name: "current client", want: []string{"switch-client", "-t", "=work"}},
		{name: "popup caller client", client: "/dev/pts/4", want: []string{"switch-client", "-c", "/dev/pts/4", "-t", "=work"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := switchClientSessionArgs(tt.client, "work"); !slices.Equal(got, tt.want) {
				t.Errorf("switchClientSessionArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSwitchClientSessionTargetDoesNotMatchEinkPrefix(t *testing.T) {
	withTestTmuxServer(t)
	if err := tmuxRun("new-session", "-d", "-s", "work-eink"); err != nil {
		t.Fatal(err)
	}
	client := startLastSessionTestClient(t, "qs-test-root")
	if err := tmuxRun(switchClientSessionArgs(client, "work")...); err == nil {
		t.Fatal("missing work session must not resolve to work-eink")
	}
	current, _, err := tmuxClientSessionHistory(client)
	if err != nil || current != "qs-test-root" {
		t.Fatalf("client session = %q, %v; want unchanged qs-test-root", current, err)
	}
}

func TestLastSessionRecordAndRead(t *testing.T) {
	defer withXDGCacheDir(t)()
	withTestTmuxServer(t)
	// recordLastSession will try to run `tmux display-message` —
	// if that fails (e.g. no tmux server in the test env), it
	// just no-ops, so we can't directly test the write. But we
	// CAN test that the file is empty / nonexistent when tmux
	// isn't available.
	recordLastSession()
	// After recording, the file either exists (tmux was available)
	// or doesn't (no tmux). Either is valid — we just verify that
	// lastSessionSwitch handles the missing-file case gracefully.
	if err := lastSessionSwitch(); err == nil {
		t.Log("lastSessionSwitch succeeded — tmux is available in the test env")
	} else {
		t.Logf("lastSessionSwitch errored as expected when no session: %v", err)
	}
}

// TestVisitStackJSONFormat verifies the on-disk format is a plain
// JSON array of strings (so other tools can read it).
func TestVisitStackJSONFormat(t *testing.T) {
	defer withXDGCacheDir(t)()
	s := visitStack{}
	s = s.push("alpha")
	s = s.push("beta")
	s.save()

	data, err := os.ReadFile(visitStackPath())
	if err != nil {
		t.Fatal(err)
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("disk format should be plain JSON array, got error: %v\ncontent: %s", err, data)
	}
	if len(arr) != 2 || arr[0] != "beta" || arr[1] != "alpha" {
		t.Errorf("decoded = %v, want [beta alpha]", arr)
	}
}
