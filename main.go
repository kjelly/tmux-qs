package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
  -h, --help      Show this help
`

func main() {
	popupSpec := defaultPopupSpec
	popup := os.Getenv("TMUX") != "" && os.Getenv(popupEnv) == ""
	for _, arg := range os.Args[1:] {
		switch {
		case arg == "--no-popup":
			popup = false
		case arg == "--popup":
			popup = os.Getenv(popupEnv) == ""
		case strings.HasPrefix(arg, "--popup="):
			popup = os.Getenv(popupEnv) == ""
			popupSpec = strings.TrimPrefix(arg, "--popup=")
		case arg == "-h" || arg == "--help":
			fmt.Print(usage)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n%s", arg, usage)
			os.Exit(2)
		}
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

	target := final.(model).result
	paneID := final.(model).resultPaneID
	if target == "" {
		return
	}
	if err := connect(target, paneID); err != nil {
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
