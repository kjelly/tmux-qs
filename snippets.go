package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// paneTarget is the exact pane a snippet will be sent to. Keeping the pane ID
// rather than a session/window target avoids tmux's active-pane fallback
// changing between selection and confirmation.
type paneTarget struct {
	session string
	window  string
	index   string
	paneID  string
	command string
	path    string
	server  tmuxServerSpec
}

func (p paneTarget) label() string {
	if p.session == "" {
		return p.paneID
	}
	if p.window == "" {
		return p.session
	}
	return fmt.Sprintf("%s:%s.%s", p.session, p.window, p.index)
}

// resolvePaneTarget turns a list row into the current concrete target pane.
// Pane/window/tmux rows carry a tab envelope whose third field is already a
// pane ID; session rows intentionally resolve to that session's active pane.
func resolvePaneTarget(selected string) (paneTarget, error) {
	return resolvePaneTargetOn(selected, getTmuxServer())
}

func resolvePaneTargetOn(selected string, server tmuxServerSpec) (paneTarget, error) {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return paneTarget{}, fmt.Errorf("no pane selected")
	}
	tmuxTarget := paneLookupTarget(selected)
	format := "#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}\t#{pane_current_path}"
	out, err := tmuxRunOutOn(server, "display-message", "-p", "-t", tmuxTarget, format)
	if err != nil {
		return paneTarget{}, err
	}
	fields := strings.Split(out, "\t")
	if len(fields) < 6 || strings.TrimSpace(fields[3]) == "" {
		return paneTarget{}, fmt.Errorf("cannot resolve target pane")
	}
	return paneTarget{
		session: fields[0], window: fields[1], index: fields[2],
		paneID: fields[3], command: fields[4], path: fields[5], server: server,
	}, nil
}

// paneLookupTarget extracts the exact pane ID embedded by pane/window/tmux
// sources. This is the critical distinction from the old send path, which
// accidentally passed the human-readable display text to tmux.
func paneLookupTarget(selected string) string {
	parts := strings.Split(selected, "\t")
	if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
		return strings.TrimSpace(parts[2])
	}
	return strings.TrimSpace(selected)
}

func hasPaneEnvelope(selected string) bool {
	parts := strings.Split(selected, "\t")
	return len(parts) >= 3 && strings.TrimSpace(parts[2]) != ""
}

func commandsMatch(command string, commands, setNames []string, commandSets map[string][]string) bool {
	if len(commands) == 0 && len(setNames) == 0 {
		return true
	}

	resolvedSets := make([][]string, 0, len(setNames))
	for _, name := range setNames {
		set, ok := commandSets[strings.TrimSpace(name)]
		if !ok {
			return false
		}
		resolvedSets = append(resolvedSets, set)
	}

	for _, candidate := range commands {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(command)) {
			return true
		}
	}
	for _, set := range resolvedSets {
		for _, candidate := range set {
			if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(command)) {
				return true
			}
		}
	}
	return false
}

func snippetMatchesCommand(s SnippetConfig, command string, commandSets map[string][]string) bool {
	return commandsMatch(command, s.Commands, s.CommandSets, commandSets)
}

