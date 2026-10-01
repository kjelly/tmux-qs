package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const scopedPromptHistoryFileName = "prompt-history.json"

// Prompt history is keyed by the target directory and foreground agent so
// the snippet picker never offers a prompt from an unrelated project.
func scopedPromptHistoryKey(path, command string) string {
	return filepath.Clean(path) + "\x00" + command
}

func loadScopedPromptHistories() map[string][]string {
	data, err := os.ReadFile(xdgCachePath(scopedPromptHistoryFileName))
	if err != nil {
		return map[string][]string{}
	}
	var histories map[string][]string
	if err := json.Unmarshal(data, &histories); err != nil || histories == nil {
		return map[string][]string{}
	}
	return histories
}

func scopedPromptHistory(path, command string) []string {
	return loadScopedPromptHistories()[scopedPromptHistoryKey(path, command)]
}

func appendScopedPromptHistory(path, command, prompt string) {
	if path == "" || command == "" || strings.TrimSpace(prompt) == "" {
		return
	}
	histories := loadScopedPromptHistories()
	key := scopedPromptHistoryKey(path, command)
	list := histories[key]
	if len(list) == 0 || list[len(list)-1] != prompt {
		list = append(list, prompt)
	}
	if len(list) > inputHistoryMax {
		list = list[len(list)-inputHistoryMax:]
	}
	histories[key] = list
	data, err := json.Marshal(histories)
	if err != nil {
		return
	}
	pathOnDisk := xdgCachePath(scopedPromptHistoryFileName)
	if pathOnDisk == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(pathOnDisk), 0o755); err == nil {
		_ = os.WriteFile(pathOnDisk, data, 0o600)
	}
}

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
