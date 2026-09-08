package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// sourceKind identifies which backing store a TUI list is loaded
// from. Each kind is loaded on demand via loadSource(). See the
// keybinding handler for the user-facing binding of each kind.
type sourceKind int

const (
	// srcDefault is the default list shown on TUI open: existing
	// tmux sessions sorted by recency. Previously the first line
	// of `sesh list` (a group header) and any literal "popup"
	// session were filtered out — neither of those exist when
	// we ask tmux directly, so this is now just an alias for
	// srcAll recency-sorted.
	srcDefault sourceKind = iota
	// srcAll is the same as srcDefault at the load level, but the
	// recency sort is handled in the itemsMsg handler. We keep the
	// distinction so future per-source behavior can branch on it.
	srcAll
	// srcTmux lists only running tmux sessions. Bound to Ctrl-t.
	srcTmux
	// srcConfigs lists the user's [[session]] entries from
	// tmux-qs's own config. Bound to Ctrl-g.
	srcConfigs
	// srcZoxide lists all zoxide-tracked directories. Bound to
	// Ctrl-x. Runs `zoxide query --list --score` directly.
	srcZoxide
	// srcZoxideRoot lists zoxide entries that contain the
	// attached tmux session's working directory — i.e. subdirs of
	// the current workspace. Bound to Alt-r.
	srcZoxideRoot
	// srcFind lists directories under $HOME up to depth 2 (matches
	// `fd -H -d 2 -t d -E .Trash . ~`). Bound to Ctrl-f. Doesn't
	// need any external tool.
	srcFind
	// srcWaiting lists only the sessions that currently have waiting
	// agents. Bound to Ctrl-w. It is built from the in-memory watcher
	// snapshot (see model.showWaiting), not via loadSource — there is
	// no external store to load it from.
	srcWaiting
	srcFiles
	srcPanes
	srcSSH
	srcCleanup
	srcCommands
	// srcWindows lists the windows of a single selected session. Bound
	// to Ctrl-v. Like srcWaiting it's populated directly from a one-off
	// tmux query (model.showWindows), not via loadSource.
	srcWindows
)

func (s sourceKind) prompt() string {
	switch s {
	case srcTmux:
		return "🪟  "
	case srcConfigs:
		return "⚙️  "
	case srcZoxide, srcZoxideRoot:
		return "📁  "
	case srcFind:
		return "🔎  "
	case srcWaiting:
		return "⏳  "
	case srcFiles:
		return "📄  "
	case srcPanes:
		return "🔍  "
	case srcSSH:
		return "🌐  "
	case srcCleanup:
		return "🧹  "
	case srcCommands:
		return "🛠️  "
	case srcWindows:
		return "▦  "
	default:
		return "⚡  "
	}
}

func loadSource(kind sourceKind) ([]string, error) {
	switch kind {
	case srcDefault, srcAll:
		return loadAllSources()
	case srcTmux:
		return loadTmuxPanes()
	case srcConfigs:
		return loadConfigSessions()
	case srcZoxide:
		items, _, err := loadZoxide("", buildExcludedSessionPaths())
		return items, err
	case srcZoxideRoot:
		root := attachedSessionPath()
		items, _, err := loadZoxide(root, buildExcludedSessionPaths())
		return items, err
	case srcFind:
		root := attachedSessionPath()
		if root == "" {
			return nil, nil
		}
		return listSubdirs(root, 30)
	case srcFiles:
		return nil, nil
	case srcPanes:
		return loadPanes()
	case srcSSH:
		return loadSSHHosts()
	case srcCleanup:
		return loadCleanup()
	case srcCommands:
		return loadCommands()
	case srcWindows:
		// Populated on demand by model.showWindows; nothing to load.
		return nil, nil
	}
	return nil, nil
}

