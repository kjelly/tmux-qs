package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	lastSessionFile  = "last-session"
	visitStackFile   = "visit-stack.json"
	visitStackMaxLen = 20
)

// lastSessionSwitch returns the current tmux client's previous session. When
// running outside tmux there is no current client, so it first uses the
// session belonging to the most recently active tmux client. The cache remains
// a fallback for a standalone invocation when no client is attached.
func lastSessionSwitch() error {
	if shouldUseTmuxClientLastSession(os.Getenv("TMUX"), os.Getenv(popupClientEnv)) {
		return switchClientToLastSession(strings.TrimSpace(os.Getenv(popupClientEnv)))
	}

	if name, err := lastActiveTmuxClientSession(); err == nil {
		return tmuxAttach(name)
	}

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

// lastActiveTmuxClientSession returns the session used by the tmux client
// whose terminal was active most recently. This is the standalone equivalent
// of switch-client -l: outside tmux there is no current client whose history
// tmux can consult, so the server-wide client activity is the useful signal.
func lastActiveTmuxClientSession() (string, error) {
	lines, err := tmuxRunLines("list-clients", "-F", "#{client_activity}\t#{client_session}")
	if err != nil {
		return "", fmt.Errorf("list tmux clients: %w", err)
	}
	if session := mostRecentClientSession(lines); session != "" {
		return session, nil
	}
	return "", fmt.Errorf("no attached tmux client")
}

// mostRecentClientSession selects a session from list-clients rows formatted
// as "client_activity<TAB>client_session". Malformed rows are ignored so a
// single unexpected client record does not prevent the picker fallback.
func mostRecentClientSession(lines []string) string {
	var (
		mostRecentActivity int64
		mostRecentSession  string
		found              bool
	)
	for _, line := range lines {
		activityText, session, ok := strings.Cut(line, "\t")
		if !ok || strings.TrimSpace(session) == "" {
			continue
		}
		activity, err := strconv.ParseInt(strings.TrimSpace(activityText), 10, 64)
		if err != nil {
			continue
		}
		if !found || activity > mostRecentActivity {
			mostRecentActivity = activity
			mostRecentSession = session
			found = true
		}
	}
	return mostRecentSession
}

// shouldUseTmuxClientLastSession reports whether tmux has a client whose own
// navigation history we can use. popupClientEnv identifies the invoking
// client when the command runs inside a display-popup child.
func shouldUseTmuxClientLastSession(tmuxEnv, popupClient string) bool {
	return strings.TrimSpace(tmuxEnv) != "" || strings.TrimSpace(popupClient) != ""
}

// switchClientToLastSession asks tmux to restore the previous session for
// client. If this client has no previous session, it falls back to the
// different session used by the most recently active other client. An empty
// client makes tmux use the client associated with this process. This
// deliberately avoids last-session: that file is global and can be overwritten
// by another attached client.
func switchClientToLastSession(client string) error {
	client = strings.TrimSpace(client)
	args := switchClientLastSessionArgs(client)
	lastErr := tmuxRun(args...)
	if lastErr == nil {
		recordVisitForClient(client)
		return nil
	}

	currentClient := client
	if currentClient == "" {
		currentClient = currentTmuxClientName()
	}
	if currentClient == "" {
		return fmt.Errorf("no previous or other client session: %w", lastErr)
	}
	session, err := lastActiveOtherTmuxClientSession(currentClient)
	if err != nil {
		return fmt.Errorf("no previous or other client session: %w", lastErr)
	}
	if err := tmuxRun(switchClientSessionArgs(currentClient, session)...); err != nil {
		return fmt.Errorf("switch to recent client session %q: %w", session, err)
	}
	recordVisitForClient(currentClient)
	return nil
}

func switchClientLastSessionArgs(client string) []string {
	args := []string{"switch-client"}
	if client = strings.TrimSpace(client); client != "" {
		args = append(args, "-c", client)
	}
	return append(args, "-l")
}

func switchClientSessionArgs(client, session string) []string {
	args := []string{"switch-client"}
	if client = strings.TrimSpace(client); client != "" {
		args = append(args, "-c", client)
	}
	return append(args, "-t", session)
}

// lastActiveOtherTmuxClientSession finds the most recently active other
// client's session, excluding both the current client and the session it is
// already viewing.
func lastActiveOtherTmuxClientSession(currentClient string) (string, error) {
	currentClient = strings.TrimSpace(currentClient)
	if currentClient == "" {
		return "", fmt.Errorf("cannot determine current tmux client")
	}
	lines, err := tmuxRunLines("list-clients", "-F", "#{client_activity}\t#{client_name}\t#{client_session}")
	if err != nil {
		return "", fmt.Errorf("list tmux clients: %w", err)
	}
	if session := mostRecentOtherClientSession(lines, currentClient); session != "" {
		return session, nil
	}
	return "", fmt.Errorf("no other tmux client session")
}

type clientSessionActivity struct {
	activity int64
	client   string
	session  string
}

// mostRecentOtherClientSession selects from list-clients rows formatted as
// "client_activity<TAB>client_name<TAB>client_session". A session already
// shown by currentClient is not considered an alternative.
func mostRecentOtherClientSession(lines []string, currentClient string) string {
	currentClient = strings.TrimSpace(currentClient)
	if currentClient == "" {
		return ""
	}

	records := make([]clientSessionActivity, 0, len(lines))
	currentSession := ""
	for _, line := range lines {
		activityText, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		client, session, ok := strings.Cut(rest, "\t")
		client = strings.TrimSpace(client)
		session = strings.TrimSpace(session)
		if !ok || client == "" || session == "" {
			continue
		}
		activity, err := strconv.ParseInt(strings.TrimSpace(activityText), 10, 64)
		if err != nil {
			continue
		}
		records = append(records, clientSessionActivity{
			activity: activity,
			client:   client,
			session:  session,
		})
		if client == currentClient {
			currentSession = session
		}
	}
	if currentSession == "" {
		return ""
	}

	var (
		mostRecentActivity int64
		mostRecentSession  string
		found              bool
	)
	for _, record := range records {
		if record.client == currentClient || record.session == currentSession {
			continue
		}
		if !found || record.activity > mostRecentActivity {
			mostRecentActivity = record.activity
			mostRecentSession = record.session
			found = true
		}
	}
	return mostRecentSession
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
	recordVisitForClient("")
}

// recordVisitForClient pushes client’s current session onto the visit stack.
// Supplying the caller client is necessary after switch-client -c from a
// popup: the popup pane itself may still be associated with the old session.
func recordVisitForClient(client string) {
	args := []string{"display-message", "-p"}
	if client = strings.TrimSpace(client); client != "" {
		args = append(args, "-c", client)
	}
	args = append(args, "#S")
	name, _ := tmuxRunOut(args...)
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
