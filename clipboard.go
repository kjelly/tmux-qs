package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// copyToClipboard writes text to the system clipboard via the OSC 52
// escape sequence:
//
//	ESC ] 52 ; c ; <base64-encoded-text> BEL
func copyToClipboard(text string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	seq := fmt.Sprintf("\x1b]52;c;%s\x07", encoded)
	_, err := os.Stdout.WriteString(seq)
	return err
}

type ClipboardReadRequest struct {
	Server tmuxServerSpec
	Client string
}

type ClipboardReader interface {
	Read(ctx context.Context, req ClipboardReadRequest) (string, error)
}

type disabledClipboardReader struct{}

func (disabledClipboardReader) Read(ctx context.Context, req ClipboardReadRequest) (string, error) {
	return "", nil
}

type tmuxBufferClipboardReader struct{}

func (tmuxBufferClipboardReader) Read(ctx context.Context, req ClipboardReadRequest) (string, error) {
	out, err := tmuxRunOutContextOn(ctx, req.Server, "show-buffer")
	if err != nil {
		return "", nil // no buffer, empty buffer, or error is non-fatal
	}
	return out, nil
}

type terminalClipboardReader struct{}

var autoBufferRegex = regexp.MustCompile(`^buffer[0-9]+$`)

var (
	serverClipboardSemMu sync.Mutex
	serverClipboardSems  = make(map[string]chan struct{})
)

func acquireServerClipboardLock(ctx context.Context, serverKey string) (func(), error) {
	serverClipboardSemMu.Lock()
	sem, ok := serverClipboardSems[serverKey]
	if !ok {
		sem = make(chan struct{}, 1)
		serverClipboardSems[serverKey] = sem
	}
	serverClipboardSemMu.Unlock()

	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (terminalClipboardReader) Read(ctx context.Context, req ClipboardReadRequest) (string, error) {
	if strings.TrimSpace(req.Client) == "" {
		return "", errors.New("terminal clipboard requires non-empty client")
	}

	unlock, err := acquireServerClipboardLock(ctx, req.Server.cacheKey())
	if err != nil {
		return "", err
	}
	defer unlock()

	// 1. Initial snapshot of buffers
	initialLines, err := tmuxRunLinesContextOn(ctx, req.Server, "list-buffers", "-F", "#{buffer_name}")
	initial := make(map[string]bool)
	if err == nil {
		for _, l := range initialLines {
			initial[strings.TrimSpace(l)] = true
		}
	}

	// 2. Query terminal via refresh-client -l -t <exact-client-name>
	if err := tmuxRunContextOn(ctx, req.Server, "refresh-client", "-l", "-t", req.Client); err != nil {
		return "", err
	}

	// 3. Poll every 25ms until context deadline
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			currentLines, _ := tmuxRunLinesContextOn(ctx, req.Server, "list-buffers", "-F", "#{buffer_name}")
			var newNames []string
			for _, l := range currentLines {
				name := strings.TrimSpace(l)
				if !initial[name] && autoBufferRegex.MatchString(name) {
					newNames = append(newNames, name)
				}
			}

			if len(newNames) == 0 {
				continue
			}
			if len(newNames) > 1 {
				return "", errors.New("ambiguous new buffers detected")
			}

			// Exactly one new automatic buffer seen. Settle window for 50ms
			candidate := newNames[0]
			settleTimer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				settleTimer.Stop()
				return "", ctx.Err()
			case <-settleTimer.C:
			}

			// Poll again after settle window
			postLines, _ := tmuxRunLinesContextOn(ctx, req.Server, "list-buffers", "-F", "#{buffer_name}")
			var postNames []string
			for _, l := range postLines {
				name := strings.TrimSpace(l)
				if !initial[name] && autoBufferRegex.MatchString(name) {
					postNames = append(postNames, name)
				}
			}

			if len(postNames) == 1 && postNames[0] == candidate {
				return tmuxRunOutContextOn(ctx, req.Server, "show-buffer", "-b", candidate)
			}
			if len(postNames) > 1 {
				return "", errors.New("ambiguous new buffers detected")
			}
		}
	}
}

func getClipboardReader(cfg ClipboardConfig, caps TmuxCapabilities) ClipboardReader {
	source := cfg.ParsedSource()
	switch source {
	case "off":
		return disabledClipboardReader{}
	case "terminal":
		if !caps.ClipboardQuery {
			return disabledClipboardReader{}
		}
		return terminalClipboardReader{}
	default:
		return tmuxBufferClipboardReader{}
	}
}

func sanitizeClipboardText(text string, maxBytes int) string {
	if len(text) == 0 || len(text) > maxBytes {
		return ""
	}
	valid := strings.ToValidUTF8(text, "")
	var b strings.Builder
	for _, r := range valid {
		if r == '\r' || r == '\n' || r == '\t' {
			b.WriteRune(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	fields := strings.Fields(b.String())
	return strings.TrimSpace(strings.Join(fields, " "))
}

func runeSafePreview(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes])
}

type clipboardResultMsg struct {
	Generation uint64
	Target     paneTarget
	Text       string
	Err        error
}

var (
	clipboardReaderOverrideMu sync.RWMutex
	clipboardReaderOverride   ClipboardReader
)

func setClipboardReaderForTest(r ClipboardReader) {
	clipboardReaderOverrideMu.Lock()
	defer clipboardReaderOverrideMu.Unlock()
	clipboardReaderOverride = r
}

func getActiveClipboardReader(cfg ClipboardConfig, caps TmuxCapabilities) ClipboardReader {
	clipboardReaderOverrideMu.RLock()
	defer clipboardReaderOverrideMu.RUnlock()
	if clipboardReaderOverride != nil {
		return clipboardReaderOverride
	}
	return getClipboardReader(cfg, caps)
}

func asyncClipboardCmd(gen uint64, target paneTarget, cfg ClipboardConfig) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.ParsedTimeout())
		defer cancel()
		caps := getTmuxCapabilities(target.server)
		reader := getActiveClipboardReader(cfg, caps)
		client := currentPopupClient()
		text, err := reader.Read(ctx, ClipboardReadRequest{
			Server: target.server,
			Client: client,
		})
		return clipboardResultMsg{
			Generation: gen,
			Target:     target,
			Text:       text,
			Err:        err,
		}
	}
}