// loadInitialSource collects tmux metadata and zoxide's database in parallel
// for the default list. Both results are returned to itemsMsg so the UI update
// loop does not repeat either external command.
func loadInitialSource(kind sourceKind, currentSession string, previousInfo map[string]sessionInfo) ([]string, map[string]float64, map[string]sessionInfo, error) {
	if (kind == srcDefault || kind == srcAll) && allServersMode {
		items, info, err := loadAllServerSources()
		return items, nil, info, err
	}
	if (kind == srcDefault || kind == srcAll) && !allServersMode {
		infoCh := make(chan map[string]sessionInfo, 1)
		zoxideCh := make(chan zoxideData, 1)
		go func() { infoCh <- tmuxSessionInfo() }()
		go func() { zoxideCh <- loadZoxideData() }()

		hiddenBase := einkBaseSession(currentSession)
		configs, _ := loadConfigSessions()
		info := <-infoCh
		zoxideItems, scores, err := (<-zoxideCh).parse("", sessionInfoPaths(info))
		if err != nil {
			return nil, nil, info, err
		}

		tmuxSessions := make([]string, 0, len(info))
		for session := range info {
			tmuxSessions = append(tmuxSessions, session)
		}
		all := make([]string, 0, len(tmuxSessions)+len(configs)+len(zoxideItems))
		for _, items := range [][]string{tmuxSessions, configs, zoxideItems} {
			for _, item := range items {
				if !isEinkSessionName(item) && item != hiddenBase {
					all = append(all, item)
				}
			}
		}
		return dedupeSourceItems(all, hiddenBase), scores, info, nil
	}
	if kind == srcZoxide || kind == srcZoxideRoot {
		info := tmuxSessionInfo()
		root := ""
		if kind == srcZoxideRoot {
			root = attachedSessionPath()
		}
		items, scores, err := loadZoxide(root, sessionInfoPaths(info))
		return items, scores, info, err
	}

	items, err := loadSource(kind)
	if err != nil {
		return nil, nil, nil, err
	}
	// Pane/window/config/command sources already loaded their complete own
	// dataset. Retain the last session snapshot rather than immediately
	// forking list-sessions plus list-panes a second time solely for metadata.
	// The default/all sources refresh it on their next activation.
	return items, nil, previousInfo, nil
}

// loadConfigSessions returns the names of the user-defined session
// entries from tmux-qs's own config. Entries whose path doesn't
// resolve to an existing directory are silently skipped — there's
// no point listing a session for a directory that has been deleted.
// Always returns a non-nil slice so callers can rely on ranging
// over the result without a nil check.
func loadConfigSessions() ([]string, error) {
	cfg := loadConfig()
	out := []string{}
	for _, s := range cfg.Sessions {
		if s.Name == "" {
			continue
		}
		path := s.ExpandedPath()
		if path == "" {
			continue
		}
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, s.Name)
	}
	return out, nil
}

// loadZoxide runs `zoxide query --list --score` and returns the
// directory paths (with the home prefix shortened to "~") suitable
// for display, sorted by zoxide's frecency score (highest first),
// along with a map of path -> score for tie-breaking during search.
// If root is non-empty, only entries that contain root as a path
// prefix are returned; this is the "subdirs of the current
// workspace" filter used by srcZoxideRoot.
//
// excludePaths, when non-empty, filters out any entry whose
// absolute path matches a running tmux session's cwd — so a
// directory the user already has a session for doesn't show up
// twice in the picker. Pass buildExcludedSessionPaths() for the
// "all running sessions across the current/all-servers tmux
// state" semantics.
//
// Returns an empty list (no error) if zoxide is not installed or
// has no entries — callers fall back to an empty picker.
func loadZoxide(root string, excludePaths map[string]bool) ([]string, map[string]float64, error) {
	lines, err := runLines("zoxide", "query", "--list", "--score")
	if err != nil {
		return nil, nil, nil
	}
	home, _ := os.UserHomeDir()
	return parseZoxideLines(root, excludePaths, lines, home)
}

type zoxideData struct {
	lines []string
	home  string
}

func loadZoxideData() zoxideData {
	lines, err := runLines("zoxide", "query", "--list", "--score")
	if err != nil {
		return zoxideData{}
	}
	home, _ := os.UserHomeDir()
	return zoxideData{lines: lines, home: home}
}

func (d zoxideData) parse(root string, excludePaths map[string]bool) ([]string, map[string]float64, error) {
	if d.lines == nil {
		return nil, nil, nil
	}
	return parseZoxideLines(root, excludePaths, d.lines, d.home)
}