func snippetMatchesPath(s SnippetConfig, targetPath string) bool {
	if len(s.Paths) == 0 {
		return true
	}
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if targetPath == "." || targetPath == "" {
		return false
	}
	for _, configuredPath := range s.Paths {
		base := filepath.Clean(expandPath(configuredPath))
		if base == "." || base == "" {
			continue
		}
		rel, err := filepath.Rel(base, targetPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// snippetDisplayName returns the explicit label when present, otherwise a
// readable label derived from the action. Derivation never changes Text or
// Keys: trailing spaces and other payload details must still reach the pane.
func snippetDisplayName(s SnippetConfig) string {
	if strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	if len(s.Keys) > 0 {
		keys := make([]string, 0, len(s.Keys))
		for _, key := range s.Keys {
			if key = strings.TrimSpace(key); key != "" {
				keys = append(keys, key)
			}
		}
		return strings.Join(keys, " ")
	}
	return strings.Join(strings.Fields(s.Text), " ")
}

// snippetActionKey identifies the input a snippet sends, independent of its
// display name. A text snippet and a tmux key sequence are different actions
// even when their visible values happen to look alike.
func snippetActionKey(s SnippetConfig) string {
	if s.Note {
		return "internal:session-note"
	}
	if s.Acknowledge {
		return "internal:agent-acknowledge"
	}
	if s.Snooze {
		return "internal:agent-snooze"
	}
	if len(s.Keys) > 0 {
		keys := make([]string, len(s.Keys))
		for i, key := range s.Keys {
			keys[i] = strings.TrimSpace(key)
		}
		return "keys:" + strings.Join(keys, "\x00")
	}
	return "text:" + strings.TrimSpace(s.Text)
}

// dedupeSnippets keeps the first occurrence of each action. Callers can put
// higher-priority sources first (for example, context snippets before config
// snippets) to control which label and metadata survives.
func dedupeSnippets(snippets []SnippetConfig) []SnippetConfig {
	seen := make(map[string]bool, len(snippets))
	out := make([]SnippetConfig, 0, len(snippets))
	for _, snippet := range snippets {
		key := snippetActionKey(snippet)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, snippet)
	}
	return out
}

func matchingSnippets(snippets []SnippetConfig, command string, commandSets map[string][]string) []SnippetConfig {
	return matchingSnippetsForTarget(snippets, command, "", commandSets)
}

func matchingSnippetsForTarget(snippets []SnippetConfig, command, targetPath string, commandSets map[string][]string) []SnippetConfig {
	out := make([]SnippetConfig, 0, len(snippets))
	for _, snippet := range snippets {
		snippet.Name = snippetDisplayName(snippet)
		if snippet.Name == "" || (snippet.Text == "" && len(snippet.Keys) == 0) || !snippetMatchesCommand(snippet, command, commandSets) || !snippetMatchesPath(snippet, targetPath) {
			continue
		}
		out = append(out, snippet)
	}
	// Keep the user's declaration order within each group. Favorites are
	// intentionally first so a controller can reach common actions with only
	// stick navigation and the confirm button.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Favorite && !out[j].Favorite
	})
	return dedupeSnippets(out)
}

// rankSnippets orders a picker without requiring fuzzy text input. Favorites
// form the first tier, then snippets are ordered by how often their literal
// text appears in the submitted input history. Stable sorting preserves the
// config order for equally-used actions.
func rankSnippets(snippets []SnippetConfig, history []string) []SnippetConfig {
	usage := make(map[string]int)
	for _, text := range history {
		text = strings.TrimSpace(text)
		if text != "" {
			usage[text]++
		}
	}
	out := append([]SnippetConfig(nil), snippets...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Favorite != out[j].Favorite {
			return out[i].Favorite
		}
		left := usage[strings.TrimSpace(out[i].Text)]
		right := usage[strings.TrimSpace(out[j].Text)]
		return left > right
	})
	return out
}

func assembleSnippetChoices(configured, recent, context []SnippetConfig, history []string) []SnippetConfig {
	choices := append([]SnippetConfig(nil), configured...)
	choices = append(choices, recent...)
	choices = append(context, choices...)
	seen := make(map[string]int, len(choices))
	merged := make([]SnippetConfig, 0, len(choices))
	for _, snippet := range choices {
		key := snippetActionKey(snippet)
		if index, ok := seen[key]; ok {
			// A configured favorite must not lose its favorite status (or
			// configured submit behavior/name) to an equivalent contextual
			// suggestion that happened to be assembled first.
			if snippet.Favorite && !merged[index].Favorite {
				merged[index] = snippet
			}
			continue
		}
		seen[key] = len(merged)
		merged = append(merged, snippet)
	}
	return rankSnippets(merged, history)
}

// recentSnippetChoices exposes recent free-form prompts as ordinary snippets
// so a controller can resend them without opening the text input. Newest
// unique entries are returned first.
func recentSnippetChoices(history []string, limit int) []SnippetConfig {
	if limit <= 0 {
		return nil
	}
	seen := make(map[string]bool)
	out := make([]SnippetConfig, 0, limit)
	for i := len(history) - 1; i >= 0 && len(out) < limit; i-- {
		text := strings.TrimSpace(history[i])
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, SnippetConfig{Name: "Recent: " + text, Text: text, Submit: true})
	}
	return out
}

func resumeSnippetChoices(target paneTarget, event agentEvent) []SnippetConfig {
	if !isInteractiveShell(target.command) {
		return nil
	}
	commands := []string{target.command}
	makeChoice := func(name, command string) SnippetConfig {
		return SnippetConfig{Name: name, Commands: commands, Text: command, Submit: true, NoHistory: true}
	}
	choices := []SnippetConfig{
		makeChoice("Resume latest Codex in this directory", "codex resume --last"),
		makeChoice("Resume latest Claude in this directory", "claude --continue"),
	}
	if event.Conversation != "" && (event.Cwd == "" || event.Cwd == target.path) {
		switch event.Agent {
		case "codex":
			choices = append([]SnippetConfig{makeChoice("Resume this Codex conversation", shellCommandWithArg("codex resume", event.Conversation))}, choices...)
		case "claude":
			choices = append([]SnippetConfig{makeChoice("Resume this Claude conversation", shellCommandWithArg("claude --resume", event.Conversation))}, choices...)
		}
	}
	return choices
}

