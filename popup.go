package main

import (
	"os"
	"os/exec"
	"strings"
)

const popupEnv = "TMUX_QS_POPUP"

// popupArgs translates an fzf --popup style spec into tmux display-popup
// arguments, following fzf's parseTmuxOptions/runTmux semantics:
//
//	[center|top|bottom|left|right][,SIZE[%]][,SIZE[%]][,border-native]
//
// One size: top/bottom -> height, left/right -> width, center -> both.
// Two sizes: always WIDTH,HEIGHT regardless of position.
// top/bottom default to full width; left/right default to full height.
//
// `border-native` is translated into tmux's `-B` flag (tmux 3.3+),
// which draws a native terminal border around the popup. Older tmux
// versions ignore the flag, so the worst case is silently no border.
func popupArgs(spec string) []string {
	var tokens []string
	border := false
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if tok == "border-native" {
			border = true
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
	if border {
		args = append(args, "-B")
	}
	return args
}

func openInPopup(spec string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// Build the full tmux command: prepend -L/-S if the user
	// requested a non-default server, so the popup child talks to
	// the same server.
	full := []string{"tmux"}
	full = append(full, tmuxArgs()...)
	full = append(full, "display-popup", "-E", "-e", popupEnv+"=1")
	full = append(full, popupArgs(spec)...)
	full = append(full, self, "--no-popup")
	// Also propagate the server spec to the child via an env var so
	// the child can re-infer it (since the child runs in a new
	// tmux client and won't see the original TMUX env var).
	if tmuxServer.flag != "" {
		envVar := "TMUX_QS_SERVER=" + tmuxServer.flag + "=" + tmuxServer.value
		cmd := exec.Command(full[0], full[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(), envVar)
		return cmd.Run()
	}
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