// parseZoxideLines is the pure (no-IO) parser behind loadZoxide.
// Exposed so tests can drive it with synthetic zoxide output
// without depending on zoxide being installed. home may be "" (no
// "~/"-prefix shortening).
func parseZoxideLines(root string, excludePaths map[string]bool, lines []string, home string) ([]string, map[string]float64, error) {
	rootClean := ""
	if root != "" {
		rootClean = filepath.Clean(root)
	}
	var out []string
	scores := make(map[string]float64)
	for _, l := range lines {
		// Each line from `zoxide query --list --score` is formatted as:
		//   <score> <path>
		// e.g. "  123.4 /home/user/project"
		// The output is already sorted by score descending, so we just
		// need to strip the score prefix and preserve order.
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		idx := strings.IndexByte(l, ' ')
		if idx < 0 {
			continue
		}
		scoreStr := strings.TrimSpace(l[:idx])
		path := strings.TrimSpace(l[idx+1:])
		if path == "" {
			continue
		}
		if rootClean != "" && path != rootClean && !strings.HasPrefix(path, rootClean+"/") {
			continue
		}
		// Hide paths that already back a running tmux session. We
		// compare against the absolute (cleaned) path so a "~"-prefixed
		// home dir still matches the session's cwd correctly.
		if len(excludePaths) > 0 && excludePaths[filepath.Clean(path)] {
			continue
		}
		if home != "" && strings.HasPrefix(path, home) {
			path = "~" + strings.TrimPrefix(path, home)
		}
		if score, err := strconv.ParseFloat(scoreStr, 64); err == nil {
			scores[path] = score
		}
		out = append(out, path)
	}
	return out, scores, nil
}

func loadAllSources() ([]string, error) {
	if allServersMode {
		items, _, err := loadAllServerSources()
		return items, err
	}
	var all []string
	hiddenBase := hiddenEinkBaseSession()

	// In --all-servers mode, scan every running tmux server and
	// merge their sessions, prefixed with the server name so the
	// user can tell which server each session belongs to.
	if allServersMode {
		servers := loadAllServerSnapshot()
		excluded := make(map[string]bool)
		for srv, sessions := range servers {
			for _, session := range sessions {
				s := session.name
				if !isEinkSessionName(s) && s != hiddenBase {
					all = append(all, "["+srv+"] "+s)
				}
				if session.path != "" {
					excluded[filepath.Clean(session.path)] = true
				}
			}
		}
		configs, _ := loadConfigSessions()
		for _, c := range configs {
			if !isEinkSessionName(c) && c != hiddenBase {
				all = append(all, c)
			}
		}
		zoxides, _, _ := loadZoxide("", excluded)
		return dedupeSourceItems(append(all, zoxides...), hiddenBase), nil
	} else {
		// 1. Load tmux sessions from the active server
		tmuxSessions, _ := tmuxRunLines("list-sessions", "-F", "#{session_name}")
		for _, s := range tmuxSessions {
			if !isEinkSessionName(s) && s != hiddenBase {
				all = append(all, s)
			}
		}
	}

	// 2. Load configured sessions
	configs, _ := loadConfigSessions()
	for _, c := range configs {
		if !isEinkSessionName(c) && c != hiddenBase {
			all = append(all, c)
		}
	}

	// 3. Load zoxide directories. buildExcludedSessionPaths filters
	// out any zoxide entry that already backs a running tmux
	// session, so the same workspace doesn't appear twice (once
	// as the session row, once as a directory).
	zoxides, _, _ := loadZoxide("", buildExcludedSessionPaths())
	all = append(all, zoxides...)

	return dedupeSourceItems(all, hiddenBase), nil
}

func loadAllServerSources() ([]string, map[string]sessionInfo, error) {
	hiddenBase := hiddenEinkBaseSession()
	servers := loadAllServerSnapshot()
	info := make(map[string]sessionInfo)
	excluded := make(map[string]bool)
	var all []string
	for label, sessions := range servers {
		for _, session := range sessions {
			if isEinkSessionName(session.name) || session.name == hiddenBase {
				continue
			}
			row := "[" + label + "] " + session.name
			all = append(all, row)
			info[row] = sessionInfo{path: session.path}
			if session.path != "" {
				excluded[filepath.Clean(session.path)] = true
			}
		}
	}
	configs, _ := loadConfigSessions()
	all = append(all, configs...)
	zoxides, _, _ := loadZoxide("", excluded)
	return dedupeSourceItems(append(all, zoxides...), hiddenBase), info, nil
}

