package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	sessionNotesFileName = "session-notes.json"
	sessionNotesVersion  = 1
	maxSessionNoteChars  = 4000
	sessionNoteSnippet   = "📝 Edit session note"
)

type sessionNotesFile struct {
	Version int               `json:"version"`
	Entries map[string]string `json:"entries,omitempty"`
}

// sessionNoteEditor wraps bubbles' multiline editor so the main model keeps
// the note-specific input separate from the list filter input. Enter is
// handled by tmux-qs as Save; Alt-Enter remains available for inserting a
// newline inside the note.
type sessionNoteEditor struct {
	textarea.Model
}

func newSessionNoteEditor() sessionNoteEditor {
	editor := textarea.New()
	editor.Prompt = "  "
	editor.Placeholder = "Write a note for this session…"
	editor.ShowLineNumbers = false
	editor.CharLimit = maxSessionNoteChars
	editor.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter"))
	editor.Blur()
	return sessionNoteEditor{Model: editor}
}

func (e *sessionNoteEditor) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	e.Model, cmd = e.Model.Update(msg)
	return cmd
}

func (e *sessionNoteEditor) setSize(width, height int) {
	e.SetWidth(max(20, width-4))
	e.SetHeight(min(12, max(3, height-5)))
}

func (m *model) resizeNoteEditor() {
	m.noteEditor.setSize(m.width, m.height)
}

func sessionNotesPath() string {
	return xdgCachePath(sessionNotesFileName)
}

