package main

import (
	"reflect"
	"testing"
)

// Expected values follow fzf's parseTmuxOptions + runTmux mapping
// (~/github/fzf/src/options.go, src/tmux.go).
func TestPopupArgs(t *testing.T) {
	cases := []struct {
		spec string
		want []string
	}{
		{"top,70%", []string{"-xC", "-y0", "-w100%", "-h70%"}},
		{"bottom,40%", []string{"-xC", "-y9999", "-w100%", "-h40%"}},
		{"left,30%", []string{"-x0", "-yC", "-w30%", "-h100%"}},
		{"right,30%", []string{"-xR", "-yC", "-w30%", "-h100%"}},
		{"center,80%,60%", []string{"-xC", "-yC", "-w80%", "-h60%"}},
		{"top,70%,90%", []string{"-xC", "-y0", "-w70%", "-h90%"}}, // two sizes = WIDTH,HEIGHT
		{"80%", []string{"-xC", "-yC", "-w80%", "-h80%"}},
		{"", []string{"-xC", "-yC", "-w50%", "-h50%"}},
		{"up,70%", []string{"-xC", "-y0", "-w100%", "-h70%"}},
		{"top,70%,border-native", []string{"-xC", "-y0", "-w100%", "-h70%", "-B"}},
		{"center,border-native", []string{"-xC", "-yC", "-w50%", "-h50%", "-B"}},
		{"border-native", []string{"-xC", "-yC", "-w50%", "-h50%", "-B"}},
	}
	for _, c := range cases {
		if got := popupArgs(c.spec); !reflect.DeepEqual(got, c.want) {
			t.Errorf("popupArgs(%q) = %v, want %v", c.spec, got, c.want)
		}
	}
}
