package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

	// pane_id is never empty, so it goes last: a new pane has no
	// pane_current_path until its child takes the terminal, and trimming
	// the output would drop that empty trailing field.
	const format = "#{client_name}\t#{pane_current_path}\t#{pane_id}"
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

// parsePopupContext splits on the first and last tab, so a tab inside the
// path stays in cwd.
func parsePopupContext(out string) popupContext {
	client, rest, ok := strings.Cut(out, "\t")
	i := strings.LastIndex(rest, "\t")
	if !ok || i < 0 {
		return popupContext{}
	}
	return popupContext{
		client: strings.TrimSpace(client),
		pane:   strings.TrimSpace(rest[i+1:]),
		cwd:    strings.TrimSpace(rest[:i]),
	}
}

func currentPopupClient() string {
	if client := strings.TrimSpace(os.Getenv(popupClientEnv)); client != "" {
		return client
	}
	return currentPopupContext().client
}

type pickerGeometry struct {
	Position string // center, top, bottom, left, right
	Width    string
	Height   string
	Border   bool
}

// parseGeometrySpec translates an fzf --popup style spec into a normalized
// pickerGeometry structure following fzf's parseTmuxOptions/runTmux semantics:
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
func parseGeometrySpec(spec string) pickerGeometry {
	var tokens []string
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" || tok == "border-native" {
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

	return pickerGeometry{
		Position: pos,
		Width:    w,
		Height:   h,
		Border:   true,
	}
}

func (g pickerGeometry) popupArgs() []string {
	var xy []string
	switch g.Position {
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
	return []string{xy[0], xy[1], "-w" + g.Width, "-h" + g.Height}
}

func popupArgs(spec string) []string {
	return parseGeometrySpec(spec).popupArgs()
}

func parseDimension(dim string, total int) (int, error) {
	dim = strings.TrimSpace(dim)
	if strings.HasSuffix(dim, "%") {
		pctStr := strings.TrimSuffix(dim, "%")
		pct, err := strconv.Atoi(pctStr)
		if err != nil || pct <= 0 {
			return 0, fmt.Errorf("invalid percentage %q", dim)
		}
		return (total * pct) / 100, nil
	}
	cells, err := strconv.Atoi(dim)
	if err != nil || cells <= 0 {
		return 0, fmt.Errorf("invalid cells %q", dim)
	}
	return cells, nil
}

func (g pickerGeometry) floatingArgs(windowWidth, windowHeight int) ([]string, error) {
	if windowWidth <= 0 || windowHeight <= 0 {
		return nil, fmt.Errorf("invalid window dimensions: %dx%d", windowWidth, windowHeight)
	}
	wVal, err := parseDimension(g.Width, windowWidth)
	if err != nil {
		return nil, err
	}
	hVal, err := parseDimension(g.Height, windowHeight)
	if err != nil {
		return nil, err
	}
	w := max(1, min(windowWidth, wVal))
	h := max(1, min(windowHeight, hVal))

	var X, Y int
	switch g.Position {
	case "top":
		X = (windowWidth - w) / 2
		Y = 0
	case "bottom":
		X = (windowWidth - w) / 2
		Y = windowHeight - h
	case "left":
		X = 0
		Y = (windowHeight - h) / 2
	case "right":
		X = windowWidth - w
		Y = (windowHeight - h) / 2
	default: // center
		X = (windowWidth - w) / 2
		Y = (windowHeight - h) / 2
	}
	if X < 0 {
		X = 0
	}
	if Y < 0 {
		Y = 0
	}
	return []string{"-x", strconv.Itoa(w), "-y", strconv.Itoa(h), "-X", strconv.Itoa(X), "-Y", strconv.Itoa(Y)}, nil
}

func openInPopup(spec string, openSnippets, allServers bool) error {
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
	full = append(full, popupCommandArgs(ctx, spec, self, openSnippets, allServers)...)
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func popupCommandArgs(ctx popupContext, spec, self string, openSnippets, allServers bool) []string {
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
	if allServers {
		args = append(args, "--all-servers")
	}
	return args
}
