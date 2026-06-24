package main

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
)

func TestInputHistoryAppendDedupAndCap(t *testing.T) {
	withCleanCacheEnv(t)
	var h []string
	h = appendInputHistory(h, "one")
	h = appendInputHistory(h, "two")
	h = appendInputHistory(h, "two") // immediate dup, ignored
	h = appendInputHistory(h, "  ")  // blank, ignored
	if !reflect.DeepEqual(h, []string{"one", "two"}) {
		t.Fatalf("dedup/blank handling wrong: %v", h)
	}
	// Persisted and reloaded identically.
	if got := loadInputHistory(); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("reload mismatch: %v", got)
	}
}

func TestRecallHistory(t *testing.T) {
	m := model{inputHistory: []string{"a", "b", "c"}, historyPos: 3}
	m.input = textinput.New()

	m.recallHistory(-1) // newest
	if m.input.Value() != "c" {
		t.Errorf("first recall should give newest 'c', got %q", m.input.Value())
	}
	m.recallHistory(-1)
	if m.input.Value() != "b" {
		t.Errorf("second recall should give 'b', got %q", m.input.Value())
	}
	m.recallHistory(1) // back toward newest
	if m.input.Value() != "c" {
		t.Errorf("forward recall should give 'c', got %q", m.input.Value())
	}
	m.recallHistory(1) // past newest -> clears
	if m.input.Value() != "" {
		t.Errorf("recall past newest should clear input, got %q", m.input.Value())
	}
}