func isInteractiveShell(command string) bool {
	switch command {
	case "bash", "zsh", "fish", "nu", "sh", "dash", "ksh":
		return true
	default:
		return false
	}
}

func extractContextSnippets(preview string, clipboard string, command string, rules []DynamicSnippetConfig, commandSets map[string][]string) []SnippetConfig {
	var out []SnippetConfig
	seen := make(map[string]bool)

	// Combine default rules with custom user rules
	allRules := defaultDynamicSnippetRules()
	allRules = append(allRules, rules...)

	lines := strings.Split(preview, "\n")
	for _, rule := range allRules {
		if rule.Disabled {
			continue
		}
		if !commandsMatch(command, rule.Commands, rule.CommandSets, commandSets) {
			continue
		}
		for _, pat := range rule.Matches {
			if strings.TrimSpace(pat) == "" {
				continue
			}
			re, err := regexp.Compile(pat)
			isRegex := err == nil

			for _, line := range lines {
				lineTrim := strings.TrimSpace(line)
				if lineTrim == "" {
					continue
				}

				matched := false
				var name, text string

				if isRegex {
					loc := re.FindStringSubmatchIndex(lineTrim)
					if loc != nil {
						matched = true
						name = string(re.ExpandString(nil, rule.Name, lineTrim, loc))
						text = string(re.ExpandString(nil, rule.Text, lineTrim, loc))
					}
				} else if strings.Contains(strings.ToLower(lineTrim), strings.ToLower(pat)) {
					matched = true
					name = rule.Name
					text = rule.Text
				}

				if matched {
					snippet := SnippetConfig{
						Name:     name,
						Text:     text,
						Submit:   rule.Submit,
						Favorite: rule.Favorite,
					}
					snippet.Name = snippetDisplayName(snippet)
					if snippet.Name == "" {
						continue
					}
					key := snippetActionKey(snippet)
					if !seen[key] {
						seen[key] = true
						out = append(out, snippet)
					}
				}
			}
		}
	}

	if clipboard = strings.TrimSpace(clipboard); clipboard != "" && len(clipboard) < 100 {
		disp := clipboard
		if len(disp) > 30 {
			disp = disp[:27] + "..."
		}
		out = append(out, SnippetConfig{
			Name:   "Paste Clipboard: " + disp,
			Text:   clipboard,
			Submit: false,
		})
	}

	return dedupeSnippets(out)
}

func defaultDynamicSnippetRules() []DynamicSnippetConfig {
	return []DynamicSnippetConfig{
		{
			Name:    "Quick Response: y",
			Matches: []string{"(?i)\\[y/n\\]", "(?i)\\(y/n\\)", "(?i)\\[yes/no\\]"},
			Text:    "y",
			Submit:  true, Favorite: true,
		},
		{
			Name:    "Quick Response: n",
			Matches: []string{"(?i)\\[y/n\\]", "(?i)\\(y/n\\)", "(?i)\\[yes/no\\]"},
			Text:    "n",
			Submit:  true, Favorite: true,
		},
		{
			Name:    "Fix: $1",
			Matches: []string{`FAIL:\s+(\w+)`, `FAILED:\s+(\w+)`},
			Text:    "fix failing test $1 and rerun tests",
			Submit:  true, Favorite: true,
		},
	}
}

func filterSnippetsByCategory(snippets []SnippetConfig, category string) []SnippetConfig {
	if category == "" || category == "All" {
		return snippets
	}
	var out []SnippetConfig
	for _, s := range snippets {
		switch category {
		case "Quick":
			if strings.HasPrefix(s.Name, "Quick Response:") || strings.HasPrefix(s.Name, "Fix:") || strings.HasPrefix(s.Name, "Paste Clipboard:") {
				out = append(out, s)
			}
		case "Blocks":
			if strings.HasPrefix(s.Name, "Block:") {
				out = append(out, s)
			}
		case "Agent":
			if (len(s.Commands) > 0 || len(s.CommandSets) > 0) && !strings.HasPrefix(s.Name, "Block:") {
				out = append(out, s)
			}
		}
	}
	return out
}

