package main

import (
	"log"
	"sort"
	"strings"
)

// Keybindings are configurable via the [keybindings] config table, which
// maps an action name to a key. Internally the dispatch in keybindings.go
// still switches on a fixed canonical key per action (e.g. the "waiting"
// action is the "ctrl+w" case). To support rebinding without rewriting
// that switch, we translate the *pressed* key to its canonical key before
// the switch runs (see model.remapKey): a custom binding maps the new key
// onto the canonical one, and the freed default key is disabled.

// keyDisabled is the sentinel a remapped-away default key translates to.
// It matches no case in handleKey, so the key becomes inert (its KeyMsg
// falls through to the textinput, which ignores control/alt chords).
const keyDisabled = "\x00disabled"

// defaultActionKeys maps each rebindable action name to the canonical key
// the handleKey switch dispatches on. Only actions listed here can be
// rebound; core navigation (up/down/enter/esc/help/quit) is intentionally
// fixed so a bad config can't make the picker unusable.
var defaultActionKeys = map[string]string{
	// source switches
	"all":         "ctrl+a",
	"tmux":        "ctrl+t",
	"configs":     "ctrl+g",
	"zoxide":      "ctrl+x",
	"zoxide-root": "alt+r",
	"find":        "ctrl+f",
	"panes":       "ctrl+e",
	"windows":     "ctrl+v",
	"ssh":         "ctrl+h",
	"commands":    "ctrl+o",
	"waiting":     "ctrl+w",
	"cleanup":     "alt+c",
	// operations
	"copy":         "ctrl+y",
	"rename":       "ctrl+r",
	"kill":         "ctrl+d",
	"branch":       "ctrl+b",
	"pin":          "alt+i",
	"agent":        "alt+v",
	"template":     "alt+t",
	"files":        "alt+f",
	"new-session":  "alt+m",
	"open-remote":  "alt+o",
	"send":         "alt+enter",
	"toggle-close": "alt+q",
	"tag-filter":   "ctrl+,",
	"group-filter": "ctrl+;",
	"detail":       "ctrl+@",
	"undo":         "alt+u",
	"jump-next":    "alt+j",
	"jump-prev":    "alt+k",
	"visit-back":   "alt+left",
	"visit-fwd":    "alt+right",
	"preview-up":   "alt+up",
	"preview-down": "alt+down",
}

// buildKeymap turns the user's [keybindings] action→key overrides into a
// pressed-key → canonical-key translation table. Unknown action names are
// warned about and ignored. A default key that has been reassigned to a
// different key (and not itself claimed as someone's custom key) is mapped
// to keyDisabled so it no longer triggers its old action.
func buildKeymap(cfg Config) map[string]string {
	if len(cfg.Keybindings) == 0 {
		return nil
	}
	remap := map[string]string{}
	var freed []string
	for action, key := range cfg.Keybindings {
		def, ok := defaultActionKeys[action]
		if !ok {
			log.Printf("tmux-qs: unknown keybinding action %q (ignored)", action)
			continue
		}
		key = normalizeKey(key)
		if key == "" || key == def {
			continue
		}
		remap[key] = def
		freed = append(freed, def)
	}
	// Disable freed default keys unless some other binding re-claimed them.
	sort.Strings(freed)
	for _, def := range freed {
		if _, claimed := remap[def]; !claimed {
			remap[def] = keyDisabled
		}
	}
	if len(remap) == 0 {
		return nil
	}
	return remap
}

// remapKey translates a pressed key through the model's keymap. Returns
// the key unchanged when there is no override.
func (m model) remapKey(pressed string) string {
	if m.keymap == nil {
		return pressed
	}
	if canon, ok := m.keymap[pressed]; ok {
		return canon
	}
	return pressed
}

// normalizeKey canonicalizes a user-supplied key string into Bubble Tea's
// notation: lowercased, with a few friendly prefix aliases (c-/m-/a-/s-)
// and "space" accepted.
func normalizeKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return ""
	}
	if k == "space" {
		return " "
	}
	// Translate prefix aliases on the final chord segment, e.g. "c-w",
	// "m-enter". Bubble Tea expects "ctrl+"/"alt+"/"shift+".
	repl := strings.NewReplacer(
		"ctrl-", "ctrl+",
		"c-", "ctrl+",
		"alt-", "alt+",
		"meta-", "alt+",
		"m-", "alt+",
		"a-", "alt+",
		"shift-", "shift+",
		"s-", "shift+",
	)
	return repl.Replace(k)
}
