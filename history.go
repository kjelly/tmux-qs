package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Input history records the texts the user has submitted from the input
// box — prompts sent with Alt-Enter, names typed for Ctrl-r rename and
// Alt-n new-session, and batch sends. The TUI lets the user recall them
// with Ctrl-Up / Ctrl-Down, the way a shell recalls command history.
//
// Stored newline-delimited (newest last) in the cache directory. Best
// effort: any I/O error just means history is empty this run.

const (
	inputHistoryFileName = "input-history.txt"
	inputHistoryMax      = 200
)

func inputHistoryPath() string {
	return xdgCachePath(inputHistoryFileName)
}

// loadInputHistory returns the saved input history, oldest first.
func loadInputHistory() []string {
	path := inputHistoryPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// appendInputHistory adds text to the persisted history (deduping an
// immediate repeat of the most recent entry) and trims it to
// inputHistoryMax. Returns the updated in-memory slice. Best-effort: the
// returned slice is always valid even if the write fails.
func appendInputHistory(existing []string, text string) []string {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return existing
	}
	// Drop an identical most-recent entry so repeated sends don't pile up.
	if n := len(existing); n > 0 && existing[n-1] == text {
		return existing
	}
	existing = append(existing, text)
	if len(existing) > inputHistoryMax {
		existing = existing[len(existing)-inputHistoryMax:]
	}
	path := inputHistoryPath()
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			_ = os.WriteFile(path, []byte(strings.Join(existing, "\n")+"\n"), 0o644)
		}
	}
	return existing
}
