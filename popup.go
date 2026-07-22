package main

import (
	"os"
	"os/exec"
	"strings"
)

const (
	popupEnv       = "TMUX_QS_POPUP"
	popupClientEnv = "TMUX_QS_CLIENT"
	popupPaneEnv   = "TMUX_QS_CALLER_PANE"
	popupCwdEnv    = "TMUX_QS_CALLER_CWD"
)

// popupContext is captured before display-popup starts. A popup is not a
// normal pane, so actions that need to address the invoking pane or client
// must not try to rediscover them after the child TUI has started.
type popupContext struct {
	client string
	pane   string
	cwd    string
}

func currentPopupContext() popupContext {
	envCtx := popupContext{
		client: strings.TrimSpace(os.Getenv(popupClientEnv)),
		pane:   strings.TrimSpace(os.Getenv(popupPaneEnv)),
		cwd:    strings.TrimSpace(os.Getenv(popupCwdEnv)),
	}
	if envCtx.client != "" && envCtx.pane != "" && envCtx.cwd != "" {
		return envCtx
	}

	const format = "#{client_name}\t#{pane_id}\t#{pane_current_path}"
	out, err := tmuxRunOut("display-message", "-p", format)
	if err != nil {
		return envCtx
	}
	ctx := parsePopupContext(out)
	if envCtx.client != "" {
		ctx.client = envCtx.client
	}
	if envCtx.pane != "" {
		ctx.pane = envCtx.pane
	}
	if envCtx.cwd != "" {
		ctx.cwd = envCtx.cwd
	}
	return ctx
}

func parsePopupContext(out string) popupContext {
	fields := strings.SplitN(out, "\t", 3)
	if len(fields) != 3 {
		return popupContext{}
	}
	return popupContext{
		client: strings.TrimSpace(fields[0]),
		pane:   strings.TrimSpace(fields[1]),
		cwd:    strings.TrimSpace(fields[2]),
	}
}

func currentPopupClient() string {
	if client := strings.TrimSpace(os.Getenv(popupClientEnv)); client != "" {
		return client
	}
	return currentPopupContext().client
}

// popupArgs translates an fzf --popup style spec into tmux display-popup
// arguments, following fzf's parseTmuxOptions/runTmux semantics:
//
//	[center|top|bottom|left|right][,SIZE[%]][,SIZE[%]][,border-native]
//
// One size: top/bottom -> height, left/right -> width, center -> both.
// Two sizes: always WIDTH,HEIGHT regardless of position.
// top/bottom default to full width; left/right default to full height.
//
// `border-native` keeps tmux's native border. tmux's `-B` means the opposite
// (no border), so this token intentionally does not add a flag. The default
// also keeps the native border so existing tmux-qs users do not see a visual
// regression when upgrading.
func popupArgs(spec string) []string {
	var tokens []string
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if tok == "border-native" {
			continue
		}
		tokens = append(tokens, tok)
	}

	pos := "center"
	w, h := "50%", "50%"
	if len(tokens) > 0 {
		switch tokens[0] {
		case "top", "up":
			pos, w = "top", "100%"
		case "bottom", "down":
			pos, w = "bottom", "100%"
		case "left":
			pos, h = "left", "100%"
		case "right":
			pos, h = "right", "100%"
		case "center":
			pos = "center"
		default:
			// first token is a size; position defaults to center
			tokens = append([]string{"center"}, tokens...)
		}
		sizes := tokens[1:]
		switch len(sizes) {
		case 1:
			switch pos {
			case "top", "bottom":
				h = sizes[0]
			case "left", "right":
				w = sizes[0]
			default:
				w, h = sizes[0], sizes[0]
			}
		case 2:
			w, h = sizes[0], sizes[1]
		}
	}

	var xy []string
	switch pos {
	case "top":
		xy = []string{"-xC", "-y0"}
	case "bottom":
		xy = []string{"-xC", "-y9999"}
	case "left":
		xy = []string{"-x0", "-yC"}
	case "right":
		xy = []string{"-xR", "-yC"}
	default:
		xy = []string{"-xC", "-yC"}
	}
	args := append(xy, "-w"+w, "-h"+h)
	return args
}

func openInPopup(spec string, openSnippets bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// Build the full tmux command: prepend -L/-S if the user
	// requested a non-default server, so the popup child talks to
	// the same server.
	ctx := currentPopupContext()
	full := []string{"tmux"}
	full = append(full, tmuxArgs()...)
	full = append(full, popupCommandArgs(ctx, spec, self, openSnippets)...)
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func popupCommandArgs(ctx popupContext, spec, self string, openSnippets bool) []string {
	args := []string{"display-popup", "-E", "-e", popupEnv + "=1"}
	if ctx.client != "" {
		args = append(args, "-c", ctx.client, "-e", popupClientEnv+"="+ctx.client)
	}
	if ctx.pane != "" {
		args = append(args, "-t", ctx.pane, "-e", popupPaneEnv+"="+ctx.pane)
	}
	if ctx.cwd != "" {
		args = append(args, "-d", ctx.cwd, "-e", popupCwdEnv+"="+ctx.cwd)
	}
	if server := getTmuxServer(); server.flag != "" && server.value != "" {
		args = append(args, "-e", "TMUX_QS_SERVER="+server.flag+"="+server.value)
	}
	args = append(args, "-T", " tmux-qs ")
	args = append(args, popupArgs(spec)...)
	args = append(args, self, "--no-popup")
	if vimEnabled {
		args = append(args, "--vim")
	}
	if openSnippets {
		args = append(args, "--snippets")
	}
	return args
}
