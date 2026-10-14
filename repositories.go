package main

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

type repositoriesMsg struct {
	generation uint64
	entries    []string
	byDir      map[string][]string
}

// Both annotation passes consume the same immutable repository snapshot;
// neither repeats the ancestor walk for the other pass or for session aliases.
func repositoryCmd(items []string, paths map[string]string, generation uint64) tea.Cmd {
	items = slices.Clone(items)
	return func() tea.Msg {
		if paths == nil {
			paths = tmuxSessionPaths()
		}
		return repositoriesMsg{generation: generation, entries: items, byDir: entriesByDir(items, paths)}
	}
}
