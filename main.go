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
  -h, --help      Show this help
`

func main() {
	popupSpec := defaultPopupSpec
	popup := os.Getenv("TMUX") != "" && os.Getenv(popupEnv) == ""
	lastSession := false
	visitBack := false
	visitForward := false
	toggle := false
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
		case arg == "-h" || arg == "--help":
			fmt.Print(usage)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n%s", arg, usage)
			os.Exit(2)
		}
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
	if name, err := runOut("tmux", "display-message", "-p", "#S"); err == nil && name == "popup" {
		_ = run("tmux", "detach-client")
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

	p := tea.NewProgram(newModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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
	if target == "" {
		return
	}
	if err := connect(target, paneID, openWithAgent, selectedAgent); err != nil {
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
