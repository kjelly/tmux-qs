package main

import (
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"
)

// A stamp describes the screen that capture-pane reads. Output generation
// alone does not cover resize, clear-history, respawn or server replacement.
type paneOutputStamp struct {
	serverIdentity string
	paneID         string
	pid            int
	tty            string
	width          int
	height         int
	alternate      string
	history        string
	mode           string
	generation     uint64
	native         bool
	mtime          int64
}

// Observation identity is independent of the captured screen stamp:
// progress can have a previous sample before any screen is captured.
type paneOutputIdentity struct {
	server string
	paneID string
	pid    int
}

func (stamp paneOutputStamp) identity() paneOutputIdentity {
	return paneOutputIdentity{server: stamp.serverIdentity, paneID: stamp.paneID, pid: stamp.pid}
}

func parseOutputGeneration(raw string) (uint64, bool) {
	if raw == "" {
		return 0, false
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	return value, err == nil
}

func parseLastOutputTime(raw string, now time.Time) int64 {
	value, ok := parseOutputGeneration(raw)
	if !ok || value == 0 || value > uint64(now.Unix()) {
		return 0
	}
	return int64(value)
}

func (s paneState) outputIdle(now time.Time) time.Duration {
	if s.lastOutputTime > 0 && s.lastOutputTime <= now.Unix() {
		return now.Sub(time.Unix(s.lastOutputTime, 0))
	}
	return max(0, ttyIdle(s.tty, now))
}

func (s paneState) previousCapture(previous paneCapture) paneCapture {
	if previous.identity != s.output.identity() {
		return paneCapture{}
	}
	return previous
}

func (s paneState) canReuseCapture(previous paneCapture) bool {
	if !previous.captureValid || s.output.serverIdentity == "" || s.output.paneID == "" || s.output.pid <= 0 || s.output.width <= 0 || s.output.height <= 0 {
		return false
	}
	if s.output != previous.output {
		return false
	}
	return s.output.native || s.output.mtime != 0
}

func (s paneState) captureSnapshot() paneCapture {
	return paneCapture{
		ttyMtime: s.ttyMtime, buf: s.buf, identity: s.output.identity(),
		output: s.capturedOutput, captureValid: s.captureValid,
	}
}

// User acknowledgements and snoozes expire on any new output. Agent hooks
// tolerate the immediate dialog/composer redraw, as on older servers.
func (s paneState) agentEventCurrent() bool {
	event := s.agentEvent
	if event.OutputGeneration != nil && s.output.native {
		if event.OutputServer != s.output.serverIdentity || event.OutputPID != s.pid {
			return false
		}
		if *event.OutputGeneration == s.output.generation {
			return true
		}
		if event.State == "reviewed" || !event.SnoozeUntil.IsZero() {
			return false
		}
		if s.lastOutputTime == 0 {
			return false // A changed generation alone cannot date hook output.
		}
	}
	if s.lastOutputTime > 0 {
		return s.lastOutputTime <= event.At.Add(2*time.Second).Unix()
	}
	return s.ttyMtime <= event.At.Add(2*time.Second).UnixNano()
}

// The preview signal includes metadata displayed next to the screen, but
// excludes advancing idle ages. It is scoped to the watcher server.
func panePreviewSignals(states map[paneKey]paneState) map[string]string {
	signals := make(map[string]string, len(states))
	for s := range maps.Values(states) {
		signals[s.paneID] = fmt.Sprintf("%+v\x00%s\x00%s\x00%s", s.output, s.cmd, s.dir, s.title)
	}
	return signals
}

func (m model) outputPreviewSignal(info waitingInfo, entry string) string {
	_, label, server := m.tmuxContextForEntry(entry)
	signals := info.outputSignals
	if info.outputByServer != nil {
		signals = info.outputByServer[server.cacheKey()]
	} else if info.outputServer != server.cacheKey() {
		return ""
	}
	var signal strings.Builder
	parts := strings.SplitN(entry, "\t", 4)
	if len(parts) >= 3 && parts[2] != "" {
		return signals[parts[2]]
	}
	_, session := sessionServer(entry)
	for _, name := range []string{session, session + "-eink"} {
		paneID := info.activePanes[qualifyWatchSession(label, name)]
		fmt.Fprintf(&signal, "%s\x00%s\x00", paneID, signals[paneID])
	}
	return signal.String()
}
