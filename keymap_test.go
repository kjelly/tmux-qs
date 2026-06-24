package main

import "testing"

func TestBuildKeymapRebindAndFree(t *testing.T) {
	cfg := Config{Keybindings: map[string]string{
		"waiting": "ctrl+1", // move waiting off ctrl+w
	}}
	km := buildKeymap(cfg)
	if km["ctrl+1"] != "ctrl+w" {
		t.Errorf("ctrl+1 should map to canonical ctrl+w, got %q", km["ctrl+1"])
	}
	if km["ctrl+w"] != keyDisabled {
		t.Errorf("freed default ctrl+w should be disabled, got %q", km["ctrl+w"])
	}
}

// When two actions swap keys, neither default should be disabled — each is
// reclaimed by the other binding.
func TestBuildKeymapSwap(t *testing.T) {
	cfg := Config{Keybindings: map[string]string{
		"waiting": "ctrl+b", // waiting -> branch's default key
		"branch":  "ctrl+w", // branch  -> waiting's default key
	}}
	km := buildKeymap(cfg)
	if km["ctrl+b"] != "ctrl+w" {
		t.Errorf("ctrl+b should map to ctrl+w, got %q", km["ctrl+b"])
	}
	if km["ctrl+w"] != "ctrl+b" {
		t.Errorf("ctrl+w should map to ctrl+b, got %q", km["ctrl+w"])
	}
	if km["ctrl+w"] == keyDisabled || km["ctrl+b"] == keyDisabled {
		t.Error("swapped keys should not be disabled")
	}
}

func TestBuildKeymapUnknownActionIgnored(t *testing.T) {
	cfg := Config{Keybindings: map[string]string{"not-an-action": "ctrl+1"}}
	if km := buildKeymap(cfg); len(km) != 0 {
		t.Errorf("unknown action should be ignored, got %v", km)
	}
}

func TestNormalizeKey(t *testing.T) {
	cases := map[string]string{
		"C-w":     "ctrl+w",
		"m-enter": "alt+enter",
		"Ctrl+W":  "ctrl+w",
		"space":   " ",
		"  a-r ":  "alt+r",
	}
	for in, want := range cases {
		if got := normalizeKey(in); got != want {
			t.Errorf("normalizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// remapKey should be the identity when no overrides are configured.
func TestRemapKeyIdentity(t *testing.T) {
	m := model{}
	if got := m.remapKey("ctrl+w"); got != "ctrl+w" {
		t.Errorf("identity remap expected, got %q", got)
	}
}
