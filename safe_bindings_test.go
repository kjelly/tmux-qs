package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSafeTmuxBindingsSpecialChars(t *testing.T) {
	withTestTmuxServer(t)
	tmp := t.TempDir()

	sentinelFile := filepath.Join(tmp, "pwned_sentinel")
	_ = os.Remove(sentinelFile)

	// Directory containing spaces, single quote, double quote, semicolon, dollar, $(), and unicode
	dirName := `test dir 'quote' "dq";$(touch ` + sentinelFile + `)$VAR_測試`
	trickyDir := filepath.Join(tmp, dirName)
	if err := os.MkdirAll(trickyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	resultFile := filepath.Join(tmp, "env_result.txt")
	scriptPath := filepath.Join(tmp, "record_env.sh")
	scriptContent := "#!/bin/sh\nprintf '%s' \"$TMUX_QS_CALLER_CWD\" > " + resultFile + "\n"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	sessionName := "qs-safe-binding-test"
	if err := tmuxRun("new-session", "-d", "-s", sessionName, "-c", trickyDir); err != nil {
		t.Fatalf("new-session failed: %v", err)
	}
	defer func() { _ = tmuxRun("kill-session", "-t", sessionName) }()

	// Execute run-shell with #{q:pane_current_path} matching README format
	cmdStr := "TMUX_QS_CLIENT=#{q:client_name} TMUX_QS_CALLER_PANE=#{q:pane_id} TMUX_QS_CALLER_CWD=#{q:pane_current_path} exec " + scriptPath
	if err := tmuxRun("run-shell", "-t", sessionName, cmdStr); err != nil {
		t.Fatalf("run-shell failed: %v", err)
	}

	// Give script time to write result
	var recordedCwd string
	for i := 0; i < 20; i++ {
		data, err := os.ReadFile(resultFile)
		if err == nil && len(data) > 0 {
			recordedCwd = string(data)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 1. Sentinel must not exist (no code injection from ; or $() or quotes)
	if _, err := os.Stat(sentinelFile); err == nil {
		t.Fatalf("SECURITY VULNERABILITY: sentinel file was created via command injection!")
	}

	// 2. Recorded CWD must exactly match the directory path
	if recordedCwd != trickyDir {
		t.Errorf("recorded CWD mismatch:\n got:  %q\n want: %q", recordedCwd, trickyDir)
	}
}