// defaultSnippets mirrors the useful pane inputs from ~/bin/fzf-send-keys.nu:
// shared terminal controls plus foreground-program-specific commands. The
// script's assist.nu entry deliberately is not included: it runs a local
// program rather than sending an input to the selected pane.
func defaultSnippets() []SnippetConfig {
	key := func(name, value string, commands ...string) SnippetConfig {
		return SnippetConfig{Name: name, Commands: commands, Keys: []string{value}}
	}
	text := func(name, value string, commands ...string) SnippetConfig {
		return SnippetConfig{Name: name, Commands: commands, Text: value}
	}
	all := []SnippetConfig{
		key("Ctrl-A", "C-a"), key("Ctrl-C", "C-c"), key("Ctrl-D", "C-d"),
		key("Ctrl-N", "C-n"), key("Ctrl-P", "C-p"), key("Ctrl-Q", "C-q"),
		key("Ctrl-Z", "C-z"), key("Alt-Z", "M-z"),
	}
	addText := func(command string, values ...string) {
		for _, value := range values {
			all = append(all, text(command+": "+value, value, command))
		}
	}
	addKey := func(command string, values ...string) {
		for _, value := range values {
			all = append(all, key(command+": "+value, value, command))
		}
	}
	agentText := func(name, value string, commands ...string) SnippetConfig {
		return SnippetConfig{
			Name: name, Commands: commands, Text: value,
			Submit: true, Favorite: true,
		}
	}
	addKey("nvim", "ZZ", "M-h", "M-j", "M-k", "M-l", "M-;")
	addText("opencode", "do it")
	addKey("opencode", "C-p")
	addText("ollama", "do it")
	addKey("ollama", "C-p")
	for _, command := range []string{"codex", "claude", "crush"} {
		agentCommands := []string{command}
		all = append(all,
			agentText("Continue", "continue", agentCommands...),
			agentText("Review changes", "review the current changes and report any issues", agentCommands...),
			agentText("Review and fix", "review the current changes and fix any issues", agentCommands...),
			agentText("Run tests", "run the relevant tests and report any failures", agentCommands...),
			agentText("Run and fix tests", "run the relevant tests and fix failures", agentCommands...),
			agentText("Explain status", "summarize the current status and next step", agentCommands...),
			agentText("Inspect error", "inspect the current error and propose a fix", agentCommands...),
		)
		values := []string{"/help", "/model", "/compact", "/clear", "/status"}
		if command == "codex" {
			values = []string{"/help", "/model", "/review", "/status", "/new", "/compact", "/diff", "/side"}
		}
		if command == "claude" {
			values = append(values, "/config", "/memory")
		}
		if command == "crush" {
			values = []string{"/help", "/model", "/provider", "/new", "/clear", "/compact", "/status"}
		}
		addText(command, values...)
		addKey(command, "C-c", "C-l")
	}
	block := func(name, value string, commands ...string) SnippetConfig {
		return SnippetConfig{Name: "Block: " + name, Commands: commands, Text: value, Submit: false}
	}
	all = append(all,
		block("Prefix: Fix", "Fix "),
		block("Prefix: Explain", "Explain "),
		block("Prefix: Review", "Review "),
		block("Subject: Recent error", "the recent error and logs "),
		block("Subject: Failing tests", "the failing test suite "),
		block("Subject: Git diff", "the current git diff changes "),
		block("Suffix: and run tests", "and run the test suite"),
	)
	return all
}

// sendTextToPane uses -l so a snippet that happens to resemble a tmux key
// name (for example "Enter") is inserted as text. Submit is a separate,
// explicit keypress so text-only snippets never execute accidentally.
func sendTextToPane(target, text string, submit bool) error {
	return sendTextToPaneOn(getTmuxServer(), target, text, submit)
}

func sendTextToPaneOn(server tmuxServerSpec, target, text string, submit bool) error {
	if err := tmuxRunOn(server, "send-keys", "-l", "-t", target, text); err != nil {
		return err
	}
	if submit {
		return tmuxRunOn(server, "send-keys", "-t", target, "Enter")
	}
	return nil
}

func sendSnippetToPane(server tmuxServerSpec, target string, snippet SnippetConfig) error {
	if len(snippet.Keys) > 0 {
		args := append([]string{"send-keys", "-t", target}, snippet.Keys...)
		if err := tmuxRunOn(server, args...); err != nil {
			return err
		}
	} else if err := sendTextToPaneOn(server, target, snippet.Text, false); err != nil {
		return err
	}
	if snippet.Submit {
		return tmuxRunOn(server, "send-keys", "-t", target, "Enter")
	}
	return nil
}

