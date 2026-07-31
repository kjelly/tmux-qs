package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	defaultPopupSpec  = "top,70%"
	defaultVimEnabled = false
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
// When unset (e.g. `go run` or a plain `go build`) it falls back to
// "dev" so --version is still useful for local development.
var version = "dev"

// vimEnabled determines if Vim mode is enabled.
var vimEnabled = defaultVimEnabled

const usage = `tmux-qs - tmux session quick switcher

Usage: tmux-qs [options] [session-name]

Commands:
  theme apply     Apply terminal and tmux styles for all connected clients.
                  Light is selected only when client_width matches @eink-widths.
  eink            Manage e-ink widths or the current client override.
	                  Use list, set WIDTHS, add [WIDTH], remove [WIDTH], reset,
	                  force, unforce, or status.

Options:
  --popup[=OPTS]  Open in a tmux popup (default when inside tmux).
                  OPTS like fzf: [center|top|bottom|left|right][,SIZE[%]][,SIZE[%]]
                  (default: ` + defaultPopupSpec + `)
  --no-popup      Run inline in the current terminal
  -l, --list      List all active tmux sessions
  -k, --kill NAME Kill the specified tmux session
  --toggle        Open TUI, or if one is already open, close it and switch
                  to the last session (like --last). Designed for binding
                  to a single key.
  --last          Switch to the last-attached session (no TUI)
  --back          Go back one step in the visit stack (no TUI)
  --forward       Go forward one step in the visit stack (no TUI)
  --snippets      Open the snippet picker for the current session's active pane.
  --eink          Create/attach an -eink grouped session for the current session.
  --server=NAME   Target a specific tmux server (sets -L)
  --socket=PATH   Target a specific tmux socket (sets -S)
                  When omitted inside a tmux session, the active server
                  is inherited automatically.
  --all-servers   Scan every running tmux server and show their
                  sessions together, prefixed with the server name.
  -v, --version   Print version and exit
  --vim           Enable vim mode (Esc toggles insert↔normal mode)
  --print-config  Print the resolved (merged) config as TOML to stdout and exit.
                  Useful for validating config.toml syntax in CI / pre-commit.
                  Exits 0 on success, 2 on parse error.
  -h, --help      Show this help
`

