package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type themeClient struct {
	tty     string
	session string
	width   int
	light   bool
}

type tmuxThemePalette struct {
	osc                   string
	statusStyle           string
	windowCurrentStyle    string
	paneBorderStyle       string
	paneActiveBorderStyle string
	modeStyle             string
	messageStyle          string
}

var (
	lightTmuxTheme = tmuxThemePalette{
		osc:                   "\033]11;#ffffff\007\033]10;#000000\007",
		statusStyle:           "fg=#000000,bg=#ffffff",
		windowCurrentStyle:    "fg=#000000,bg=#ffffff,bold,reverse",
		paneBorderStyle:       "fg=#888888",
		paneActiveBorderStyle: "fg=#000000,bold",
		modeStyle:             "fg=#ffffff,bg=#000000",
		messageStyle:          "fg=#000000,bg=#ffffff,bold",
	}
	darkTmuxTheme = tmuxThemePalette{
		osc:                   "\033]11;#171421\007\033]10;#d0cfcc\007",
		statusStyle:           "fg=#d0cfcc,bg=#383838",
		windowCurrentStyle:    "fg=#d0cfcc,bg=#383838,bold,reverse",
		paneBorderStyle:       "fg=#383838",
		paneActiveBorderStyle: "fg=#d0cfcc,bold",
		modeStyle:             "fg=#171421,bg=#d0cfcc",
		messageStyle:          "fg=#d0cfcc,bg=#383838,bold",
	}
)

func parseThemeClient(line string, widths map[int]struct{}) (themeClient, error) {
	parts := strings.Split(line, "\t")
	if len(parts) != 3 {
		return themeClient{}, fmt.Errorf("invalid tmux client row %q", line)
	}
	width, err := strconv.Atoi(parts[1])
	if err != nil {
		return themeClient{}, fmt.Errorf("invalid tmux client width %q: %w", parts[1], err)
	}
	return themeClient{
		tty:     parts[0],
		width:   width,
		session: parts[2],
		light:   isEinkWidth(width, widths),
	}, nil
}

func ensureEinkWidthsOption() (map[int]struct{}, error) {
	raw, err := tmuxRunOut("show-options", "-gv", "@eink-widths")
	if err == nil {
		if widths := parseConfiguredEinkWidths(raw); len(widths) > 0 {
			return widths, nil
		}
	}
	if err := tmuxRun("set-option", "-g", "@eink-widths", defaultEinkWidths); err != nil {
		return nil, err
	}
	return parseConfiguredEinkWidths(defaultEinkWidths), nil
}

