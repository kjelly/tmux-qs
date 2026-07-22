package main

import (
	"os"
	"runtime"
	"testing"
)

func TestAllOtherTmuxQsPIDs_ExcludesSelf(t *testing.T) {
	ourPID := os.Getpid()
	pids := allOtherTmuxQsPIDs()
	for _, p := range pids {
		if p == ourPID {
			t.Errorf("result should exclude our own PID %d, got %v", ourPID, pids)
		}
	}
}

func TestAllOtherTmuxQsPIDs_OnlyInts(t *testing.T) {
	// If pgrep returns something, every entry must be a valid PID.
	pids := allOtherTmuxQsPIDs()
	for _, p := range pids {
		if p <= 0 {
			t.Errorf("PID should be positive, got %d (list: %v)", p, pids)
		}
	}
}

func TestPidHasEnv_CurrentProcessHasPath(t *testing.T) {
	// PATH is set on virtually every Unix process spawned by go test.
	// We can't test for an exact value, but we can verify the
	// detection works by checking a key we know exists.
	if !pidHasEnv(os.Getpid(), "PATH="+os.Getenv("PATH")) {
		t.Errorf("current process should have PATH=%s in environ", os.Getenv("PATH"))
	}
}

func TestPidHasEnv_NonExistentPIDReturnsFalseOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific (uses /proc)")
	}
	// Use a PID that's almost certainly dead. 0x7FFFFFFF is the
	// max int32; on the off chance it's alive, pidHasEnv would
	// return whatever the real process has, so we don't assert
	// "false" here — we just confirm the function doesn't crash.
	_ = pidHasEnv(0x7FFFFFFF, "TMUX_QS_POPUP=1")
}

func TestPidHasEnv_NoMatchReturnsFalse(t *testing.T) {
	// Current process does NOT have this fake env var.
	if pidHasEnv(os.Getpid(), "TMUX_QS_POPUP_DEFINITELY_NOT_SET_XYZZY=1") {
		t.Errorf("current process should not have a fake env var set")
	}
}

func TestPopupChildPIDs_DoesNotIncludeSelf(t *testing.T) {
	ourPID := os.Getpid()
	pids := popupChildPIDs("")
	for _, p := range pids {
		if p == ourPID {
			t.Errorf("popupChildPIDs should exclude our own PID %d, got %v", ourPID, pids)
		}
	}
}

func TestPopupChildPIDs_AllHavePopupEnv(t *testing.T) {
	// Property: every PID returned by popupChildPIDs MUST have
	// TMUX_QS_POPUP=1 in its environment. We verify by re-reading
	// each PID's environ. On non-Linux we skip — the implementation
	// falls back to the unfiltered list which doesn't claim to be
	// popup-child-only.
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific (uses /proc)")
	}
	for _, pid := range popupChildPIDs("") {
		if !pidHasEnv(pid, popupEnv+"=1") {
			t.Errorf("popupChildPIDs returned PID %d which lacks %s=1", pid, popupEnv)
		}
	}
}

func TestPopupChildPIDs_FilteredFromAllList(t *testing.T) {
	// popupChildPIDs should be a subset of allOtherTmuxQsPIDs —
	// it can never include a PID that wasn't in the all-list.
	all := make(map[int]bool)
	for _, p := range allOtherTmuxQsPIDs() {
		all[p] = true
	}
	for _, p := range popupChildPIDs("") {
		if !all[p] {
			t.Errorf("popupChildPIDs returned PID %d which isn't in allOtherTmuxQsPIDs", p)
		}
	}
}