func main() {
	if handled, err := runThemeSubcommand(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "tmux-qs: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Initialize fzf's fuzzy-matching scoring tables. Must be called
	// once before any fuzzyScore / fuzzyMatch call.
	initFuzzy()

	popupSpec := defaultPopupSpec
	tmuxUsable := tmuxEnvironmentUsable()
	popup := tmuxUsable && os.Getenv(popupEnv) == ""
	lastSession := false
	visitBack := false
	visitForward := false
	toggle := false
	allServers := false
	printConfig := false
	openSnippets := false
	listSessionsFlag := false
	killSessionTarget := ""
	targetSession := ""

	cliArgs := os.Args[1:]
	for i := 0; i < len(cliArgs); i++ {
		arg := cliArgs[i]
		switch {
		case arg == "--no-popup":
			popup = false
		case arg == "--popup":
			popup = os.Getenv(popupEnv) == ""
		case strings.HasPrefix(arg, "--popup="):
			popup = os.Getenv(popupEnv) == ""
			popupSpec = strings.TrimPrefix(arg, "--popup=")
		case arg == "--toggle":
			toggle = true
		case arg == "--last":
			lastSession = true
		case arg == "--back":
			visitBack = true
		case arg == "--forward":
			visitForward = true
		case arg == "--all-servers":
			allServers = true
		case arg == "--snippets":
			openSnippets = true
		case arg == "--eink":
			if err := createEinkSessionForCurrent(); err != nil {
				fmt.Fprintf(os.Stderr, "tmux-qs: %v\n", err)
				os.Exit(1)
			}
			return
		case arg == "-l" || arg == "--list":
			listSessionsFlag = true
		case arg == "-k" || arg == "--kill":
			if i+1 < len(cliArgs) && !strings.HasPrefix(cliArgs[i+1], "-") {
				i++
				killSessionTarget = cliArgs[i]
			} else {
				fmt.Fprintln(os.Stderr, "tmux-qs: -k/--kill requires a session name")
				os.Exit(2)
			}
		case strings.HasPrefix(arg, "-k=") || strings.HasPrefix(arg, "--kill="):
			parts := strings.SplitN(arg, "=", 2)
			killSessionTarget = parts[1]
		case strings.HasPrefix(arg, "--server="):
			setTmuxServer(tmuxServerSpec{flag: "-L", value: strings.TrimPrefix(arg, "--server=")})
		case strings.HasPrefix(arg, "--socket="):
			setTmuxServer(tmuxServerSpec{flag: "-S", value: strings.TrimPrefix(arg, "--socket=")})
		case arg == "-v" || arg == "--version":
			fmt.Printf("tmux-qs %s\n", version)
			return
		case arg == "--print-config":
			printConfig = true
		case arg == "--no-vim" || arg == "--no-vim-mode" || arg == "--vim=false" || arg == "--vim-mode=false":
			vimEnabled = false
		case arg == "--vim" || arg == "--vim-mode" || arg == "--vim=true" || arg == "--vim-mode=true":
			vimEnabled = true
		case arg == "-h" || arg == "--help":
			fmt.Print(usage)
			return
		default:
			if !strings.HasPrefix(arg, "-") {
				targetSession = arg
			} else {
				fmt.Fprintf(os.Stderr, "unknown option: %s\n%s", arg, usage)
				os.Exit(2)
			}
		}
	}

	if listSessionsFlag {
		sessions, err := listTmuxSessions()
		if err != nil {
			fmt.Fprintf(os.Stderr, "tmux-qs: %v\n", err)
			os.Exit(1)
		}
		for _, s := range sessions {
			fmt.Println(s)
		}
		return
	}

	if killSessionTarget != "" {
		if err := killTmuxSession(killSessionTarget); err != nil {
			fmt.Fprintf(os.Stderr, "tmux-qs: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if targetSession != "" {
		if err := switchOrAttach(targetSession); err != nil {
			fmt.Fprintf(os.Stderr, "tmux-qs: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --print-config: dump the resolved config (after merging file + defaults)
	// as TOML to stdout. Parse errors are reported on stderr and exit with
	// code 2 so it slots into CI pipelines. The merged form is what the
	// running TUI actually uses, so this catches missing fields as well as
	// syntax errors.
	if printConfig {
		path := configFilePath()
		if path == "" {
			fmt.Fprintln(os.Stderr, "tmux-qs: no config file found (using built-in defaults)")
		}
		cfg, err := loadConfigForPrint()
		if err != nil {
			fmt.Fprintf(os.Stderr, "tmux-qs: %v\n", err)
			os.Exit(2)
		}
		if err := printResolvedConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "tmux-qs: %v\n", err)
			os.Exit(2)
		}
		return
	}

	// If we're inside tmux and no --server/--socket was passed
	// explicitly, inherit the active server so the picker can talk
	// to it. --all-servers overrides this — the user wants a union
	// view across every running server.
	if allServers {
		allServersMode = true
	} else if tmuxUsable && getTmuxServer().flag == "" {
		setTmuxServerFromEnv()
	}

	// --toggle: if a tmux-qs instance is already running (popup is open),
	// kill it and switch to the last session. Otherwise fall through to
	// the normal popup/TUI flow. Designed for binding to a single key
	// so the first press opens the picker and the second press dismisses
	// it and goes back to where you were.
	//
	// Two protections against double-press races (especially under
	// system lag, which widens every window in this code path):
	//   1. tryAcquireToggleLock serializes concurrent invocations —
	//      the second process sees the lock held and exits cleanly
	//      instead of racing the first.
	//   2. popupChildPIDs identifies the actual popup TUI child
	//      (via /proc/<pid>/environ reading TMUX_QS_POPUP=1) so we
	//      don't kill our own parent process and orphan the
	//      tmux display-popup machinery.
	//   3. lastSessionSwitch failure falls through to opening the
	//      picker instead of exiting with code 1 — a missing or
	//      stale last-session file during the second press is
	//      expected under lag, not a user-visible error.
	if toggle {
		if runToggle(popupSpec, popup) {
			return
		}
		// runToggle decided to fall through to the normal picker
		// flow (e.g. no popup child was running, or the switch
		// failed and we want to open the picker as a recovery).
	}

	// Fast-path: --last, --back, --forward skip the TUI entirely.
	if lastSession {
		if err := lastSessionSwitch(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if visitBack {
		if err := visitStackBack(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if visitForward {
		if err := visitStackForward(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Mirror ~/bin/workspace guards: detach a leftover "popup" session and
	// bail out if this tmux client already owns a popup child. On Linux, popups
	// open in other attached clients are independent and do not block this one;
	// macOS/BSD retain the conservative process-wide fallback (see popup_child.go).
	if name, err := tmuxRunOut("display-message", "-p", "#S"); err == nil && name == "popup" {
		_ = tmuxRun("detach-client")
	}
	// The popup child skips the check: its parent already performed it and
	// may still be alive for a moment.
	if os.Getenv(popupEnv) == "" && len(popupChildPIDs(currentPopupClient())) > 0 {
		return
	}

	if popup {
		if err := openInPopup(popupSpec, openSnippets); err != nil {
			if !popupLaunchErrorIsRecoverable(err) {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			// A stale/inaccessible tmux socket can pass the initial
			// probe but still reject display-popup. Keep the picker
			// usable in the current terminal in that case.
		} else {
			return
		}
	}

	// Detect the terminal background before Bubble Tea takes over stdin
	// (termenv in tmux never issues OSC queries, and lipgloss only does
	// its one-shot background probe lazily on first render — by then the
	// OSC 11 reply is unreadable). See theme.go.
	themeWatch := initTheme()

	// restoreLastView: when this is the default TUI invocation (no
	// fast-path flag and no explicit server/socket override), try to
	// resume the previous list state. We suppress restore when:
	//   - any fast-path flag is set (--last/--back/--forward/--toggle)
	//     — they skip the TUI entirely
	//   - --all-servers is set — last-view is single-server
	//   - --server/--socket is set — the user is targeting a different
	//     server than the one the cache was recorded against
	restoreLastView := !lastSession && !visitBack && !visitForward && !toggle && !allServers && !openSnippets && getTmuxServer().flag == ""

	p := tea.NewProgram(newModel(themeWatch, restoreLastView, vimEnabled, openSnippets), tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Persist the just-completed TUI's list state so a reopen within
	// 60s can resume it. We do this BEFORE the result handling below
	// so a successful save happens even when the user picks a
	// session (the picker is still "what they were looking at" up
	// until Enter). saveLastView is best-effort and swallows errors.
	if fm, ok := final.(model); ok {
		saveLastView(&fm)
	}

	resultCommand := final.(model).resultCommand
	if resultCommand != "" {
		if err := executeCommand(resultCommand); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if target := final.(model).resultEinkTarget; target != "" {
		if err := toggleEinkSession(target); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if final.(model).resultToggleClose {
		if err := lastSessionSwitch(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	target := final.(model).result
	paneID := final.(model).resultPaneID
	openWithAgent := final.(model).openWithAgent
	selectedAgent := final.(model).selectedAgent
	resultServer := final.(model).resultServer
	resultHintPath := final.(model).resultHintPath
	if target == "" {
		return
	}
	// In all-servers mode, the user picked a session from a
	// non-active server. Temporarily switch to it for the connect
	// call. Restored on exit via defer.
	if resultServer != "" {
		oldSpec := getTmuxServer()
		setTmuxServer(tmuxServerSpec{flag: "-L", value: resultServer})
		defer func() { setTmuxServer(oldSpec) }()
	}
	if err := connect(target, paneID, openWithAgent, selectedAgent, resultHintPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func popupLaunchErrorIsRecoverable(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

// runToggle implements the --toggle behavior. It returns true when
// it consumed the toggle action (successfully closed an existing
// popup and either switched to the last session or decided to
// open the picker). It returns false when it explicitly wants the
// caller to fall through to the normal picker flow — either because
// no popup child was running, or because the switch-back failed and
// opening the picker is a better recovery than exiting 1.
//
// Three layers of defense against rapid M-q double-press under
// system lag:
//
//  1. tryAcquireToggleLock serializes invocations. The second
//     process sees the lock held and returns false, so the caller
//     opens the picker normally (which is the user's intent for
//     the second press anyway, given the picker is now gone).
//
//  2. popupChildPIDs identifies the popup TUI child specifically
//     (via /proc/<pid>/environ reading TMUX_QS_POPUP=1) so we
//     don't kill our own parent process. Killing the parent
//     orphans the tmux display-popup machinery, leaving a
//     zombie popup window under lag.
//
//  3. lastSessionSwitch failure returns false instead of
//     os.Exit(1). Under lag the last-session file may not have
//     been written yet (the popup child is still being killed).
//     Falling through to open the picker is the right user
//     experience — pressing M-q again should still work.
func runToggle(popupSpec string, popup bool) bool {
	// Layer 1: flock. If another tmux-qs is already mid-toggle,
	// bail out and let the caller open the picker (or no-op if
	// it turns out the popup is still being torn down — the
	// next press will succeed).
	release, ok := tryAcquireToggleLock()
	if !ok {
		return false
	}
	defer release()

	// Layer 2: precise PID identification. On Linux we read
	// each candidate's environ to filter to popup children
	// only. On macOS/BSD we fall back to the unfiltered list
	// (the original behavior) since /proc isn't available.
	childPIDs := waitForPopupChild(func() []int {
		return popupChildPIDs(currentPopupClient())
	}, 120*time.Millisecond)
	if len(childPIDs) == 0 {
		// No popup child was running. Open the picker.
		return false
	}

	// Send SIGTERM (default for `kill`) to each popup child.
	// We intentionally do NOT escalate to SIGKILL — Bubble
	// Tea's TUI exits on SIGTERM/SIGINT and writes
	// last-session and last-view caches on the way out. Sending
	// SIGKILL would skip that graceful shutdown and produce
	// the same stale-cache race we're trying to avoid.
	for _, pid := range childPIDs {
		_ = run("kill", strconv.Itoa(pid))
	}

	// Wait for the popup child to die so the last-session file
	// (if it existed) is finalized before we try to read it.
	// 200ms is enough for Bubble Tea's normal shutdown on a
	// healthy system; under lag we accept some staleness and
	// fall through.
	waitForPopupChildExit(childPIDs, 200*time.Millisecond)

	// Brief pause before reading the cache / falling through,
	// to let the OS finish reaping the zombie. 50ms is below
	// human perception but enough for the kernel.
	time.Sleep(50 * time.Millisecond)

	// Layer 3: graceful fallback. If the switch succeeds,
	// we're done. If it fails (no last-session, stale entry,
	// session was killed, etc.) we open the picker instead of
	// exiting 1 — the user's intent for the second press is
	// "give me the picker back", not "show me an error".
	if err := lastSessionSwitch(); err == nil {
		return true
	}
	// lastSessionSwitch failed. Fall through to the picker
	// so the user gets a working TUI rather than an error.
	// The picker's own alt+q handler can still close-and-
	// switch from there.
	_ = popupSpec
	_ = popup
	return false
}

// waitForPopupChild covers the short interval between display-popup being
// started and its tmux-qs child appearing in the process list. Without this
// wait, a fast second M-q can miss the child, after which the normal popup
// guard sees it and silently returns instead of switching back.
func waitForPopupChild(lookup func() []int, timeout time.Duration) []int {
	if children := lookup(); len(children) > 0 {
		return children
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if children := lookup(); len(children) > 0 {
			return children
		}
	}
	return nil
}

// waitForPopupChildExit polls every 20ms (via `kill -0`) until
// every PID in childPIDs has exited or the deadline elapses. Uses
// `kill -0` rather than parsing /proc because it's the most
// portable signal: `kill -0 <pid>` returns 0 if the process is
// alive and non-zero if it has exited (or was never a process we
// could signal — both cases mean "we don't need to wait").
func waitForPopupChildExit(childPIDs []int, timeout time.Duration) {
	if len(childPIDs) == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range childPIDs {
			if err := run("kill", "-0", strconv.Itoa(pid)); err == nil {
				alive = true
				break
			}
		}
		if !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
