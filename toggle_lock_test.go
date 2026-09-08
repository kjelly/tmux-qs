package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTryAcquireToggleLock_FirstWins(t *testing.T) {
	defer withXDGCacheDir(t)()

	release, ok := tryAcquireToggleLock()
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	if release == nil {
		t.Fatal("release should be non-nil on success")
	}
	release()
}

func TestTryAcquireToggleLock_ConflictWhenHeld(t *testing.T) {
	defer withXDGCacheDir(t)()

	release, ok := tryAcquireToggleLock()
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	defer release()

	release2, ok2 := tryAcquireToggleLock()
	if ok2 {
		release2()
		t.Fatal("second acquire should fail while first is held")
	}
	if release2 != nil {
		t.Fatal("release should be nil on failure")
	}
}

func TestTryAcquireToggleLock_ReleaseAllowsReacquire(t *testing.T) {
	defer withXDGCacheDir(t)()

	release, ok := tryAcquireToggleLock()
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	release()

	release2, ok2 := tryAcquireToggleLock()
	if !ok2 {
		t.Fatal("acquire after release should succeed")
	}
	release2()
}

func TestTryAcquireToggleLock_FileCreatedInCacheDir(t *testing.T) {
	defer withXDGCacheDir(t)()

	release, ok := tryAcquireToggleLock()
	if !ok {
		t.Fatal("acquire should succeed")
	}
	defer release()

	path := toggleLockPath()
	if path == "" {
		t.Fatal("toggleLockPath should return a non-empty path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should exist at %q: %v", path, err)
	}
	// File should live directly under a "tmux-qs" directory.
	if got := filepath.Base(filepath.Dir(path)); got != "tmux-qs" {
		t.Errorf("lock file should be under tmux-qs/, got parent dir %q (path %q)", got, path)
	}
}

func TestTryAcquireToggleLock_ConcurrentOnlyOneWins(t *testing.T) {
	defer withXDGCacheDir(t)()

	// Race 20 goroutines: the first to acquire holds the lock
	// for the entire test. The other 19 must observe the lock
	// held. This is a strict test of LOCK_NB serialization under
	// contention.
	//
	// Implementation note: we deliberately do NOT release the
	// winner's lock inside its goroutine — that would let
	// later goroutines "win" by getting the lock after the
	// winner released. The winner keeps the lock until the
	// test ends.
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	wins := make(chan struct {
		release func()
		ok      bool
	}, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			release, ok := tryAcquireToggleLock()
			wins <- struct {
				release func()
				ok      bool
			}{release, ok}
		}()
	}
	wg.Wait()
	close(wins)

	var winner struct {
		release func()
		ok      bool
	}
	count := 0
	for r := range wins {
		if r.ok {
			count++
			winner = r
		} else {
			if r.release != nil {
				t.Errorf("non-winner should have nil release, got non-nil")
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 winner, got %d", count)
	}
	if winner.release != nil {
		winner.release()
	}
}

func TestTryAcquireToggleLock_NoCacheDirReturnsFalse(t *testing.T) {
	// Point XDG_CACHE_HOME at a path that can't be created
	// (e.g. a regular file). acquire should fail gracefully
	// rather than panic.
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", blocker)
	t.Setenv("HOME", "/this/home/does/not/exist/at/all")

	release, ok := tryAcquireToggleLock()
	if ok {
		release()
		t.Fatal("acquire should fail when cache dir cannot be created")
	}
	if release != nil {
		t.Fatal("release should be nil on failure")
	}
}

func TestToggleOpeningSecondPressRequestsClose(t *testing.T) {
	defer withXDGCacheDir(t)()
	const client = "/dev/pts/4"
	if !beginToggleOpening(client) {
		t.Fatal("first press should begin popup startup")
	}
	if beginToggleOpening(client) {
		t.Fatal("second press should request the starting popup to close")
	}
	if !consumeToggleOpening(client) {
		t.Fatal("popup child should receive the close request")
	}
	if consumeToggleOpening(client) {
		t.Fatal("marker should be removed after consumption")
	}
}
