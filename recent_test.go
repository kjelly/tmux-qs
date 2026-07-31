package main

import (
	"reflect"
	"testing"
	"time"
)

func TestRecentOrderedByFrecency(t *testing.T) {
	nowT := time.Now()
	f := recentFile{entries: map[string]recentEntry{
		"hot":      {Count: 1, Last: nowT.Unix() - 10},    // most recent
		"warm":     {Count: 1, Last: nowT.Unix() - 100},   // less recent
		"cold":     {Count: 1, Last: nowT.Unix() - 10000}, // very old (older bucket)
		"unknown1": {Count: 0, Last: 0},
		"unknown2": {Count: 0, Last: 0},
	}}
	in := []string{"unknown1", "cold", "warm", "unknown2", "hot"}
	got := f.orderedByFrecency(in)
	want := []string{"hot", "warm", "cold", "unknown1", "unknown2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orderedByFrecency mismatch:\n got %v\nwant %v", got, want)
	}
}

// A frequently-chosen entry should outrank a more-recent but rarely
// chosen one once both fall in the same recency bucket.
func TestFrecencyFrequencyWins(t *testing.T) {
	nowUnix := time.Now().Unix()
	freq := recentEntry{Count: 10, Last: nowUnix - 200}
	rare := recentEntry{Count: 1, Last: nowUnix - 100}
	if frecencyScore(freq, nowUnix) <= frecencyScore(rare, nowUnix) {
		t.Errorf("frequent entry should outscore rare one: freq=%v rare=%v",
			frecencyScore(freq, nowUnix), frecencyScore(rare, nowUnix))
	}
}

// Recency buckets: a very recent single hit should beat an old single hit.
func TestFrecencyRecencyBuckets(t *testing.T) {
	nowUnix := time.Now().Unix()
	recent := recentEntry{Count: 1, Last: nowUnix - 10}     // <1h ×4
	old := recentEntry{Count: 1, Last: nowUnix - 1_000_000} // older ×0.25
	if frecencyScore(recent, nowUnix) <= frecencyScore(old, nowUnix) {
		t.Errorf("recent hit should outscore old hit")
	}
}

func TestRecentRecordTouch(t *testing.T) {
	f := recentFile{entries: map[string]recentEntry{}}
	f = f.recordTouch("a")
	if f.entries["a"].Last == 0 {
		t.Errorf("recordTouch should set a non-zero timestamp, got %d", f.entries["a"].Last)
	}
	if f.entries["a"].Count != 1 {
		t.Errorf("recordTouch should set count to 1, got %d", f.entries["a"].Count)
	}
	f = f.recordTouch("a")
	if f.entries["a"].Count != 2 {
		t.Errorf("second recordTouch should bump count to 2, got %d", f.entries["a"].Count)
	}
}

func TestRecentEmpty(t *testing.T) {
	f := recentFile{entries: nil}
	got := f.orderedByFrecency([]string{"x", "y"})
	if !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Errorf("empty recency should preserve order, got %v", got)
	}
}

func TestRecentLoadSaveRoundtrip(t *testing.T) {
	withCleanCacheEnv(t)
	original := recentFile{entries: map[string]recentEntry{
		"a": {Count: 3, Last: 123},
		"b": {Count: 1, Last: 456},
	}}
	original.save()

	loaded := loadRecent()
	if loaded.entries["a"].Count != 3 || loaded.entries["a"].Last != 123 {
		t.Errorf("loadRecent mismatch for a: got %+v", loaded.entries["a"])
	}
	if loaded.entries["b"].Last != 456 {
		t.Errorf("loadRecent mismatch for b: got %+v", loaded.entries["b"])
	}
}

// TestRecencyOfTmuxSessionActivityWins verifies that for a running
// tmux session, #{session_activity} (i.e. sessionMeta.lastActive) is
// the primary recency signal — even when recent.json has a more
// recent (or older) record. The tmux-native timestamp captures
// in-pane activity that recent.json alone can't.
func TestRecencyOfTmuxSessionActivityWins(t *testing.T) {
	nowT := time.Now()
	// session is 5 minutes old in tmux activity, but recent.json
	// says it was just touched (e.g. another picker instance).
	// The tmux value should win because the user is currently
	// active inside the session.
	info := map[string]sessionInfo{
		"alpha": {meta: sessionMeta{lastActive: nowT.Add(-5 * time.Minute), hasLastAct: true}},
	}
	recent := recentFile{entries: map[string]recentEntry{
		"alpha": {Count: 1, Last: nowT.Unix()},
	}}
	rec, ok := recencyOf("alpha", info, recent, nil)
	if !ok {
		t.Fatal("alpha should have a recency signal")
	}
	if rec != nowT.Add(-5*time.Minute).Unix() {
		t.Errorf("expected tmux session_activity to win, got %d (want %d)",
			rec, nowT.Add(-5*time.Minute).Unix())
	}
}

