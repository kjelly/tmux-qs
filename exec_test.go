package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunWithDeadlineCapturesCombinedOutput(t *testing.T) {
	out, err := runWithDeadline(time.Second, "sh", "-c", "printf stdout; printf stderr >&2")
	if err != nil {
		t.Fatalf("runWithDeadline: %v", err)
	}
	if got := string(out); !strings.Contains(got, "stdout") || !strings.Contains(got, "stderr") {
		t.Fatalf("combined output = %q", got)
	}
}

func TestRunWithDeadlineKillsShellProcessGroup(t *testing.T) {
	started := time.Now()
	_, err := runWithDeadline(40*time.Millisecond, "sh", "-c", "sleep 5 & wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout waited for descendant process: %v", elapsed)
	}
}

func TestRunActionIsNotBoundByQueryDeadline(t *testing.T) {
	started := time.Now()
	if err := runAction("sh", "-c", "sleep 0.08"); err != nil {
		t.Fatalf("runAction: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 60*time.Millisecond {
		t.Fatalf("action returned too early to have run command: %v", elapsed)
	}
}