// A picker may remain open while an agent exits and leaves a shell behind.
// Resolve the pane again immediately before sending so Submit cannot execute
// a prompt in a different foreground program.
func sendSnippetToTarget(target paneTarget, snippet SnippetConfig) error {
	current, err := resolvePaneTargetOn(target.paneID, target.server)
	if err != nil {
		return fmt.Errorf("target pane is no longer available: %w", err)
	}
	if current.paneID != target.paneID || current.session != target.session || current.command != target.command {
		return fmt.Errorf("target pane changed; reopen snippets before sending")
	}
	return sendSnippetToPane(target.server, target.paneID, snippet)
}

type snippetSendMsg struct {
	err        error
	closeAfter bool
}

func snippetSendCmd(target paneTarget, snippet SnippetConfig, closeAfter bool) tea.Cmd {
	return func() tea.Msg {
		if snippet.Acknowledge {
			return snippetSendMsg{err: acknowledgePaneAgent(target), closeAfter: closeAfter}
		}
		if snippet.Snooze {
			return snippetSendMsg{err: snoozePaneAgent(target, 10*time.Minute), closeAfter: closeAfter}
		}
		return snippetSendMsg{err: sendSnippetToTarget(target, snippet), closeAfter: closeAfter}
	}
}

// sendSelectedSnippet starts delivery from the snippet list. Enter closes the
// picker after a successful send; Space keeps it open for repeated sends.
func (m model) sendSelectedSnippet(idx int, closeAfter bool) (tea.Model, tea.Cmd) {
	if m.loading || idx < 0 || idx >= len(m.snippetChoices) {
		return m, nil
	}
	m.selectedSnippet = m.snippetChoices[idx]
	if m.selectedSnippet.Note {
		if !m.openSelectedSessionNote() {
			m.errText = "notes require a running tmux session"
		}
		return m, nil
	}
	m.loading = true
	m.errText = ""
	return m, snippetSendCmd(m.snippetTarget, m.selectedSnippet, closeAfter)
}

// startSnippetPicker opens snippets for either the current workspace (Space)
// or the cursor's explicit destination (Ctrl-s).
func (m *model) startSnippetPicker(useSelectedTarget bool) (tea.Cmd, error) {
	selected, ok := m.selected()
	if !ok {
		return nil, fmt.Errorf("select a session or pane first")
	}
	targetEntry := selected
	if !useSelectedTarget {
		targetEntry = m.snippetTargetEntry(selected)
	}
	return m.startSnippetPickerForTarget(targetEntry)
}

// startCurrentSnippetPicker opens snippets for the current session's active
// pane, regardless of a restored list cursor or selected pane row.
func (m *model) startCurrentSnippetPicker() (tea.Cmd, error) {
	if callerPane := strings.TrimSpace(os.Getenv(popupPaneEnv)); callerPane != "" {
		return m.startSnippetPickerForTarget(callerPane)
	}
	if m.currentSession == "" {
		return nil, fmt.Errorf("cannot determine current tmux session")
	}
	return m.startSnippetPickerForTarget(m.currentSession)
}

