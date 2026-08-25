package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const einkClientOverridesFile = "eink-client-overrides.json"

const einkEnvironmentVariable = "LC_IS_EINK"

func einkEnvironmentRequestsForce() bool {
	return strings.TrimSpace(os.Getenv(einkEnvironmentVariable)) == "1"
}

// autoForceEinkClient persists the compatibility environment signal as a
// client-scoped override. TMUX_QS_CLIENT is supplied by popup bindings when
// available; currentTmuxClientName falls back to tmux's current client for
// inline invocations.
func autoForceEinkClient() error {
	if !einkEnvironmentRequestsForce() {
		return nil
	}
	if strings.TrimSpace(os.Getenv("TMUX")) == "" && strings.TrimSpace(os.Getenv("TMUX_QS_CLIENT")) == "" {
		return nil
	}
	if currentTmuxClientName() == "" || einkClientForced() {
		return nil
	}
	return setEinkClientForced(true)
}

func currentTmuxClientName() string {
	if client := strings.TrimSpace(os.Getenv("TMUX_QS_CLIENT")); client != "" {
		return client
	}
	raw, err := tmuxRunOut("display-message", "-p", "#{client_name}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(raw)
}

func einkClientOverrideKey() string {
	client := currentTmuxClientName()
	if client == "" {
		return ""
	}
	server := getTmuxServer()
	serverKey := server.flag + "=" + server.value
	if serverKey == "=" {
		// TMUX starts with the socket path followed by two comma-separated
		// ids. It distinguishes non-default servers without requiring a
		// second tmux query.
		serverKey = strings.SplitN(os.Getenv("TMUX"), ",", 2)[0]
	}
	if serverKey == "" {
		serverKey = "default"
	}
	return serverKey + "\x00" + client
}

func einkClientOverridesPath() string {
	return xdgCachePath(einkClientOverridesFile)
}

func loadEinkClientOverrides() (map[string]bool, error) {
	path := einkClientOverridesPath()
	if path == "" {
		return nil, fmt.Errorf("cannot determine tmux-qs cache directory")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var overrides map[string]bool
	if err := json.Unmarshal(data, &overrides); err != nil {
		return nil, fmt.Errorf("invalid e-ink client overrides: %w", err)
	}
	if overrides == nil {
		overrides = map[string]bool{}
	}
	return overrides, nil
}

func writeEinkClientOverrides(overrides map[string]bool) error {
	path := einkClientOverridesPath()
	if path == "" {
		return fmt.Errorf("cannot determine tmux-qs cache directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(overrides, "", "  ")
	if err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(path), ".eink-client-*")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := tmpFile.Write(append(data, '\n')); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func einkClientForced() bool {
	key := einkClientOverrideKey()
	if key == "" {
		return false
	}
	overrides, err := loadEinkClientOverrides()
	return err == nil && overrides[key]
}

func setEinkClientForced(forced bool) error {
	key := einkClientOverrideKey()
	if key == "" {
		return fmt.Errorf("cannot determine current tmux client")
	}
	overrides, err := loadEinkClientOverrides()
	if err != nil {
		return err
	}
	if forced {
		overrides[key] = true
	} else {
		delete(overrides, key)
	}
	return writeEinkClientOverrides(overrides)
}
