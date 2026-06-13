package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func executeCommand(cmd string) error {
	switch cmd {
	case "Tmux: Detach Client":
		return run("tmux", "detach-client")
	case "Tmux: Kill Server (Danger)":
		return run("tmux", "kill-server")
	case "Tmux: Reload Tmux Config":
		return run("tmux", "source-file", expandPath("~/.tmux.conf"))
	case "Tmux-QS: Open Config File":
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "nvim"
		}
		path := expandPath("~/.config/tmux-qs/config.toml")
		return run("tmux", "new-window", "-n", "config", editor+" "+path)
	case "Resurrect: Save Workspace State":
		return resurrectSave()
	case "Resurrect: Restore Workspace State":
		return resurrectRestore()
	}

	cfg := loadConfig()
	for _, c := range cfg.Commands {
		if c.Name == cmd {
			execCmd := c.Cmd
			if strings.Contains(execCmd, "{session}") {
				execCmd = strings.ReplaceAll(execCmd, "{session}", currentSessionName())
			}
			if strings.Contains(execCmd, "{path}") {
				execCmd = strings.ReplaceAll(execCmd, "{path}", currentSessionPath())
			}
			return run("sh", "-c", execCmd)
		}
	}
	return nil
}

func currentSessionPath() string {
	path, _ := runOut("tmux", "display-message", "-p", "#{session_path}")
	return path
}

type ResurrectState struct {
	Sessions map[string]string `json:"sessions"`
}

func stateFilePath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "state", "tmux-qs")
	os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "resurrect.json")
}

func resurrectSave() error {
	lines, err := runLines("tmux", "list-sessions", "-F", "#{session_name}\t#{session_path}")
	if err != nil {
		return err
	}
	state := ResurrectState{Sessions: make(map[string]string)}
	for _, l := range lines {
		parts := strings.Split(l, "\t")
		if len(parts) == 2 {
			state.Sessions[parts[0]] = parts[1]
		}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(stateFilePath(), data, 0644); err != nil {
		return err
	}
	// display message in tmux
	return run("tmux", "display-message", "tmux-qs: Workspace state saved successfully")
}

func resurrectRestore() error {
	data, err := os.ReadFile(stateFilePath())
	if err != nil {
		return err
	}
	var state ResurrectState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	
	// Get currently active sessions to avoid duplicates
	currentLines, _ := runLines("tmux", "list-sessions", "-F", "#{session_name}")
	current := make(map[string]bool)
	for _, l := range currentLines {
		current[l] = true
	}

	count := 0
	for name, path := range state.Sessions {
		if !current[name] {
			if err := run("tmux", "new-session", "-d", "-s", name, "-c", path); err == nil {
				count++
			}
		}
	}
	return run("tmux", "display-message", fmt.Sprintf("tmux-qs: Restored %d sessions", count))
}
