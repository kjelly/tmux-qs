package main

import (
	"bytes"
	"log"
	"os"
	"testing"
)

// TestMain initializes the fzf scoring tables before any test runs, the
// same way main() does in production. Without this, case-insensitive
// matching against inputs that contain uppercase letters (smart-case)
// behaves differently from the running app.
func TestMain(m *testing.M) {
	initFuzzy()
	os.Exit(m.Run())
}

func TestSilenceTUILogsDoesNotWriteToTerminal(t *testing.T) {
	previous := log.Writer()
	t.Cleanup(func() { log.SetOutput(previous) })

	var output bytes.Buffer
	log.SetOutput(&output)
	silenceTUILogs()
	log.Print("must not move the Bubble Tea cursor")
	if output.Len() != 0 {
		t.Fatalf("TUI log output = %q, want none", output.String())
	}
}
