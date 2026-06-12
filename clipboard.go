package main

import (
	"encoding/base64"
	"fmt"
	"os"
)

// copyToClipboard writes text to the system clipboard via the OSC 52
// escape sequence:
//
//	ESC ] 52 ; c ; <base64-encoded-text> BEL
//
// The terminal (iTerm2, Alacritty, kitty, WezTerm, modern xterm) is
// responsible for intercepting the sequence and updating the system
// clipboard. Inside tmux, this works as long as the server has
// `set-clipboard on` configured — tmux itself forwards the OSC 52 to
// the outer terminal.
//
// This approach has zero external dependencies: no pbcopy, xclip,
// wl-copy, xsel, or clip.exe binary required. The trade-off is that
// we cannot verify the terminal actually honored the request —
// write-to-stdout success is the best signal we get.
func copyToClipboard(text string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	seq := fmt.Sprintf("\x1b]52;c;%s\x07", encoded)
	_, err := os.Stdout.WriteString(seq)
	return err
}
