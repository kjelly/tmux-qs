package main

import "testing"

func TestSourceTabAtMapsVisibleTabs(t *testing.T) {
	pos := 2
	for _, tab := range sourceTabs {
		got, ok := sourceTabAt(pos + 1)
		if !ok || got != tab.src {
			t.Errorf("sourceTabAt(%d) = %v, %v; want %v, true", pos+1, got, ok, tab.src)
		}
		pos += len([]rune(sourceTabText(tab))) + 1
	}
}

func TestSourceTabAtIgnoresSeparators(t *testing.T) {
	if _, ok := sourceTabAt(0); ok {
		t.Fatal("leading padding should not activate a source tab")
	}
}
