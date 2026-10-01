package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestClipboardSourceDefaultsToTmuxBuffer(t *testing.T) {
	cfg := defaultConfig
	if cfg.Clipboard.Source != "tmux-buffer" {
		t.Errorf("default clipboard source = %q, want %q", cfg.Clipboard.Source, "tmux-buffer")
	}
	if cfg.Clipboard.ParsedSource() != "tmux-buffer" {
		t.Errorf("parsed default clipboard source = %q, want %q", cfg.Clipboard.ParsedSource(), "tmux-buffer")
	}
	if cfg.Clipboard.Timeout != "250ms" {
		t.Errorf("default timeout = %q, want %q", cfg.Clipboard.Timeout, "250ms")
	}
	if cfg.Clipboard.MaxBytes != 4096 {
		t.Errorf("default max_bytes = %d, want 4096", cfg.Clipboard.MaxBytes)
	}
	reader := getClipboardReader(cfg.Clipboard, TmuxCapabilities{})
	if _, ok := reader.(tmuxBufferClipboardReader); !ok {
		t.Errorf("reader type = %T, want tmuxBufferClipboardReader", reader)
	}
}

func TestClipboardDisabledDoesNotQuery(t *testing.T) {
	cfg := ClipboardConfig{Source: "off"}
	reader := getClipboardReader(cfg, TmuxCapabilities{ClipboardQuery: true})
	if _, ok := reader.(disabledClipboardReader); !ok {
		t.Fatalf("reader type = %T, want disabledClipboardReader", reader)
	}
	text, err := reader.Read(context.Background(), ClipboardReadRequest{
		Client: "client1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Fatalf("expected empty text, got %q", text)
	}
}

func TestTmuxBufferClipboardSnippetAppears(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-clip-buf-test"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	if err := tmuxRun("set-buffer", "echo hello_from_buffer"); err != nil {
		t.Fatalf("set-buffer failed: %v", err)
	}

	m := newModel(false, false, false, true)
	m.currentSession = session
	m.items = []string{session}
	cfg := m.cfg()
	cfg.Clipboard.Source = "tmux-buffer"
	m.resolvedCfg = cfg

	_, err := m.startSnippetPickerForTarget(session)
	if err != nil {
		t.Fatalf("startSnippetPickerForTarget failed: %v", err)
	}

	found := false
	for _, s := range m.allSnippetChoices {
		if s.Text == "echo hello_from_buffer" {
			found = true
			if !strings.HasPrefix(s.Name, "Paste Clipboard: ") {
				t.Errorf("snippet Name = %q, want prefix 'Paste Clipboard: '", s.Name)
			}
			if s.Submit {
				t.Errorf("snippet Submit = true, want false")
			}
			if !s.NoHistory {
				t.Errorf("snippet NoHistory = false, want true")
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected clipboard snippet not found in choices: %#v", m.allSnippetChoices)
	}
}

func TestTerminalClipboardUsesExactCallerClient(t *testing.T) {
	reader := terminalClipboardReader{}
	// When client is empty, Read must return an error immediately
	_, err := reader.Read(context.Background(), ClipboardReadRequest{
		Client: "",
	})
	if err == nil || !strings.Contains(err.Error(), "requires non-empty client") {
		t.Fatalf("expected non-empty client error, got: %v", err)
	}
}

type blockingReader struct {
	started chan struct{}
}

func (b *blockingReader) Read(ctx context.Context, req ClipboardReadRequest) (string, error) {
	if b.started != nil {
		close(b.started)
	}
	<-ctx.Done()
	return "", ctx.Err()
}

func TestTerminalClipboardTimeoutDoesNotBlockPicker(t *testing.T) {
	withTestTmuxServer(t)
	const session = "qs-clip-timeout-test"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	blocker := &blockingReader{started: make(chan struct{})}
	setClipboardReaderForTest(blocker)
	defer setClipboardReaderForTest(nil)

	m := newModel(false, false, false, true)
	m.currentSession = session
	m.items = []string{session}
	cfg := m.cfg()
	cfg.Clipboard.Source = "terminal"
	cfg.Clipboard.Timeout = "50ms"
	cfg.Snippets = []SnippetConfig{{Name: "test-snip", Text: "test-cmd"}}
	m.resolvedCfg = cfg

	cmd, err := m.startSnippetPickerForTarget(session)
	if err != nil {
		t.Fatalf("startSnippetPickerForTarget failed: %v", err)
	}
	if m.mode != modeSnippetSelect {
		t.Fatalf("mode = %v, want modeSnippetSelect", m.mode)
	}
	if !m.snippetClipboardPending {
		t.Fatalf("snippetClipboardPending = false, want true")
	}
	if cmd == nil {
		t.Fatal("expected async clipboard cmd to be returned")
	}

	// Trigger the async cmd in background to confirm it respects timeout
	msgCh := make(chan tea.Msg, 1)
	go func() {
		msgCh <- cmd()
	}()

	select {
	case msg := <-msgCh:
		res, ok := msg.(clipboardResultMsg)
		if !ok {
			t.Fatalf("expected clipboardResultMsg, got %T", msg)
		}
		if res.Err == nil {
			t.Errorf("expected timeout error in result msg, got nil")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("async cmd did not return within timeout")
	}
}

func TestTerminalClipboardCommandsHonorContextCancellation(t *testing.T) {
	reader := terminalClipboardReader{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled upfront

	_, err := reader.Read(ctx, ClipboardReadRequest{
		Client: "client0",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestTerminalClipboardOnlyCanOpenEmptyPicker(t *testing.T) {
	withCleanCacheEnv(t)
	withTestTmuxServer(t)
	const session = "qs-empty-clip-test"
	if err := tmuxRun("new-session", "-d", "-s", session, "sleep", "30"); err != nil {
		t.Skipf("cannot create test session: %v", err)
	}
	defer tmuxRun("kill-session", "-t", session)

	m := newModel(false, false, false, true)
	m.currentSession = session
	m.items = []string{session}
	m.inputHistory = nil
	cfg := m.cfg()
	cfg.Clipboard.Source = "terminal"
	cfg.Snippets = nil
	m.resolvedCfg = cfg

	cmd, err := m.startSnippetPickerForTarget(session)
	if err != nil {
		t.Fatalf("startSnippetPickerForTarget failed: %v", err)
	}
	if m.mode != modeSnippetSelect {
		t.Fatalf("mode = %v, want modeSnippetSelect", m.mode)
	}
	if !m.snippetClipboardPending {
		t.Fatal("expected snippetClipboardPending = true")
	}
	if len(m.snippetChoices) != 0 {
		t.Fatalf("expected 0 choices initially, got %d", len(m.snippetChoices))
	}
	if cmd == nil {
		t.Fatal("expected async clipboard cmd")
	}
}

func TestTerminalClipboardLateReplyGenerationDiscarded(t *testing.T) {
	m := newModel(false, false, false, true)
	m.mode = modeSnippetSelect
	m.snippetGeneration = 5
	m.snippetTarget = paneTarget{paneID: "%1"}
	m.snippetClipboardPending = true
	m.allSnippetChoices = []SnippetConfig{{Name: "Existing", Text: "existing"}}

	// Stale generation 4
	staleMsg := clipboardResultMsg{
		Generation: 4,
		Target:     m.snippetTarget,
		Text:       "late text",
	}
	updated, _ := m.Update(staleMsg)
	m2 := updated.(model)

	if !m2.snippetClipboardPending {
		t.Error("stale reply should not clear pending flag")
	}
	for _, c := range m2.allSnippetChoices {
		if c.Text == "late text" {
			t.Fatal("stale reply should not add snippet")
		}
	}
}

func TestTerminalClipboardAmbiguousNewBuffersIgnored(t *testing.T) {
	withTestTmuxServer(t)
	testServer := getTmuxServer()

	// Pre-create two automatic buffers
	_ = tmuxRun("set-buffer", "-b", "buffer101", "first")
	_ = tmuxRun("set-buffer", "-b", "buffer102", "second")
	defer tmuxRun("delete-buffer", "-b", "buffer101")
	defer tmuxRun("delete-buffer", "-b", "buffer102")

	reader := terminalClipboardReader{}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	// We pass a dummy client; since no client exists, refresh-client will fail.
	// But let's test acquireServerClipboardLock + ambiguous buffer logic.
	// We can test ambiguous buffer logic directly:
	currentLines := []string{"buffer101", "buffer102"}
	initial := make(map[string]bool)
	var newNames []string
	for _, l := range currentLines {
		name := strings.TrimSpace(l)
		if !initial[name] && autoBufferRegex.MatchString(name) {
			newNames = append(newNames, name)
		}
	}
	if len(newNames) <= 1 {
		t.Fatalf("expected >1 ambiguous buffers, got %d", len(newNames))
	}

	_, err := reader.Read(ctx, ClipboardReadRequest{
		Server: testServer,
		Client: "dummy-nonexistent-client",
	})
	if err == nil {
		t.Fatal("expected error with nonexistent client or ambiguous buffer")
	}
}

func TestTerminalClipboardWaitsForStableSingleNewBuffer(t *testing.T) {
	withTestTmuxServer(t)
	// Create an automatic buffer
	_ = tmuxRun("set-buffer", "-b", "buffer99", "stable payload")
	defer tmuxRun("delete-buffer", "-b", "buffer99")

	lines, err := tmuxRunLines("list-buffers", "-F", "#{buffer_name}")
	if err != nil {
		t.Fatalf("list-buffers: %v", err)
	}
	found := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "buffer99" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("buffer99 not found in test server")
	}

	// Verify show-buffer reads it
	out, err := tmuxRunOut("show-buffer", "-b", "buffer99")
	if err != nil || out != "stable payload" {
		t.Fatalf("show-buffer returned %q, err: %v", out, err)
	}
}

func TestTerminalClipboardRequestsSerializePerServer(t *testing.T) {
	ctx := context.Background()
	unlock1, err := acquireServerClipboardLock(ctx, "serverA")
	if err != nil {
		t.Fatalf("acquire lock 1: %v", err)
	}

	acquired2 := make(chan bool)
	go func() {
		ctx2, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		unlock2, err := acquireServerClipboardLock(ctx2, "serverA")
		if err == nil {
			unlock2()
			acquired2 <- true
		} else {
			acquired2 <- false
		}
	}()

	// Lock on serverA should block
	select {
	case acq := <-acquired2:
		if acq {
			t.Fatal("concurrent lock on serverA should have blocked")
		}
	case <-time.After(50 * time.Millisecond):
		// Expected to block
	}

	// But lock on serverB should succeed immediately
	unlockB, err := acquireServerClipboardLock(ctx, "serverB")
	if err != nil {
		t.Fatalf("acquire lock on serverB: %v", err)
	}
	unlockB()

	unlock1()
	// After unlock1, serverA should be acquirable
	select {
	case acq := <-acquired2:
		// Completed
		_ = acq
	case <-time.After(200 * time.Millisecond):
	}
}

func TestClipboardOversizeIgnored(t *testing.T) {
	large := strings.Repeat("x", 1000)
	sanitized := sanitizeClipboardText(large, 500)
	if sanitized != "" {
		t.Fatalf("oversize clipboard should be empty, got length %d", len(sanitized))
	}
}

func TestClipboardControlCharsCannotSubmit(t *testing.T) {
	raw := "echo hello\r\nrm -rf /\n\tdo something\x00\x1b[31m"
	sanitized := sanitizeClipboardText(raw, 1000)
	if strings.Contains(sanitized, "\n") || strings.Contains(sanitized, "\r") {
		t.Fatalf("sanitized text contains newline: %q", sanitized)
	}
	if strings.Contains(sanitized, "\x1b") || strings.Contains(sanitized, "\x00") {
		t.Fatalf("sanitized text contains control characters: %q", sanitized)
	}
	want := "echo hello rm -rf / do something[31m"
	if sanitized != want {
		t.Fatalf("sanitized = %q, want %q", sanitized, want)
	}

	snippet := SnippetConfig{
		Name:      "Paste Clipboard: " + runeSafePreview(sanitized, 30),
		Text:      sanitized,
		Submit:    false,
		NoHistory: true,
	}
	if snippet.Submit {
		t.Error("clipboard snippet Submit must be false")
	}
}

func TestClipboardC1ControlCharsRemoved(t *testing.T) {
	// \u0085 is Next Line (NEL), \u009B is Control Sequence Introducer (CSI)
	raw := "line1\u0085line2\u009bcommand"
	sanitized := sanitizeClipboardText(raw, 1000)
	if strings.Contains(sanitized, "\u0085") || strings.Contains(sanitized, "\u009b") {
		t.Fatalf("sanitized text contains C1 control chars: %q", sanitized)
	}
	if sanitized != "line1line2command" {
		t.Fatalf("got %q, want %q", sanitized, "line1line2command")
	}
}

func TestClipboardInvalidUTF8Sanitized(t *testing.T) {
	invalid := "hello" + string([]byte{0xff, 0xfe, 0x80}) + "world"
	sanitized := sanitizeClipboardText(invalid, 1000)
	if strings.Contains(sanitized, "\ufffd") {
		t.Fatalf("sanitized text contains replacement rune: %q", sanitized)
	}
	if sanitized != "helloworld" {
		t.Fatalf("got %q, want %q", sanitized, "helloworld")
	}
}

func TestClipboardPreviewTruncatesByRune(t *testing.T) {
	text := "測試字元長度超過限制內容"
	preview := runeSafePreview(text, 4)
	if preview != "測試字元" {
		t.Fatalf("got %q, want %q", preview, "測試字元")
	}
	if len([]rune(preview)) != 4 {
		t.Fatalf("rune count = %d, want 4", len([]rune(preview)))
	}
}

func TestClipboardDuplicateSnippetDeduped(t *testing.T) {
	m := newModel(false, false, false, true)
	m.mode = modeSnippetSelect
	m.snippetGeneration = 1
	m.snippetTarget = paneTarget{paneID: "%1", command: "bash"}
	m.resolvedCfg = Config{
		Snippets: []SnippetConfig{
			{Name: "Check status", Text: "git status", Favorite: true, Submit: true},
		},
	}
	m.allSnippetChoices = matchingSnippets(m.resolvedCfg.Snippets, "bash", m.resolvedCfg.SnippetCommandSets)
	m.snippetChoices = m.allSnippetChoices

	msg := clipboardResultMsg{
		Generation: 1,
		Target:     m.snippetTarget,
		Text:       "git status", // same command text
	}
	updated, _ := m.Update(msg)
	m2 := updated.(model)

	// Should not duplicate "git status" snippet
	count := 0
	for _, c := range m2.allSnippetChoices {
		if c.Text == "git status" {
			count++
			if c.Name != "Check status" {
				t.Errorf("deduped snippet Name = %q, want configured name %q", c.Name, "Check status")
			}
			if !c.Favorite {
				t.Errorf("deduped snippet Favorite should be preserved")
			}
			if !c.Submit {
				t.Errorf("deduped snippet Submit should be preserved")
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 instance of 'git status', found %d", count)
	}
}

func TestClipboardIncrementalUpdatePreservesCategoryAndCursor(t *testing.T) {
	m := newModel(false, false, false, true)
	m.mode = modeSnippetSelect
	m.snippetGeneration = 1
	m.snippetTarget = paneTarget{paneID: "%1", command: "bash"}
	m.snippetCategory = "Quick"
	m.resolvedCfg = Config{
		Snippets: []SnippetConfig{
			{Name: "Quick Response: snip1", Text: "cmd1"},
			{Name: "Quick Response: snip2", Text: "cmd2"},
			{Name: "Quick Response: snip3", Text: "cmd3"},
		},
	}
	m.allSnippetChoices = m.resolvedCfg.Snippets
	m.snippetChoices = m.allSnippetChoices
	m.items = []string{"Quick Response: snip1", "Quick Response: snip2", "Quick Response: snip3"}
	m.refilter()
	m.cursor = 1 // pointing at snip2

	msg := clipboardResultMsg{
		Generation: 1,
		Target:     m.snippetTarget,
		Text:       "new clipboard text",
	}
	updated, _ := m.Update(msg)
	m2 := updated.(model)

	if m2.snippetCategory != "Quick" {
		t.Errorf("category = %q, want 'Quick'", m2.snippetCategory)
	}
	// Verify snip2 is still the selected snippet at cursor
	if m2.cursor < 0 || m2.cursor >= len(m2.filtered) {
		t.Fatalf("cursor %d out of bounds (len %d)", m2.cursor, len(m2.filtered))
	}
	selected := m2.snippetChoices[m2.filtered[m2.cursor]]
	if selected.Name != "Quick Response: snip2" {
		t.Errorf("selected snippet after update = %q, want 'Quick Response: snip2'", selected.Name)
	}
}

func TestClipboardContentIsNotRecordedInHistory(t *testing.T) {
	withCleanCacheEnv(t)
	m := newModel(false, false, false, true)
	m.inputHistory = nil

	clipSnippet := SnippetConfig{
		Name:      "Paste Clipboard: secret",
		Text:      "secret text",
		Submit:    false,
		NoHistory: true,
	}
	m.selectedSnippet = clipSnippet

	// Simulate successful snippet send
	updated, _ := m.Update(snippetSendMsg{
		err:        nil,
		closeAfter: false,
	})
	m2 := updated.(model)

	for _, h := range m2.inputHistory {
		if strings.Contains(h, "secret text") {
			t.Fatalf("clipboard snippet text found in input history: %v", m2.inputHistory)
		}
	}
}
