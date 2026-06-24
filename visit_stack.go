package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	lastSessionFile  = "last-session"
	visitStackFile   = "visit-stack.json"
	visitStackMaxLen = 20
)

// lastSessionSwitch reads the last-attached session from the cache
// file and switches to it. Used by the --last CLI flag for a
// one-keystroke "Alt-Tab" between the two most recent sessions.
func lastSessionSwitch() error {
	path := xdgCachePath(lastSessionFile)
	if path == "" {
		return fmt.Errorf("no last session recorded")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no last session recorded")
	}
	name := string(data)
	if name == "" {
		return fmt.Errorf("no last session recorded")
	}
	return switchOrAttach(name)
}

// recordLastSession writes the current attached session name to the
// cache file. Called before every switchOrAttach so the next --last
// invocation can return to this session.
func recordLastSession() {
	name, _ := tmuxRunOut("display-message", "-p", "#S")
	if name == "" {
		return
	}
	path := xdgCachePath(lastSessionFile)
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(name), 0o644)
}

// visitStack is a LIFO stack of recently-visited session names.
// Persisted as JSON in the cache directory.
type visitStack struct {
	entries []string
}

func visitStackPath() string {
	return xdgCachePath(visitStackFile)
}

func loadVisitStack() visitStack {
	path := visitStackPath()
	if path == "" {
		return visitStack{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return visitStack{}
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		return visitStack{}
	}
	return visitStack{entries: entries}
}

func (s visitStack) save() {
	path := visitStackPath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	data, _ := json.Marshal(s.entries)
	_ = os.WriteFile(path, data, 0o644)
}

// push adds name to the top of the stack. If name is already in the
// stack, it is moved to the top (dedup). The stack is capped at
// visitStackMaxLen.
func (s visitStack) push(name string) visitStack {
	// Remove existing occurrence.
	filtered := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		if e != name {
			filtered = append(filtered, e)
		}
	}
	// Prepend to top.
	s.entries = append([]string{name}, filtered...)
	if len(s.entries) > visitStackMaxLen {
		s.entries = s.entries[:visitStackMaxLen]
	}
	return s
}

// pop removes and returns the top entry. Returns "" if empty.
func (s *visitStack) pop() string {
	if len(s.entries) == 0 {
		return ""
	}
	top := s.entries[0]
	s.entries = s.entries[1:]
	return top
}

// peek returns the top entry without removing it.
func (s visitStack) peek() string {
	if len(s.entries) == 0 {
		return ""
	}
	return s.entries[0]
}

// recordVisit pushes the current attached session onto the visit
// stack. Called after every successful switchOrAttach.
func recordVisit() {
	name, _ := tmuxRunOut("display-message", "-p", "#S")
	if name == "" {
		return
	}
	s := loadVisitStack().push(name)
	s.save()
}

// visitStackBack pops the current session from the stack and switches
// to the previous one. Used by the --back CLI flag and Alt-← in the
// TUI.
func visitStackBack() error {
	s := loadVisitStack()
	// Pop current (top) to get to the previous one.
	_ = s.pop()
	prev := s.pop()
	if prev == "" {
		return fmt.Errorf("no previous session in visit stack")
	}
	// Push prev back on top (it becomes current after switch).
	s = s.push(prev)
	s.save()
	return switchOrAttach(prev)
}

// visitStackForward switches to the next session in the visit stack.
// Used by the --forward CLI flag and Alt-→ in the TUI. This is a
// best-effort operation — the "forward" direction is only meaningful
// after a --back, and the stack may have been modified since.
func visitStackForward() error {
	s := loadVisitStack()
	if len(s.entries) < 2 {
		return fmt.Errorf("no forward session in visit stack")
	}
	// The second entry is the one we came from.
	next := s.entries[1]
	return switchOrAttach(next)
}

// visitStackBackCmd returns a tea.Cmd that switches to the previous
// session in the visit stack. Used by Alt-← in the TUI.
func visitStackBackCmd() tea.Cmd {
	return func() tea.Msg {
		if err := visitStackBack(); err != nil {
			return uiErrMsg{err}
		}
		return tea.Quit()
	}
}

// visitStackForwardCmd returns a tea.Cmd that switches to the next
// session in the visit stack. Used by Alt-→ in the TUI.
func visitStackForwardCmd() tea.Cmd {
	return func() tea.Msg {
		if err := visitStackForward(); err != nil {
			return uiErrMsg{err}
		}
		return tea.Quit()
	}
}
