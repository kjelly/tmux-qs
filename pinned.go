package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	tea "github.com/charmbracelet/bubbletea"
)

type pinMsg struct {
	entry  string
	pinned bool
}

func loadPinned() map[string]bool {
	pinned := make(map[string]bool)
	path := xdgConfigPath("pinned.txt")
	if path == "" {
		return pinned
	}
	file, err := os.Open(path)
	if err != nil {
		return pinned
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			pinned[line] = true
		}
	}
	return pinned
}

func savePinned(pinned map[string]bool) error {
	path := xdgConfigPath("pinned.txt")
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for k, v := range pinned {
		if v {
			if _, err := writer.WriteString(k + "\n"); err != nil {
				return err
			}
		}
	}
	return writer.Flush()
}

func togglePinCmd(entry string, currentlyPinned bool) tea.Cmd {
	return func() tea.Msg {
		pinnedMap := loadPinned()
		nextState := !currentlyPinned
		if nextState {
			pinnedMap[entry] = true
		} else {
			delete(pinnedMap, entry)
		}
		_ = savePinned(pinnedMap)
		return pinMsg{entry: entry, pinned: nextState}
	}
}