type serverSession struct {
	name string
	path string
}

// loadAllServerSnapshot probes each discovered server once, then gets names
// and paths in the same tmux request. The bounded parallel fan-out prevents a
// slow or stale socket from serializing --all-servers startup.
func loadAllServerSnapshot() map[string][]serverSession {
	servers := scanRunningTmuxServers()
	out := make(map[string][]serverSession, len(servers))
	for _, server := range servers {
		out[server.label] = server.sessions
	}
	return out
}

func dedupeSourceItems(items []string, hiddenBase string) []string {
	seen := make(map[string]bool)
	var deduped []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] && !isEinkSessionName(item) && item != hiddenBase {
			seen[item] = true
			deduped = append(deduped, item)
		}
	}
	return deduped
}

func sessionInfoPaths(info map[string]sessionInfo) map[string]bool {
	paths := make(map[string]bool, len(info))
	for _, si := range info {
		if si.path != "" {
			paths[filepath.Clean(si.path)] = true
		}
	}
	return paths
}

// sessionServer extracts the "[server] " prefix from an entry
// produced by loadAllSources in all-servers mode, and returns the
// server name and the bare session name. If the entry doesn't have a
// server prefix, returns ("", entry).
func sessionServer(entry string) (server, session string) {
	entry = strings.TrimSpace(entry)
	if strings.HasPrefix(entry, "[") {
		if i := strings.Index(entry, "] "); i > 0 {
			return entry[1:i], entry[i+2:]
		}
	}
	return "", entry
}

// listSubdirs walks root breadth-first and returns up to maxItems
// descendant directory paths. Hidden directories (those whose
// name starts with '.') are skipped, matching fd's default
// behavior. Permission errors and other read failures are
// swallowed so a single bad subdirectory doesn't kill the whole
// list (the user can still pick from what we did find).
//
// The traversal is BFS so siblings at the same level are emitted
// before descending — that matches the user's mental model of
// "list the children, then their children, then their children".
// Within each level, entries are sorted alphabetically for stable
// output (otherwise ReadDir's order is platform-dependent and
// would make fuzzy matches jump around between launches).
//
// The root itself is NOT included; only descendants. Output paths
// are absolute (filepath.Join(absolute_root, ...)) and have no
// trailing slash, so the result is directly usable as a cwd
// argument to `tmux new-window -c`.
func listSubdirs(root string, maxItems int) ([]string, error) {
	if maxItems <= 0 {
		return nil, nil
	}
	var out []string
	queue := []string{root}
	for len(queue) > 0 {
		if len(out) >= maxItems {
			break
		}
		dir := queue[0]
		queue = queue[1:]

		entries, err := os.ReadDir(dir)
		if err != nil {
			// Permission denied / disappeared mid-walk: skip
			// this directory, don't abort the whole list.
			continue
		}
		// Sort for stable, alphabetical output within each level.
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		for _, e := range entries {
			if len(out) >= maxItems {
				break
			}
			name := e.Name()
			// Skip hidden directories (.git, .vscode, etc.) —
			// matches fd's default hidden-file behavior and
			// keeps the list focused on user-meaningful entries.
			if strings.HasPrefix(name, ".") {
				continue
			}
			if !e.IsDir() {
				continue
			}
			child := filepath.Join(dir, name)
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out, nil
}

// expandPath expands a leading "~" to the user's home directory and cleans
// trailing slashes.
func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	if strings.HasPrefix(p, "/") {
		p = filepath.Clean(p)
	}
	return p
}

// looksLikePath reports whether a list entry refers to a directory rather
// than a tmux session or config name.
func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~")
}

