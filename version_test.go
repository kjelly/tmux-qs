package main

import "testing"

func TestVersionVariable(t *testing.T) {
	if version == "" {
		t.Fatal("version variable should default to non-empty (got \"\")")
	}
	// In a plain `go test` (no ldflags) it should be "dev".
	if version != "dev" {
		t.Logf("version overridden to %q (expected for ldflags builds)", version)
	}
}