// TestRecencyOfVisitStackWinsOverSessionActivity verifies that a
// session's position in the client's visit stack outranks
// #{session_activity}, even when the other session's activity
// timestamp is far newer. This is the fix for background agent
// output (which keeps bumping session_activity in a session the
// user hasn't actually switched to) displacing the session the
// user genuinely picked most recently.
func TestRecencyOfVisitStackWinsOverSessionActivity(t *testing.T) {
	nowT := time.Now()
	info := map[string]sessionInfo{
		// "busy" has much fresher tmux activity (an unattended agent
		// keeps typing into it) than "picked", which the user
		// actually switched to via tmux-qs a while ago.
		"busy":   {meta: sessionMeta{lastActive: nowT, hasLastAct: true}},
		"picked": {meta: sessionMeta{lastActive: nowT.Add(-2 * time.Hour), hasLastAct: true}},
	}
	recent := recentFile{}
	visits := []string{"picked", "busy"} // "picked" switched to more recently
	recPicked, okPicked := recencyOf("picked", info, recent, visits)
	recBusy, okBusy := recencyOf("busy", info, recent, visits)
	if !okPicked || !okBusy {
		t.Fatalf("both entries should have a recency signal: okPicked=%v okBusy=%v", okPicked, okBusy)
	}
	if recPicked <= recBusy {
		t.Errorf("visit-stack rank should outrank session_activity: picked=%d busy=%d", recPicked, recBusy)
	}
}

// TestRecencyOfVisitStackCreditsEinkTwin verifies that a visit to a
// session's -eink twin counts toward the base session's rank, and
// vice versa. The picker never displays "-eink"-suffixed rows
// separately, so a base row's recency must credit visits recorded
// under its twin's literal name — otherwise switching to "work-eink"
// (very common, since that's the whole point of the eink toggle)
// leaves "work" looking untouched in the picker.
func TestRecencyOfVisitStackCreditsEinkTwin(t *testing.T) {
	recent := recentFile{}
	visits := []string{"work-eink", "other"}
	rec, ok := recencyOf("work", nil, recent, visits)
	if !ok {
		t.Fatal("expected the -eink twin's visit to count for the base session")
	}
	if want := visitRankBase; rec != want {
		t.Errorf("got %d, want %d (rank 0 via the -eink twin)", rec, want)
	}
}

// TestRecencyOfFallsBackToRecentFile verifies the fallback path:
// when the entry is not a running tmux session (no sessionInfo), or
// the session has no activity yet (hasLastAct=false), recent.json
// is consulted.
func TestRecencyOfFallsBackToRecentFile(t *testing.T) {
	nowT := time.Now()
	// No sessionInfo — could be a zoxide path or a config session.
	recent := recentFile{entries: map[string]recentEntry{
		"~/work/q": {Count: 5, Last: nowT.Unix() - 100},
	}}
	rec, ok := recencyOf("~/work/q", nil, recent, nil)
	if !ok {
		t.Fatal("expected recent.json fallback to provide a signal")
	}
	if rec != nowT.Unix()-100 {
		t.Errorf("got %d, want %d", rec, nowT.Unix()-100)
	}
}

// TestRecencyOfFreshTmuxSessionWithNoActivity verifies that a
// freshly-created tmux session with no recorded activity yet
// returns (0, false) — letting the caller sink it to the bottom
// in stable order, rather than promoting a session that the user
// hasn't actually used.
func TestRecencyOfFreshTmuxSessionWithNoActivity(t *testing.T) {
	info := map[string]sessionInfo{
		"fresh": {meta: sessionMeta{hasLastAct: false}}, // no activity yet
	}
	recent := recentFile{entries: map[string]recentEntry{}}
	_, ok := recencyOf("fresh", info, recent, nil)
	if ok {
		t.Error("fresh tmux session with no activity should return (0, false)")
	}
}