func writeThemeOSC(tty, sequence string) error {
	f, err := os.OpenFile(tty, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(sequence)
	return err
}

func applyThemeClient(client themeClient) error {
	palette := darkTmuxTheme
	if client.light {
		palette = lightTmuxTheme
	}

	var errs []error
	if err := writeThemeOSC(client.tty, palette.osc); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", client.tty, err))
	}
	for _, option := range []struct {
		name, value string
	}{
		{"status-style", palette.statusStyle},
		{"window-status-current-style", palette.windowCurrentStyle},
		{"pane-border-style", palette.paneBorderStyle},
		{"pane-active-border-style", palette.paneActiveBorderStyle},
		{"mode-style", palette.modeStyle},
		{"message-style", palette.messageStyle},
	} {
		if err := tmuxRun("set-option", "-t", client.session, option.name, option.value); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func applyTmuxTheme() error {
	widths, err := ensureEinkWidthsOption()
	if err != nil {
		return err
	}
	if err := tmuxRun("set-option", "-g", "window-style", "default"); err != nil {
		return err
	}
	if err := tmuxRun("set-option", "-g", "window-active-style", "default"); err != nil {
		return err
	}

	rows, err := tmuxRunLines("list-clients", "-F", "#{client_tty}\t#{client_width}\t#{session_name}")
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range rows {
		client, err := parseThemeClient(row, widths)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := applyThemeClient(client); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func runThemeSubcommand(args []string) (handled bool, err error) {
	if len(args) == 0 {
		return false, nil
	}
	if args[0] == "eink" {
		setTmuxServerFromEnv()
		if err := autoForceEinkClient(); err != nil {
			return true, fmt.Errorf("cannot auto-force e-ink client: %w", err)
		}
		return true, runEinkWidthsCommand(args[1:])
	}
	if args[0] != "theme" {
		return false, nil
	}
	setTmuxServerFromEnv()
	if err := autoForceEinkClient(); err != nil {
		return true, fmt.Errorf("cannot auto-force e-ink client: %w", err)
	}
	if len(args) == 2 && args[1] == "apply" {
		return true, applyTmuxTheme()
	}
	return true, errors.New("usage: tmux-qs theme apply | tmux-qs eink [list|set WIDTHS|add [WIDTH]|remove [WIDTH]|reset]")
}

// runEinkWidthsCommand manages the global tmux option used by theme apply.
// It is deliberately non-interactive so hooks and scripts never open the TUI.
func runEinkWidthsCommand(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "force":
			if len(args) != 1 {
				return errors.New("usage: tmux-qs eink force")
			}
			return setEinkClientForced(true)
		case "unforce":
			if len(args) != 1 {
				return errors.New("usage: tmux-qs eink unforce")
			}
			return setEinkClientForced(false)
		case "status":
			if len(args) != 1 {
				return errors.New("usage: tmux-qs eink status")
			}
			if einkClientForced() {
				fmt.Println("forced")
			} else {
				fmt.Println("auto")
			}
			return nil
		}
	}
	current := loadEinkWidths()
	if len(args) == 0 {
		width, err := currentTmuxClientWidth()
		if err != nil {
			return fmt.Errorf("cannot determine current tmux client width: %w", err)
		}
		if width <= 0 {
			return fmt.Errorf("invalid current tmux client width %d", width)
		}
		current[width] = struct{}{}
		return writeEinkWidths(current)
	}
	if args[0] == "list" {
		if len(args) != 1 {
			return errors.New("usage: tmux-qs eink list")
		}
		fmt.Println(formatEinkWidths(current))
		return nil
	}

	var next map[int]struct{}
	switch args[0] {
	case "set":
		if len(args) != 2 {
			return errors.New("usage: tmux-qs eink set WIDTHS (for example 167,165)")
		}
		var err error
		next, err = parseEinkWidthSetting(args[1])
		if err != nil {
			return err
		}
	case "add", "remove":
		if len(args) > 2 {
			return fmt.Errorf("usage: tmux-qs eink %s [WIDTH]", args[0])
		}
		width := 0
		if len(args) == 1 {
			var err error
			width, err = currentTmuxClientWidth()
			if err != nil {
				return fmt.Errorf("cannot determine current tmux client width: %w", err)
			}
		} else {
			widths, err := parseEinkWidthSetting(args[1])
			if err != nil || len(widths) != 1 {
				return fmt.Errorf("%s requires one positive integer width", args[0])
			}
			for value := range widths {
				width = value
			}
		}
		if width <= 0 {
			return fmt.Errorf("invalid current tmux client width %d", width)
		}
		next = make(map[int]struct{}, len(current)+1)
		for value := range current {
			next[value] = struct{}{}
		}
		if args[0] == "add" {
			next[width] = struct{}{}
		} else {
			delete(next, width)
			if len(next) == 0 {
				return errors.New("cannot remove the last e-ink width")
			}
		}
	case "reset":
		if len(args) != 1 {
			return errors.New("usage: tmux-qs eink reset")
		}
		next = parseConfiguredEinkWidths(defaultEinkWidths)
	default:
		return errors.New("usage: tmux-qs eink [list|set WIDTHS|add [WIDTH]|remove [WIDTH]|reset]")
	}

	return writeEinkWidths(next)
}

func writeEinkWidths(widths map[int]struct{}) error {
	value := formatEinkWidths(widths)
	if err := tmuxRun("set-option", "-g", "@eink-widths", value); err != nil {
		return err
	}
	fmt.Println(value)
	return nil
}
