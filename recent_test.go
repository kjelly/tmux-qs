package main

import (
	"reflect"
	"testing"
	"time"
)

func TestRecentOrderedByRecency(t *testing.T) {
	now := time.Now()
	f := recentFile{entries: map[string]int64{
		"hot":  now.Unix() - 10,    // most recent
		"warm": now.Unix() - 100,   // less recent
		"cold": now.Unix() - 10000, // very old
		"unknown1": 0,
		"unknown2": 0,
	}}
	in := []string{"unknown1", "cold", "warm", "unknown2", "hot"}
	got := f.orderedByRecency(in)
	want := []string{"hot", "warm", "cold", "unknown1", "unknown2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orderedByRecency mismatch:\n got %v\nwant %v", got, want)
	}
}

func TestRecentRecordTouch(t *testing.T) {
	f := recentFile{entries: map[string]int64{}}
	f = f.recordTouch("a")
	if f.entries["a"] == 0 {
		t.Errorf("recordTouch should set a non-zero timestamp, got %d", f.entries["a"])
	}
}

func TestRecentEmpty(t *testing.T) {
	f := recentFile{entries: nil}
	got := f.orderedByRecency([]string{"x", "y"})
	if !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Errorf("empty recency should preserve order, got %v", got)
	}
}

func TestRecentLoadSaveRoundtrip(t *testing.T) {
	withCleanCacheEnv(t)
	original := recentFile{entries: map[string]int64{"a": 123, "b": 456}}
	original.save()

	loaded := loadRecent()
	if loaded.entries["a"] != 123 || loaded.entries["b"] != 456 {
		t.Errorf("loadRecent mismatch: got %+v", loaded.entries)
	}
}
