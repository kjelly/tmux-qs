package main

import (
	"testing"
)

func TestEntryGroup(t *testing.T) {
	// Save and restore the original config.
	origCfg := loadConfig()
	defer func() {
		configMu.Lock()
		cachedConfig = &cachedConfigEntry{cfg: origCfg, path: configFilePath()}
		configMu.Unlock()
	}()

	// Install a test config with grouped sessions.
	configMu.Lock()
	cachedConfig = &cachedConfigEntry{
		cfg: Config{
			Sessions: []SessionEntry{
				{Name: "work-1", Path: "/tmp", Group: "work"},
				{Name: "work-2", Path: "/tmp", Group: "work"},
				{Name: "personal", Path: "/tmp", Group: "personal"},
				{Name: "nogroup", Path: "/tmp", Group: ""},
			},
		},
	}
	configMu.Unlock()

	cases := []struct {
		entry string
		want  string
	}{
		{"work-1", "work"},
		{"work-2", "work"},
		{"personal", "personal"},
		{"nogroup", ""},
		{"unknown", ""},
	}
	for _, c := range cases {
		got := entryGroup(c.entry)
		if got != c.want {
			t.Errorf("entryGroup(%q) = %q, want %q", c.entry, got, c.want)
		}
	}
}

func TestAllGroups(t *testing.T) {
	// Save and restore the original config.
	origCfg := loadConfig()
	defer func() {
		configMu.Lock()
		cachedConfig = &cachedConfigEntry{cfg: origCfg, path: configFilePath()}
		configMu.Unlock()
	}()

	// Empty config → no groups.
	configMu.Lock()
	cachedConfig = &cachedConfigEntry{cfg: Config{}}
	configMu.Unlock()
	if got := allGroups(); got != nil {
		t.Errorf("allGroups() with no sessions = %v, want nil", got)
	}

	// With grouped and ungrouped sessions.
	configMu.Lock()
	cachedConfig = &cachedConfigEntry{
		cfg: Config{
			Sessions: []SessionEntry{
				{Name: "a", Group: "z"},
				{Name: "b", Group: "a"},
				{Name: "c", Group: "z"}, // duplicate
				{Name: "d", Group: ""},  // ungrouped
			},
		},
	}
	configMu.Unlock()
	got := allGroups()
	if len(got) != 2 || got[0] != "a" || got[1] != "z" {
		t.Errorf("allGroups() = %v, want [a z]", got)
	}
}