func findFiles(dir string) ([]string, error) {
	dir = expandPath(dir)
	var files []string
	maxFiles := 1000
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(files) >= maxFiles {
			return filepath.SkipDir
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && name != "." && name != ".." {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || name == "tmux-qs" {
			return nil
		}
		if isBinaryFile(path) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err == nil {
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

// isBinaryFile returns true when the file at path looks like binary
// content â i.e. not something we should suggest opening
// in $EDITOR. Used by findFiles so the picker does not offer to
// "edit" compiled binaries, archives, or images.
//
// The detection uses net/http.DetectContentType, which examines up
// to the first 512 bytes and recognizes the magic bytes of:
//
//   - ELF binaries (\x7fELF)
//   - Mach-O 32/64-bit (\xfe\xed\xfa\xce / \xfe\xed\xfa\xcf / etc.)
//   - PE/Windows executables (MZ)
//   - Java class files (\xca\xfe\xba\xbe)
//   - Common archives: gzip, zip, tar, bzip2, xz, 7z, rar
//   - Common media: PNG, JPEG, GIF, BMP, WebP, MP3, MP4, AVI, WAV
//   - PDFs, fonts, WASM, …
//
// Files that look like text (or whose first 512 bytes are too short
// to decide) are treated as editable. Read errors are also treated
// as editable: an unreadable file will fail again in the editor,
// which is a better UX than silently dropping it from the picker.
func isBinaryFile(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, err := io.ReadFull(file, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false
	}
	buf = buf[:n]
	if len(buf) == 0 {
		return false
	}
	ctype := http.DetectContentType(buf)
	// Anything DetectContentType confidently identifies as binary:
	// the generic octet-stream bucket, plus specific image / audio
	// / archive / executable types the user would not want to open
	// in $EDITOR. Text-like types (text/*, application/json,
	// application/xml) and the empty / unknown fallback are kept
	// editable â surfacing them is better than silently
	// hiding a real file the user might want to read.
	switch {
	case ctype == "application/octet-stream":
		return true
	case ctype == "": // very short input
		return false
	}
	// Any "image/", "audio/", "video/" MIME type.
	if len(ctype) >= 6 {
		prefix := ctype[:6]
		if prefix == "image/" || prefix == "audio/" || prefix == "video/" {
			return true
		}
	}
	// Common archive / executable / document formats we want to
	// skip. We list specific types rather than deny-listing
	// "application/*" because legitimate app types (json, xml,
	// javascript, …) should still be editable.
	switch ctype {
	case "application/pdf",
		"application/zip",
		"application/x-gzip",
		"application/gzip",
		"application/x-tar",
		"application/x-bzip2",
		"application/x-xz",
		"application/x-7z-compressed",
		"application/x-rar-compressed",
		"application/wasm",
		"application/x-msdownload",  // .exe
		"application/x-mach-binary", // some macOS binaries
		"application/java-vm",       // .class
		"application/font-sfnt",
		"application/font-woff",
		"application/font-woff2":
		return true
	}
	return false
}

// loadPanes lists all active tmux panes across all sessions.
// It formats them as "session_name:window_index.pane_index  [command]  path"
func loadPanes() ([]string, error) {
	// Format: session:window.pane<TAB>command<TAB>path<TAB>pane_id
	lines, err := tmuxRunLines("list-panes", "-a", "-F", "#{session_name}:#{window_index}.#{pane_index}\t#{pane_current_command}\t#{pane_current_path}\t#{pane_id}")
	if err != nil {
		return nil, err
	}
	var out []string
	home, _ := os.UserHomeDir()
	for _, l := range lines {
		parts := strings.SplitN(l, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		name, cmd, path, paneID := parts[0], parts[1], parts[2], parts[3]
		if home != "" && strings.HasPrefix(path, home) {
			path = "~" + strings.TrimPrefix(path, home)
		}
		// Format: display_text \t session_name \t paneID \t pane cwd.
		// The raw cwd (rather than the home-shortened display path) lets the
		// preview inspect the exact pane's git repository without a second
		// tmux metadata lookup.
		display := fmt.Sprintf("%-20s %-10s %s", name, "["+cmd+"]", path)
		sessionName := strings.SplitN(name, ":", 2)[0]
		out = append(out, fmt.Sprintf("%s\t%s\t%s\t%s", display, sessionName, paneID, parts[2]))
	}
	return out, nil
}

// loadTmuxPanes is the new backing store for srcTmux (Ctrl-t). It
// produces a flat per-pane list, one row per pane, in the same
// "list-sessions alphabetical, panes sorted by (win, pane)" order
// that the old bare-session listing effectively gave. Each row
// carries a tab envelope: `display\t<session>\t<paneID>\t<pane cwd>` so
// choose() can focus the right pane and the preview can inspect that exact
// pane's repository.
//
// The display format is:
//
//	session:win.pane [cmd] ~cwd 「title」
//
// where:
//   - ~cwd is the pane's working directory (NOT the session's cwd),
//     with the user's $HOME prefix shortened to "~".
//   - "title" is `#{pane_title}` (set by shells/editors/agents via
//     OSC 0/2; falls back to foreground command when unset). If
//     title is empty or identical to cmd, the entire "「」" segment
//     is dropped to avoid noise.
func loadTmuxPanes() ([]string, error) {
	// 1. Use list-sessions as the canonical session ordering so the
	// new flat list preserves the previous (alphabetical by default)
	// ordering users are used to from the old srcTmux view. We can't
	// rely on `list-panes -a` alone because tmux returns panes in
	// its own internal order, not in the same order as list-sessions.
	sessionNames, err := tmuxRunLines("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil, err
	}

	// 2. One list-panes pass to grab everything we need per row.
	lines, err := tmuxRunLines("list-panes", "-a", "-F",
		"#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}\t#{pane_current_path}\t#{pane_title}")
	if err != nil {
		return nil, err
	}

	type paneRow struct {
		session string
		winIdx  int
		paneIdx int
		paneID  string
		cmd     string
		dir     string
		title   string
	}

	bySession := make(map[string][]paneRow, len(sessionNames))
	for _, l := range lines {
		parts := strings.SplitN(l, "\t", 7)
		if len(parts) < 7 {
			continue
		}
		if isEinkSessionName(parts[0]) {
			continue
		}
		wIdx, _ := strconv.Atoi(parts[1])
		pIdx, _ := strconv.Atoi(parts[2])
		bySession[parts[0]] = append(bySession[parts[0]], paneRow{
			session: parts[0],
			winIdx:  wIdx,
			paneIdx: pIdx,
			paneID:  parts[3],
			cmd:     parts[4],
			dir:     parts[5],
			title:   parts[6],
		})
	}

	// 3. Emit in sessionOrder; within each session, sort by (win, pane).
	home, _ := os.UserHomeDir()
	var out []string
	for _, s := range sessionNames {
		if isEinkSessionName(s) {
			continue
		}
		rows := bySession[s]
		if len(rows) == 0 {
			// A session with zero panes shouldn't happen, but
			// guard against it — we don't want to silently drop
			// the session from the list.
			continue
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].winIdx != rows[j].winIdx {
				return rows[i].winIdx < rows[j].winIdx
			}
			return rows[i].paneIdx < rows[j].paneIdx
		})
		for _, r := range rows {
			dirStr := r.dir
			if home != "" && strings.HasPrefix(dirStr, home) {
				dirStr = "~" + strings.TrimPrefix(dirStr, home)
			}
			head := fmt.Sprintf("%s:%d.%d [%s] %s", r.session, r.winIdx, r.paneIdx, r.cmd, dirStr)
			display := head
			if r.title != "" && r.title != r.cmd {
				display = head + " 「" + r.title + "」"
			}
			out = append(out, fmt.Sprintf("%s\t%s\t%s\t%s", display, r.session, r.paneID, r.dir))
		}
	}
	return out, nil
}

// loadSSHHosts parses ~/.ssh/config for Host entries.
func loadSSHHosts() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil, nil
	}
	var hosts []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "host ") {
			parts := strings.Fields(line)
			for _, h := range parts[1:] {
				if !strings.Contains(h, "*") && !strings.Contains(h, "?") && !seen[h] {
					seen[h] = true
					hosts = append(hosts, "ssh "+h)
				}
			}
		}
	}
	return hosts, nil
}

