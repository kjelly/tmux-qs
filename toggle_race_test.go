package main

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestRunToggle_NoPopupChildReturnsFalse verifies the most common
// "first press" case: there are no popup children running, so
// runToggle returns false and the caller opens the picker.
//
// We can't easily simulate a real popup child in a unit test, but
// the empty-cache case exercises the "no children" branch and the
// fallback-to-picker path in one shot.
func TestRunToggle_NoPopupChildReturnsFalse(t *testing.T) {
	defer withXDGCacheDir(t)()

	// Sanity: no other tmux-qs processes should be running for
	// this test, otherwise the test is fragile to the test env.
	if others := allOtherTmuxQsPIDs(); len(others) > 0 {
		t.Skipf("test requires no other tmux-qs processes; found %d", len(others))
	}

	got := runToggle("top,100%", true)
	if got {
		t.Errorf("runToggle should return false when no popup child is running (would skip the picker)")
	}
}

// TestWaitForPopupChild_WaitsForStartingPopup covers the gap between the
// first M-q launching display-popup and its child becoming visible to pgrep.
// A second M-q in that gap must still find and close the just-starting popup.
func TestWaitForPopupChild_WaitsForStartingPopup(t *testing.T) {
	lookups := 0
	got := waitForPopupChild(func() []int {
		lookups++
		if lookups < 3 {
			return nil
		}
		return []int{1234}
	}, 100*time.Millisecond)

	if len(got) != 1 || got[0] != 1234 {
		t.Fatalf("waitForPopupChild() = %v, want [1234]", got)
	}
	if lookups < 3 {
		t.Fatalf("waitForPopupChild checked %d times, want at least 3", lookups)
	}
}

// TestRunToggle_LockHeldReturnsFalse simulates the rapid
// double-press scenario: the first invocation acquires the lock,
// the second sees the lock held and returns false. We hold the
// lock manually to verify the path.
func TestRunToggle_LockHeldReturnsFalse(t *testing.T) {
	defer withXDGCacheDir(t)()

	release, ok := tryAcquireToggleLock()
	if !ok {
		t.Fatal("test setup: should be able to acquire lock")
	}
	defer release()

	start := time.Now()
	got := runToggle("top,100%", true)
	elapsed := time.Since(start)

	if got {
		t.Errorf("runToggle should return false when lock is held")
	}
	// Should be near-instant — the lock check is the very first
	// thing runToggle does, no pgrep, no flock polling.
	if elapsed > 100*time.Millisecond {
		t.Errorf("runToggle should return quickly when lock is held, took %v", elapsed)
	}
}

// TestRunToggle_LastSessionMissingFallsThrough is the regression
// test for the original exit(1) bug. With a clean cache directory
// and no popup child, runToggle should return false (fall through
// to picker) rather than exit 1.
func TestRunToggle_LastSessionMissingFallsThrough(t *testing.T) {
	defer withXDGCacheDir(t)()

	if others := allOtherTmuxQsPIDs(); len(others) > 0 {
		t.Skipf("test requires no other tmux-qs processes; found %d", len(others))
	}

	// Should NOT panic, should NOT call os.Exit, should return false.
	got := runToggle("top,100%", true)
	if got {
		t.Errorf("runToggle should return false when lastSessionSwitch would fail")
	}
}

// TestRunToggle_StaleLastSessionFallsThrough verifies that even if
// the last-session file exists but is empty (or points to a
// non-existent session), runToggle still falls through to the
// picker rather than exiting 1.
func TestRunToggle_StaleLastSessionFallsThrough(t *testing.T) {
	defer withXDGCacheDir(t)()

	// Write an empty last-session file.
	cacheDir := filepath.Join(t.TempDir(), "tmux-qs")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Dir(cacheDir))
	t.Setenv("HOME", filepath.Dir(cacheDir))
	if err := os.WriteFile(filepath.Join(cacheDir, "last-session"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	if others := allOtherTmuxQsPIDs(); len(others) > 0 {
		t.Skipf("test requires no other tmux-qs processes; found %d", len(others))
	}

	got := runToggle("top,100%", true)
	if got {
		t.Errorf("runToggle should return false when last-session is empty")
	}
}

// TestWaitForPopupChildExit_NonExistentPIDs verifies that
// waitForPopupChildExit returns immediately for PIDs that don't
// exist (no point in polling a dead PID).
func TestWaitForPopupChildExit_NonExistentPIDs(t *testing.T) {
	start := time.Now()
	// Use a PID that almost certainly doesn't exist.
	waitForPopupChildExit([]int{0x7FFFFFFF}, 1*time.Second)
	elapsed := time.Since(start)

	// `kill -0` on a non-existent PID should fail immediately,
	// so the wait should return in well under the 1s timeout.
	if elapsed > 200*time.Millisecond {
		t.Errorf("waitForPopupChildExit should return quickly for non-existent PIDs, took %v", elapsed)
	}
}

// TestWaitForPopupChildExit_EmptyList is a trivial sanity check:
// an empty input is a no-op.
func TestWaitForPopupChildExit_EmptyList(t *testing.T) {
	start := time.Now()
	waitForPopupChildExit(nil, 1*time.Second)
	waitForPopupChildExit([]int{}, 1*time.Second)
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Errorf("empty input should be instant, took %v", elapsed)
	}
}

// TestWaitForPopupChildExit_DeadlineExpires verifies that the
// function respects its timeout when given a non-existent PID —
// it should return essentially immediately (kill -0 fails fast)
// without blocking for the full timeout.
func TestWaitForPopupChildExit_RespectsTimeout(t *testing.T) {
	start := time.Now()
	// 1s timeout with a dead PID should return in well under that.
	waitForPopupChildExit([]int{0x7FFFFFFF}, 1*time.Second)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("waitForPopupChildExit took too long (%v) for a dead PID — should return as soon as kill -0 fails", elapsed)
	}
}

// TestRunToggle_DoublePressSerializes simulates the user's actual
// scenario: two concurrent toggle invocations. Exactly one should
// "do work" (return true, or in our case, return false because
// there's no child but the lock prevents duplicate work). The
// other should early-return.
func TestRunToggle_DoublePressSerializes(t *testing.T) {
	defer withXDGCacheDir(t)()

	if others := allOtherTmuxQsPIDs(); len(others) > 0 {
		t.Skipf("test requires no other tmux-qs processes; found %d", len(others))
	}

	// Run two concurrent runToggle calls. The first one wins the
	// lock; the second sees the lock held and returns false
	// immediately. The first itself returns false because there's
	// no popup child to close.
	//
	// We can't directly assert which one got the lock (it's
	// racy), but we can assert that both complete without
	// deadlock and both return false (no popup child).
	var wg sync.WaitGroup
	results := make([]bool, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = runToggle("top,100%", true)
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if r {
			t.Errorf("result[%d] = true, expected false (no popup child)", i)
		}
	}
}

// strconv import keeper (also used by other tests in the package).
var _ = strconv.Itoa
