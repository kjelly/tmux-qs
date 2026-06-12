package main

import (
	"os"
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
func popupArgs(spec string) []string {
	var tokens []string
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok != "" && tok != "border-native" {
			tokens = append(tokens, tok)
		}
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
	return append(xy, "-w"+w, "-h"+h)
}

// openInPopup re-runs this binary inside a tmux display-popup.
func openInPopup(spec string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"display-popup", "-E", "-e", popupEnv + "=1"}
	args = append(args, popupArgs(spec)...)
	args = append(args, self, "--no-popup")
	return run("tmux", args...)
}