// loadCleanup returns a list of sessions that are considered "stale".
// A session is stale if its path doesn't exist anymore, or if it hasn't
// been active in 7 days.
func loadCleanup() ([]string, error) {
	info := tmuxSessionInfo()
	var out []string
	for name, si := range info {
		if isEinkSessionName(name) {
			continue
		}
		stale := false
		if _, err := os.Stat(si.path); os.IsNotExist(err) {
			stale = true
		} else {
			ref := si.meta.lastActive
			if !si.meta.hasLastAct || ref.IsZero() {
				ref = si.meta.created
			}
			if !ref.IsZero() && time.Since(ref) > 7*24*time.Hour {
				stale = true
			}
		}
		if stale {
			out = append(out, name)
		}
	}
	return out, nil
}

// loadCommands returns a list of global actions for the Command Palette.
func loadCommands() ([]string, error) {
	cmds := []string{
		"Tmux: Toggle Eink Session",
		"Resurrect: Save Workspace State",
		"Resurrect: Restore Workspace State",
		"Tmux-QS: Open Config File",
		"Tmux: Detach Client",
		"Tmux: Detach Other Clients",
		"Tmux: Reload Tmux Config",
		"Tmux: Kill Server (Danger)",
	}
	if einkCreateCommandAvailable(currentSessionName()) {
		// Creating the pair is only meaningful from the base session. In
		// an -eink session the toggle action already provides the way back.
		cmds = append([]string{"Tmux: Create Eink Session for Current"}, cmds...)
	}
	if einkClientForced() {
		cmds = append([]string{"Tmux: Clear Current Client Eink Override"}, cmds...)
	} else {
		cmds = append([]string{"Tmux: Force Current Client as Eink"}, cmds...)
	}
	cfg := loadConfig()
	for _, c := range cfg.Commands {
		if c.Name != "" && c.Cmd != "" {
			cmds = append(cmds, c.Name)
		}
	}
	return cmds, nil
}

