package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const agentEventOption = "@tmux_qs_agent_event"

// agentEvent is stored on the owning tmux pane. It survives closing the
// picker and carries the conversation ID needed to resume a saved chat.
type agentEvent struct {
	Agent        string    `json:"agent"`
	Conversation string    `json:"conversation,omitempty"`
	Task         string    `json:"task,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	State        string    `json:"state"`
	Cwd          string    `json:"cwd,omitempty"`
	At           time.Time `json:"at"`
	SnoozeUntil  time.Time `json:"snooze_until,omitzero"`
}

func parseAgentEvent(agent string, data []byte, now time.Time) (agentEvent, error) {
	var raw struct {
		Type          string   `json:"type"`
		Hook          string   `json:"hook_event_name"`
		Notification  string   `json:"notification_type"`
		ThreadID      string   `json:"thread-id"`
		SessionID     string   `json:"session_id"`
		InputMessages []string `json:"input-messages"`
		LastMessage   string   `json:"last-assistant-message"`
		ClaudeMessage string   `json:"last_assistant_message"`
		Cwd           string   `json:"cwd"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return agentEvent{}, err
	}
	event := agentEvent{Agent: agent, Cwd: raw.Cwd, At: now}
	switch agent {
	case "codex":
		if raw.Type != "agent-turn-complete" {
			return agentEvent{}, fmt.Errorf("unsupported Codex event %q", raw.Type)
		}
		event.State = "turn complete"
		event.Conversation = raw.ThreadID
		event.Task = briefText(strings.Join(raw.InputMessages, " "), 100)
		event.Summary = briefText(raw.LastMessage, 240)
	case "claude":
		event.Conversation = raw.SessionID
		switch raw.Hook {
		case "Stop":
			event.State = "turn complete"
			event.Summary = briefText(raw.ClaudeMessage, 240)
		case "PermissionRequest":
			event.State = "approval needed"
		case "Notification":
			switch raw.Notification {
			case "permission_prompt":
				event.State = "approval needed"
			case "idle_prompt":
				event.State = "answer needed"
			default:
				return agentEvent{}, fmt.Errorf("unsupported Claude notification %q", raw.Notification)
			}
		default:
			return agentEvent{}, fmt.Errorf("unsupported Claude hook %q", raw.Hook)
		}
	default:
		return agentEvent{}, fmt.Errorf("unsupported agent %q", agent)
	}
	return event, nil
}

func briefText(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

func readPaneAgentEvent(server tmuxServerSpec, pane string) agentEvent {
	raw, err := tmuxRunOutOn(server, "show-option", "-p", "-v", "-t", pane, agentEventOption)
	if err != nil {
		return agentEvent{}
	}
	var event agentEvent
	_ = json.Unmarshal([]byte(raw), &event)
	return event
}

func runAgentEvent(agent string, input io.Reader, pane string, server tmuxServerSpec) error {
	if !strings.HasPrefix(pane, "%") || len(pane) < 2 {
		return fmt.Errorf("agent-event requires TMUX_PANE")
	}
	data, err := io.ReadAll(io.LimitReader(input, 64*1024+1))
	if err != nil {
		return err
	}
	if len(data) > 64*1024 {
		return fmt.Errorf("agent event is too large")
	}
	event, err := parseAgentEvent(agent, data, time.Now())
	if err != nil {
		return err
	}
	previous := readPaneAgentEvent(server, pane)
	if event.Task == "" {
		event.Task = previous.Task
	}
	if event.Cwd == "" {
		event.Cwd = previous.Cwd
	}
	return writePaneAgentEvent(server, pane, event)
}

func agentEventFromOption(raw string) agentEvent {
	var event agentEvent
	_ = json.Unmarshal([]byte(raw), &event)
	return event
}

func acknowledgePaneAgent(target paneTarget) error {
	current, err := resolvePaneTargetOn(target.paneID, target.server)
	if err != nil || current.session != target.session {
		return fmt.Errorf("target pane is no longer available")
	}
	event := readPaneAgentEvent(target.server, target.paneID)
	if event.Agent == "" {
		event.Agent = target.command
	}
	event.State = "reviewed"
	event.At = time.Now()
	event.SnoozeUntil = time.Time{}
	return writePaneAgentEvent(target.server, target.paneID, event)
}

func snoozePaneAgent(target paneTarget, duration time.Duration) error {
	current, err := resolvePaneTargetOn(target.paneID, target.server)
	if err != nil || current.session != target.session {
		return fmt.Errorf("target pane is no longer available")
	}
	event := readPaneAgentEvent(target.server, target.paneID)
	if event.Agent == "" {
		event.Agent = target.command
		event.State = "quiet"
	}
	event.At = time.Now()
	event.SnoozeUntil = event.At.Add(duration)
	return writePaneAgentEvent(target.server, target.paneID, event)
}

func writePaneAgentEvent(server tmuxServerSpec, pane string, event agentEvent) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := tmuxRunOn(server, "set-option", "-p", "-t", pane, agentEventOption, string(encoded)); err != nil {
		return err
	}
	if path := cachePath(); path != "" {
		_ = os.Remove(path)
	}
	return nil
}
