package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// toggleLockFile is the name of the file used to serialize --toggle
// invocations. Two rapid M-q presses spawn two tmux-qs processes; the
// first one acquires the lock and runs the toggle logic, the second
// sees the lock is held and returns immediately. This keeps the second
// process from killing the first's popup child while the first is
// still in the middle of the switch.
const toggleLockFile = "toggle.lock"

const (
	toggleOpeningPrefix = "toggle-opening-"
	toggleOpeningTTL    = 2 * time.Second
	toggleCloseRequest  = "close"
)

// tryAcquireToggleLock attempts to take an exclusive, non-blocking
// flock on the toggle lock file. Returns a release function and true
// on success, or nil and false when another tmux-qs process is
// already holding the lock. Callers MUST invoke release (typically via
// defer) so the lock is freed when the toggle logic finishes.
//
// On success the returned release function closes the underlying file
// descriptor, which releases the flock automatically. On failure the
// returned release is nil.
//
// Behavior is best-effort: if the lock file can't be created (e.g. the
// XDG cache directory is unreadable) we return false so the caller
// bails out rather than running duplicate toggle logic. Returning true
// without a working lock could result in race conditions during system
// lag, so we err on the safe side and abort.
func tryAcquireToggleLock() (release func(), ok bool) {
	path := xdgCachePath(toggleLockFile)
	if path == "" {
		return nil, false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, false
		}
		_ = f.Close()
		return nil, false
	}
	// Truncate the file so a leftover size from a previous crashed
	// process doesn't surprise anyone (e.g. the file growing to many
	// KBs over time is harmless but ugly).
	_ = f.Truncate(0)
	return func() {
		// Unlocking is implicit on close, but be explicit so the
		// behavior is obvious from reading this file.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true
}

// toggleLockPath returns the absolute path of the toggle lock file.
// Exported only for tests / diagnostics.
func toggleLockPath() string {
	return xdgCachePath(toggleLockFile)
}

// errToggleLockHeld is the diagnostic message printed (and discarded)
// when the lock is held. Kept as a var so it can be referenced by
// callers that want to log without having to duplicate the string.
var errToggleLockHeld = fmt.Errorf("toggle: another tmux-qs is already toggling")

// toggleOpeningPath is scoped to a tmux client so two terminals can open
// independent popups. Hashing keeps client names such as /dev/pts/4 safe as a
// cache filename.
func toggleOpeningPath(client string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(client)))
	return xdgCachePath(fmt.Sprintf("%s%x", toggleOpeningPrefix, sum[:8]))
}

// beginToggleOpening records that a popup child is being spawned. It returns
// true for the first press. A second press during the tiny spawn window marks
// a close request for the child to consume, without polling for its PID.
func beginToggleOpening(client string) bool {
	path := toggleOpeningPath(client)
	if path == "" || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return true
	}
	for range 2 {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_ = f.Close()
			return true
		}
		if !errors.Is(err, os.ErrExist) {
			return true
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > toggleOpeningTTL {
			_ = os.Remove(path)
			continue
		}
		_ = os.WriteFile(path, []byte(toggleCloseRequest), 0o644)
		return false
	}
	return true
}

// consumeToggleOpening removes the spawn marker when the popup child starts.
// It reports whether a second M-q arrived before the child was visible.
func consumeToggleOpening(client string) bool {
	path := toggleOpeningPath(client)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_ = os.Remove(path)
	return string(data) == toggleCloseRequest
}