func einkCreateCommandAvailable(session string) bool {
	return !isEinkSessionName(strings.TrimSpace(session))
}

// autoDetectTags returns a set of tags inferred from marker files in
// the given directory. Used to supplement user-defined tags from
// config so sessions without explicit tags still get useful labels.
func autoDetectTags(dir string) []string {
	if dir == "" {
		return nil
	}
	var tags []string
	checks := []struct {
		file string
		tag  string
	}{
		{"go.mod", "go"},
		{"Cargo.toml", "rust"},
		{"package.json", "js"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
		{"setup.py", "python"},
		{"Makefile", "make"},
		{"CMakeLists.txt", "cmake"},
		{"Dockerfile", "docker"},
		{"docker-compose.yml", "docker"},
		{"docker-compose.yaml", "docker"},
		{"terraform.tf", "terraform"},
		{".github", "ci"},
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(dir, c.file)); err == nil {
			tags = append(tags, c.tag)
		}
	}
	return tags
}

// entryTags returns the tags for a list entry. Config-defined sessions
// use their explicit tags; other entries get auto-detected tags from
// their directory. Returns nil if no tags are available.
func (m model) entryTags(entry string) []string {
	entry = strings.TrimSpace(entry)
	// Check config-defined tags first.
	for _, s := range loadConfig().Sessions {
		if s.Name == entry && len(s.Tags) > 0 {
			return s.Tags
		}
	}
	// Auto-detect from directory.
	dir := entryDir(entry, m.sessionPaths)
	if dir == "" {
		return nil
	}
	return autoDetectTags(dir)
}

// entryGroup returns the group name for a list entry, or "" if the
// entry has no group. Only config-defined sessions can have a group;
// arbitrary paths and zoxide dirs cannot (the user didn't tag them
// as belonging to a project).
func entryGroup(entry string) string {
	entry = strings.TrimSpace(entry)
	for _, s := range loadConfig().Sessions {
		if s.Name == entry {
			return s.Group
		}
	}
	return ""
}

// allGroups returns the set of unique group names across all
// config-defined sessions. Used by the group filter mode to populate
// the selection list. Returns an empty slice if no groups are
// defined.
func allGroups() []string {
	seen := make(map[string]bool)
	for _, s := range loadConfig().Sessions {
		if s.Group != "" {
			seen[s.Group] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	groups := make([]string, 0, len(seen))
	for g := range seen {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	return groups
}

// allTags returns the union of all tags across config sessions and
// auto-detected tags for all current entries. Used by the tag filter
// mode to populate the selection list.
func (m model) allTags() []string {
	seen := make(map[string]bool)
	// Config-defined tags.
	for _, s := range loadConfig().Sessions {
		for _, t := range s.Tags {
			seen[t] = true
		}
	}
	// Auto-detected tags from current entries.
	for _, item := range m.items {
		dir := entryDir(item, m.sessionPaths)
		for _, t := range autoDetectTags(dir) {
			seen[t] = true
		}
	}
	var tags []string
	for t := range seen {
		tags = append(tags, t)
	}
	sortTags(tags)
	return tags
}

func sortTags(tags []string) {
	sort.Strings(tags)
}