func loadSessionNotes() map[string]string {
	path := sessionNotesPath()
	if path == "" {
		return map[string]string{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}

	var file sessionNotesFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != sessionNotesVersion {
		return map[string]string{}
	}

	out := make(map[string]string, len(file.Entries))
	for key, note := range file.Entries {
		if key = strings.TrimSpace(key); key != "" {
			if note = normalizeSessionNote(note); note != "" {
				out[key] = note
			}
		}
	}
	return out
}

func saveSessionNotes(notes map[string]string) error {
	path := sessionNotesPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	clean := make(map[string]string, len(notes))
	for key, note := range notes {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if note = normalizeSessionNote(note); note != "" {
			clean[key] = note
		}
	}
	data, err := json.Marshal(sessionNotesFile{
		Version: sessionNotesVersion,
		Entries: clean,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// writeSessionNote reloads immediately before writing so notes already saved
// by another invocation are preserved during the normal sequential case.
func writeSessionNote(key, note string) error {
	notes := loadSessionNotes()
	if note = normalizeSessionNote(note); note == "" {
		delete(notes, key)
	} else {
		notes[key] = note
	}
	return saveSessionNotes(notes)
}

func moveSessionNote(oldSession, newSession string, server tmuxServerSpec) error {
	notes := loadSessionNotes()
	oldKey := sessionNoteKey(oldSession, server)
	note, ok := notes[oldKey]
	if !ok {
		return nil
	}
	delete(notes, oldKey)
	notes[sessionNoteKey(newSession, server)] = note
	return saveSessionNotes(notes)
}

func saveSessionNoteCmd(key, note, previous string, sequence uint64) tea.Cmd {
	return func() tea.Msg {
		return sessionNoteSavedMsg{
			key: key, note: note, previous: previous, sequence: sequence,
			err: writeSessionNote(key, note),
		}
	}
}

func sessionNoteKey(session string, server tmuxServerSpec) string {
	return server.cacheKey() + "\x00" + strings.TrimSpace(session)
}

// noteTargetForEntry resolves the session represented by a list row and the
// server that owns it. Pane/window rows carry the session in their tab
// envelope; all-server rows carry the endpoint in their [server] prefix.
func noteTargetForEntry(entry, fallbackServerLabel string) (string, tmuxServerSpec, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", tmuxServerSpec{}, false
	}
	session := sessionNameForItem(entry)
	if session == "" {
		return "", tmuxServerSpec{}, false
	}
	serverEntry := sessionKeyForItem(entry)
	label, _ := sessionServer(serverEntry)
	if label == "" {
		label = fallbackServerLabel
	}
	return session, serverSpecForLabel(label), true
}

func (m model) selectedNoteTarget() (string, tmuxServerSpec, bool) {
	if m.mode == modeSnippetSelect {
		if strings.TrimSpace(m.snippetTarget.session) == "" {
			return "", tmuxServerSpec{}, false
		}
		return strings.TrimSpace(m.snippetTarget.session), m.snippetTarget.server, true
	}
	if m.mode != modeList {
		return "", tmuxServerSpec{}, false
	}
	selected, ok := m.selected()
	if !ok {
		return "", tmuxServerSpec{}, false
	}
	session, server, ok := noteTargetForEntry(selected, m.windowsServerLabel)
	if !ok {
		return "", tmuxServerSpec{}, false
	}
	key := sessionKeyForItem(selected)
	_, knownSession := m.sessionInfo[key]
	if !knownSession {
		_, knownSession = m.sessionPaths[key]
	}
	// These sources are made exclusively from live tmux panes/sessions, even
	// when their metadata snapshot predates the source load.
	if !knownSession {
		switch m.src {
		case srcPanes, srcTmux, srcWaiting, srcWindows, srcCleanup:
			knownSession = true
		}
	}
	if !knownSession {
		return "", tmuxServerSpec{}, false
	}
	return session, server, true
}

func (m *model) openSelectedSessionNote() bool {
	session, server, ok := m.selectedNoteTarget()
	if !ok {
		return false
	}
	m.openSessionNote(session, server)
	return true
}

func (m *model) openSessionNote(session string, server tmuxServerSpec) {
	m.noteSession = strings.TrimSpace(session)
	m.noteServer = server
	m.noteReturnMode = m.mode
	m.noteReturnInput = m.input.Value()
	m.noteReturnHistory = m.historyPos
	m.noteEditor.SetValue(m.sessionNotes[sessionNoteKey(m.noteSession, server)])
	m.noteEditor.Focus()
	m.noteEditor.CursorEnd()
	m.resizeNoteEditor()
	m.noteConfirm = ""
	m.errText = ""
	m.mode = modeSessionNote
}

func (m *model) leaveSessionNote() {
	returnMode := m.noteReturnMode
	m.mode = returnMode
	m.input.SetValue(m.noteReturnInput)
	m.historyPos = m.noteReturnHistory
	m.noteEditor.Blur()
	m.noteSession = ""
	m.noteServer = tmuxServerSpec{}
	m.noteReturnInput = ""
	m.noteReturnHistory = 0
	m.refilter()
}

func (m *model) saveCurrentSessionNote() (tea.Model, tea.Cmd) {
	key := sessionNoteKey(m.noteSession, m.noteServer)
	previous := m.sessionNotes[key]
	note := normalizeSessionNote(m.noteEditor.Value())
	if m.sessionNotes == nil {
		m.sessionNotes = map[string]string{}
	}
	if note == "" {
		delete(m.sessionNotes, key)
	} else {
		m.sessionNotes[key] = note
	}
	m.noteSaveSequence++
	sequence := m.noteSaveSequence
	session := m.noteSession
	m.leaveSessionNote()
	m.noteConfirm = "saved note for " + session
	return *m, saveSessionNoteCmd(key, note, previous, sequence)
}

func (m model) handleSessionNoteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return m.saveCurrentSessionNote()
	case "esc":
		m.leaveSessionNote()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	default:
		cmd := m.noteEditor.update(msg)
		return m, cmd
	}
}

func normalizeSessionNote(note string) string {
	note = strings.ReplaceAll(note, "\r\n", "\n")
	note = strings.ReplaceAll(note, "\r", "\n")
	var b strings.Builder
	for _, r := range note {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case !unicode.IsControl(r):
			b.WriteRune(r)
		}
	}
	note = strings.TrimSpace(b.String())
	if len([]rune(note)) > maxSessionNoteChars {
		note = string([]rune(note)[:maxSessionNoteChars])
	}
	return note
}

func (m model) sessionNoteForEntry(entry string) string {
	session, server, ok := noteTargetForEntry(entry, m.windowsServerLabel)
	if !ok {
		return ""
	}
	return m.sessionNotes[sessionNoteKey(session, server)]
}

func (m model) hasSessionNote(entry string) bool {
	return m.sessionNoteForEntry(entry) != ""
}

func sessionNoteSnippetChoice() SnippetConfig {
	return SnippetConfig{Name: sessionNoteSnippet, Favorite: true, Note: true}
}

func prependSessionNoteChoice(choices []SnippetConfig) []SnippetConfig {
	out := make([]SnippetConfig, 0, len(choices)+1)
	out = append(out, sessionNoteSnippetChoice())
	out = append(out, choices...)
	return out
}