func (m *model) startSnippetPickerForTarget(targetEntry string) (tea.Cmd, error) {
	bare, _, server := m.tmuxContextForEntry(targetEntry)
	target, err := resolvePaneTargetOn(bare, server)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect target pane: %w", err)
	}
	m.snippetGeneration++
	m.snippetClipboardPending = false
	m.snippetEmptyUnavailable = false
	configured := matchingSnippetsForTarget(m.cfg().Snippets, target.command, target.path, m.cfg().SnippetCommandSets)
	history := m.inputHistory
	if isAgentCommand(target.command, m.watchOpt) {
		history = scopedPromptHistory(target.path, target.command)
	}
	recent := recentSnippetChoices(history, 6)
	paneEvent := readPaneAgentEvent(target.server, target.paneID)
	resumes := resumeSnippetChoices(target, paneEvent)
	var agentActions []SnippetConfig
	if m.src == srcWaiting || paneEvent.Agent != "" {
		agentActions = append(agentActions, SnippetConfig{Name: "Mark agent reviewed", Acknowledge: true, NoHistory: true, Favorite: m.src == srcWaiting})
		agentActions = append(agentActions, SnippetConfig{Name: "Snooze agent 10m", Snooze: true, NoHistory: true})
	}
	m.savedItems = m.items
	m.snippetTarget = target
	// Capture once when the picker opens. This is intentionally a snapshot:
	// it makes the target visible without adding a polling fork while the user
	// browses snippets, and the pane ID remains pinned through confirmation.
	if preview, err := tmuxRunOutOn(target.server, "capture-pane", "-p", "-t", target.paneID, "-S", "-20"); err == nil {
		m.snippetPreview = sanitizePanePreview(preview)
	} else {
		m.snippetPreview = "(pane preview unavailable)"
	}

	ctxSnippets := extractContextSnippets(m.snippetPreview, "", target.command, m.cfg().DynamicSnippets, m.cfg().SnippetCommandSets)

	clipboardSource := m.cfg().Clipboard.ParsedSource()
	if clipboardSource == "tmux-buffer" {
		caps := getTmuxCapabilities(target.server)
		reader := getActiveClipboardReader(m.cfg().Clipboard, caps)
		ctx, cancel := context.WithTimeout(context.Background(), m.cfg().Clipboard.ParsedTimeout())
		rawClip, _ := reader.Read(ctx, ClipboardReadRequest{Server: target.server, Client: currentPopupClient()})
		cancel()
		sanitized := sanitizeClipboardText(rawClip, m.cfg().Clipboard.ValidatedMaxBytes())
		if sanitized != "" {
			clipSnippet := SnippetConfig{
				Name:      "Paste Clipboard: " + runeSafePreview(sanitized, 30),
				Text:      sanitized,
				Submit:    false,
				NoHistory: true,
			}
			ctxSnippets = append(ctxSnippets, clipSnippet)
		}
	}

	choices := assembleSnippetChoices(append(agentActions, append(configured, resumes...)...), recent, ctxSnippets, history)

	if len(choices) == 0 {
		if clipboardSource != "terminal" {
			return nil, fmt.Errorf("no snippets for %s", target.command)
		}
		// terminal source: open empty picker with pending status
		m.allSnippetChoices = nil
		m.snippetChoices = nil
		m.items = nil
		m.snippetCategory = "All"
		m.snippetClipboardPending = true
		m.mode = modeSnippetSelect
		m.input.SetValue("")
		m.errText = ""
		m.refilter()
		return asyncClipboardCmd(m.snippetGeneration, target, m.cfg().Clipboard), nil
	}
	choices = prependSessionNoteChoice(choices)

	m.allSnippetChoices = choices
	m.snippetCategory = "All"
	m.applySnippetCategory()
	m.mode = modeSnippetSelect
	m.input.SetValue("")
	m.errText = ""
	m.refilter()

	if clipboardSource == "terminal" {
		m.snippetClipboardPending = true
		return asyncClipboardCmd(m.snippetGeneration, target, m.cfg().Clipboard), nil
	}

	return nil, nil
}

func (m *model) applySnippetCategory() {
	m.snippetChoices = filterSnippetsByCategory(m.allSnippetChoices, m.snippetCategory)
	m.items = make([]string, len(m.snippetChoices))
	for i, snippet := range m.snippetChoices {
		m.items[i] = snippet.Name
	}
	m.cursor = 0
	m.offset = 0
	m.refilter()
}

func (m *model) cycleSnippetCategory(delta int) {
	categories := []string{"All", "Quick", "Blocks", "Agent"}
	index := 0
	for i, category := range categories {
		if category == m.snippetCategory {
			index = i
			break
		}
	}
	index = (index + delta + len(categories)) % len(categories)
	m.snippetCategory = categories[index]
	m.applySnippetCategory()
}

// snippetTargetEntry separates navigation from delivery. In ordinary session
// lists the cursor is a prospective switch destination, while snippets should
// continue to reach the workspace the user is currently working in. Pane rows
// are explicit delivery targets and therefore always win.
func (m model) snippetTargetEntry(selected string) string {
	if hasPaneEnvelope(selected) {
		return selected
	}
	if m.currentSession != "" {
		return m.currentSession
	}
	return selected
}

func (m *model) leaveSnippetPicker() {
	m.snippetGeneration++
	m.snippetClipboardPending = false
	m.snippetEmptyUnavailable = false
	m.items = m.savedItems
	m.mode = modeList
	m.snippetTarget = paneTarget{}
	m.snippetChoices = nil
	m.allSnippetChoices = nil
	m.selectedSnippet = SnippetConfig{}
	m.snippetPreview = ""
	m.input.SetValue("")
	m.refilter()
}
