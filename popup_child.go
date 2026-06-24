package main

import (
	"bytes"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// popupChildPIDs returns the PIDs of running tmux-qs processes that
// are tmux-qs popup children (i.e. have TMUX_QS_POPUP=1 in their
// environment) AND are not the current process.
//
// This is used by --toggle to kill the popup child (the TUI process
// spawned inside the tmux display-popup) without also killing the
// parent process that originally opened the popup. Killing the parent
// is harmless but pointless: the parent is already blocked in cmd.Run
// waiting for tmux's display-popup child to exit, and killing it can
// orphan the tmux display-popup machinery under system lag.
//
// On non-Linux platforms (macOS, BSD) we cannot read /proc, so we
// fall back to the conservative behavior of returning all tmux-qs
// PIDs that aren't us. The race window on those platforms is much
// smaller in practice (no fork-bomb-style system lag) so the original
// "kill everything" behavior is acceptable there.
func popupChildPIDs() []int {
	all := allOtherTmuxQsPIDs()
	if runtime.GOOS != "linux" {
		return all
	}
	var children []int
	for _, pid := range all {
		if pidHasEnv(pid, popupEnv+"=1") {
			children = append(children, pid)
		}
	}
	return children
}

// allOtherTmuxQsPIDs returns PIDs of running tmux-qs processes
// except the current one. Refactored out of the original
// otherInstancePIDs so both --toggle and any future code path can
// share the same enumeration.
//
// Returns an empty slice (not nil) on errors so callers can range
// over the result without a nil check.
func allOtherTmuxQsPIDs() []int {
	ourPID := os.Getpid()
	out, err := runLines("pgrep", "-x", "tmux-qs")
	if err != nil {
		return []int{}
	}
	pids := make([]int, 0, len(out))
	for _, line := range out {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid == ourPID {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// pidHasEnv reports whether the process with the given PID has an
// environment variable of the form key=value matching the given
// "key=value" string. Matches on exact byte equality (the env block
// is null-byte separated and the search string must match a single
// entry, so callers must include "=value" themselves).
//
// On Linux this reads /proc/<pid>/environ, which is a virtual file
// containing the process's initial environment null-byte separated.
// On other platforms (or when the file can't be read) we return
// true to err on the side of "treat as popup child" — this is the
// conservative choice: a false positive just means we kill the
// process, which the --toggle code path would have done anyway
// under the old behavior.
func pidHasEnv(pid int, keyValue string) bool {
	if runtime.GOOS != "linux" {
		return true
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		// /proc may not be mounted (containers, chroot), or the
		// process may have exited between pgrep and now. Treat
		// unknown as "could be a child" so the caller includes
		// it in the kill list — safer than leaving a popup
		// child alive after --toggle.
		return true
	}
	// The environ file uses NUL bytes as separators (not newlines).
	for _, entry := range bytes.Split(data, []byte{0}) {
		if bytes.Equal(entry, []byte(keyValue)) {
			return true
		}
	}
	return false
}
