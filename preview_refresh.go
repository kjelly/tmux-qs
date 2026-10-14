package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) refreshPreviewIfStaleCmd() tea.Cmd {
	if m.mode != modeList || m.previewEntry == "" {
		return nil
	}
	if cached, ok := m.previewCache[m.previewEntry]; ok && time.Since(cached.at) < previewCacheTTL {
		return nil
	}
	return m.updatePreviewCmd()
}
