package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const defaultPopupSpec = "top,70%"

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
// When unset (e.g. `go run` or a plain `go build`) it falls back to
// "dev" so --version is still useful for local development.
var version = "dev"

const usage = `tmux-qs - tmux session quick switcher

Usage: tmux-qs [options]

Options:
  --popup[=OPTS]  Open in a tmux popup (default when inside tmux).
                  OPTS like fzf: [center|top|bottom|left|right][,SIZE[%]][,SIZE[%]]
                  (default: ` + defaultPopupSpec + `)
  --no-popup      Run inline in the current terminal
  --toggle        Open TUI, or if one is already open, close it and switch
                  to the last session (like --last). Designed for binding
                  to a single key.
  --last          Switch to the last-attached session (no TUI)
  --back          Go back one step in the visit stack (no TUI)
  --forward       Go forward one step in the visit stack (no TUI)
  --server=NAME   Target a specific tmux server (sets -L)
  --socket=PATH   Target a specific tmux socket (sets -S)
                  When omitted inside a tmux session, the active server
                  is inherited automatically.
  --all-servers   Scan every running tmux server and show their
                  sessions together, prefixed with the server name.
  -v, --version   Print version and exit
  --print-config  Print the resolved (merged) config as TOML to stdout and exit.
                  Useful for validating config.toml syntax in CI / pre-commit.
                  Exits 0 on success, 2 on parse error.
  -h, --help      Show this help
`

func main() {
	// Initialize fzf's fuzzy-matching scoring tables. Must be called
	// once before any fuzzyScore / fuzzyMatch call.
	initFuzzy()

	popupSpec := defaultPopupSpec
	popup := os.Getenv("TMUX") != "" && os.Getenv(popupEnv) == ""
	lastSession := false
	visitBack := false
	visitForward := false
	toggle := false
	allServers := false
	printConfig := false
	for _, arg := range os.Args[1:] {
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
		case strings.HasPrefix(arg, "--server="):
			tmuxServer = tmuxServerSpec{flag: "-L", value: strings.TrimPrefix(arg, "--server=")}
		case strings.HasPrefix(arg, "--socket="):
			tmuxServer = tmuxServerSpec{flag: "-S", value: strings.TrimPrefix(arg, "--socket=")}
		case arg == "-v" || arg == "--version":
			fmt.Printf("tmux-qs %s\n", version)
			return
		case arg == "--print-config":
			printConfig = true
		case arg == "-h" || arg == "--help":
			fmt.Print(usage)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n%s", arg, usage)
			os.Exit(2)
		}
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
	} else if tmuxServer.flag == "" {
		setTmuxServerFromEnv()
	}

	// --toggle: if a tmux-qs instance is already running (popup is open),
	// kill it and switch to the last session. Otherwise fall through to
	// the normal popup/TUI flow. Designed for binding to a single key
	// so the first press opens the picker and the second press dismisses
	// it and goes back to where you were.
	if toggle {
		others := otherInstancePIDs()
		if len(others) > 0 {
			for _, pid := range others {
				_ = run("kill", strconv.Itoa(pid))
			}
			if err := lastSessionSwitch(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
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
	// bail out if another instance is already running.
	if name, err := tmuxRunOut("display-message", "-p", "#S"); err == nil && name == "popup" {
		_ = tmuxRun("detach-client")
	}
	// The popup child skips the check: its parent already performed it and
	// may still be alive for a moment.
	if os.Getenv(popupEnv) == "" && instanceCount() > 1 {
		return
	}

	if popup {
		if err := openInPopup(popupSpec); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
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
	restoreLastView := !lastSession && !visitBack && !visitForward && !toggle && !allServers && tmuxServer.flag == ""

	p := tea.NewProgram(newModel(themeWatch, restoreLastView), tea.WithAltScreen(), tea.WithMouseCellMotion())
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
		oldSpec := tmuxServer
		tmuxServer = tmuxServerSpec{flag: "-L", value: resultServer}
		defer func() { tmuxServer = oldSpec }()
	}
	if err := connect(target, paneID, openWithAgent, selectedAgent, resultHintPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func instanceCount() int {
	out, err := runLines("pgrep", "-x", "tmux-qs")
	if err != nil {
		return 1
	}
	return len(out)
}

// otherInstancePIDs returns PIDs of running tmux-qs processes other
// than the current one. Used by --toggle to detect whether a popup
// is already open.
func otherInstancePIDs() []int {
	ourPID := os.Getpid()
	out, err := runLines("pgrep", "-x", "tmux-qs")
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range out {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid == ourPID {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}
