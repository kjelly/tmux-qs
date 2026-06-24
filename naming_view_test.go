package main

import (
	"strings"
	"testing"
)

// TestViewDisambiguatesCollidingSessions verifies that the picker
// appends a path fragment to rows whose directory basename appears
// in more than one tmux session, so the user can tell them apart.
// Non-colliding sessions must keep their clean single-line layout.
func TestViewDisambiguatesCollidingSessions(t *testing.T) {
	m := newModel()
	m.items = []string{"foo", "work-foo", "bar"}
	m.filtered = []int{0, 1, 2}
	m.sessionInfo = map[string]sessionInfo{
		"foo":      {path: "/home/u/work/foo", windows: 1, panes: 1},
		"work-foo": {path: "/home/u/personal/foo", windows: 1, panes: 1},
		"bar":      {path: "/home/u/work/bar", windows: 1, panes: 1},
	}
	m.sessionPaths = map[string]string{
		"foo":      "/home/u/work/foo",
		"work-foo": "/home/u/personal/foo",
		"bar":      "/home/u/work/bar",
	}
	m.dupBasenames = findDuplicateBasenames(m.sessionInfo)
	show := true
	m.resolvedCfg.Naming.ShowPathWhenDuplicate = &show

	view := m.View()

	// The "foo" row should carry its parent path fragment.
	if !strings.Contains(view, "work/foo") {
		t.Errorf("foo row should be disambiguated; view missing 'work/foo' fragment:\n%s", view)
	}
	// The "work-foo" row (cwd /home/u/personal/foo) should carry "personal/foo".
	if !strings.Contains(view, "personal/foo") {
		t.Errorf("work-foo row should be disambiguated with 'personal/foo'; view missing it:\n%s", view)
	}
	// The "bar" row is non-colliding; it must NOT carry a path fragment.
	// Find the line containing "bar" and verify it has no "work/bar" suffix.
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "bar") && !strings.Contains(line, "foobar") {
			if strings.Contains(line, "work/bar") {
				t.Errorf("non-colliding 'bar' row should NOT carry a path fragment; line=%q", line)
			}
		}
	}
}

func TestViewDoesNotDisambiguateWhenDisabled(t *testing.T) {
	m := newModel()
	m.items = []string{"foo", "foo-1"}
	m.filtered = []int{0, 1}
	m.sessionInfo = map[string]sessionInfo{
		"foo":   {path: "/home/u/work/foo", windows: 1, panes: 1},
		"foo-1": {path: "/home/u/personal/foo", windows: 1, panes: 1},
	}
	m.sessionPaths = map[string]string{
		"foo":   "/home/u/work/foo",
		"foo-1": "/home/u/personal/foo",
	}
	m.dupBasenames = findDuplicateBasenames(m.sessionInfo)
	no := false
	m.resolvedCfg.Naming.ShowPathWhenDuplicate = &no

	view := m.View()
	if strings.Contains(view, "work/foo") {
		t.Errorf("show_path_when_duplicate = false should suppress path fragments; view:\n%s", view)
	}
}
