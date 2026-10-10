package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const watchEventCoalesce = time.Second

// One server monitor checks the aggregate format once a second. Nested
// loops cover every session/window/pane without attaching a synthetic client.
// Shell integration is a wake-up signal, never an Agent completion signal.
func watchMonitorFormat(selfID string) string {
	pane := "#{pane_id}|#{pane_output_generation}|#{pane_current_command}|#{pane_current_path}|#{pane_title}|#{pane_in_mode}|#{pane_width}|#{pane_height}|#{history_generation}|#{pane_pb_state}|#{pane_pb_progress}|#{pane_last_prompt_time}|#{pane_command_running}|#{pane_command_start_time}|#{pane_command_end_time}|#{pane_dead}|#{@tmux_qs_agent_event}|#{@tmux_qs_reviewed}|#{@tmux_qs_snoozed};"
	if paneIDRegex.MatchString(selfID) {
		pane = "#{?#{==:#{pane_id}," + selfID + "},," + pane + "}"
	}
	pane = "#{?@tmux_qs,,#{?#{==:#{pane_current_command},tmux-qs},," + pane + "}}"
	return "#{S:#{session_id}|#{session_name}|#{W:#{window_id}|#{window_name}|#{window_active}|#{pane_id}|#{P:" + pane + "}}}"
}

// watchEventHub owns all subprocesses and temporary hooks for one TUI.
// It intentionally has no global state; cancellation precedes main's exit.
type watchEventHub struct {
	ctx           context.Context
	cancel        context.CancelFunc
	changed       chan struct{}
	raw           chan struct{}
	mu            sync.Mutex
	workers       map[string]context.CancelFunc
	wg            sync.WaitGroup
	nonce         string
	owner         tmuxServerSpec
	lastDiscovery time.Time
}

func newWatchEventHub(parent context.Context) *watchEventHub {
	ctx, cancel := context.WithCancel(parent)
	nonce, _ := generateInstanceNonce()
	h := &watchEventHub{ctx: ctx, cancel: cancel, changed: make(chan struct{}, 1), raw: make(chan struct{}, 1), workers: map[string]context.CancelFunc{}, nonce: nonce, owner: getTmuxServer()}
	h.wg.Add(1)
	go func() { defer h.wg.Done(); coalesceWatchEvents(ctx, h.raw, h.changed, watchEventCoalesce) }()
	return h
}

func coalesceWatchEvents(ctx context.Context, raw <-chan struct{}, out chan<- struct{}, delay time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-raw:
		}
		// A fixed window avoids starvation under continuous output.
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	drain:
		for {
			select {
			case <-raw:
			default:
				break drain
			}
		}
		select {
		case out <- struct{}{}:
		default:
		}
	}
}

func (h *watchEventHub) configure(endpoints []tmuxServerEndpoint, selfID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil || h.nonce == "" {
		return
	}
	desired := map[string]bool{}
	for _, endpoint := range endpoints {
		key := endpoint.spec.cacheKey()
		desired[key] = true
		if _, ok := h.workers[key]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(h.ctx)
		h.workers[key] = cancel
		// The worker resolves socket identity before excluding the inline
		// pane: different servers may reuse the same numeric pane ID.
		exclude := selfID
		h.wg.Add(1)
		go func() { defer h.wg.Done(); h.runServer(ctx, endpoint.spec, exclude) }()
	}
	for key, cancel := range h.workers {
		if !desired[key] {
			cancel()
			delete(h.workers, key)
		}
	}
}

func (h *watchEventHub) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.cancel()
	h.mu.Unlock()
	h.wg.Wait()
}

func (h *watchEventHub) runServer(ctx context.Context, server tmuxServerSpec, selfID string) {
	for ctx.Err() == nil {
		// Re-probe after disconnect/restart, including old-to-new upgrades.
		caps, err := probeServerCapabilitiesWithRunner(server, watchCapabilityRunner{ctx})
		retry := 2 * time.Second
		if err == nil && caps.EventMonitors {
			h.monitorServer(ctx, server, selfID)
		} else if err == nil {
			retry = 30 * time.Second
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (h *watchEventHub) monitorServer(ctx context.Context, server tmuxServerSpec, selfID string) {
	if selfID != "" && server.cacheKey() != h.owner.cacheKey() {
		ownerSocket, _ := tmuxRunOutContextOn(ctx, h.owner, "display-message", "-p", "#{socket_path}")
		targetSocket, _ := tmuxRunOutContextOn(ctx, server, "display-message", "-p", "#{socket_path}")
		if ownerSocket == "" || targetSocket != ownerSocket {
			selfID = ""
		}
	}
	workerNonce, err := generateInstanceNonce()
	if err != nil {
		return
	}
	name := "@tmux_qs_watch_" + h.nonce + "_" + workerNonce
	defer func() {
		// Cleanup uses a fresh bounded context because the owner was cancelled.
		cleanup, cancel := context.WithTimeout(context.Background(), execTimeout)
		defer cancel()
		_, _ = tmuxRunOutContextOn(cleanup, server, "set-hook", "-gu", "-B", name)
		_, _ = tmuxRunOutContextOn(cleanup, server, "set-hook", "-gu", name)
	}()
	args := []string{"set-hook", "-g", "-B", name + "::" + watchMonitorFormat(selfID)}
	if _, err := tmuxRunOutContextOn(ctx, server, args...); err != nil {
		return
	}
	select {
	case h.raw <- struct{}{}:
	default:
	}
	_ = receiveMonitorEvents(ctx, server, name, func() {
		select {
		case h.raw <- struct{}{}:
		default:
		}
	})
}

// -v prints an event before -F is evaluated. The constant false filter keeps
// the waiter subscribed until cancellation, so there is no re-arm gap.
func receiveMonitorEvents(ctx context.Context, server tmuxServerSpec, name string, emit func()) error {
	args := append(server.args(), "wait-for", "-Ev", "-F", "0", name)
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.WaitDelay = time.Second
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		if scanner.Text() == "event="+name {
			emit()
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanErr != nil {
		return fmt.Errorf("monitor event stream: %w", scanErr)
	}
	return waitErr
}

func watchEndpoints() []tmuxServerEndpoint {
	current := getTmuxServer()
	if !allServersMode {
		return []tmuxServerEndpoint{{spec: current}}
	}
	allServerEndpoints.RLock()
	defer allServerEndpoints.RUnlock()
	var endpoints []tmuxServerEndpoint
	for label, spec := range allServerEndpoints.byLabel {
		endpoints = append(endpoints, tmuxServerEndpoint{label: label, spec: spec})
	}
	if len(endpoints) == 0 {
		return []tmuxServerEndpoint{{spec: current}}
	}
	return endpoints
}

func qualifyWatchSession(label, session string) string {
	if label == "" {
		return session
	}
	return "[" + label + "] " + session
}

func eventWait(ctx context.Context, changed <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-changed:
		return true
	case <-timer.C:
		return true
	}
}

func collectAllWatchMessages(endpoints []tmuxServerEndpoint, self paneKey, opts WatchingConfig, prev map[paneKey]paneCapture, prevErrs int, pinned map[string]bool, now time.Time) watchMsg {
	result := watchMsg{captures: map[paneKey]paneCapture{}, info: waitingInfo{bySession: map[string][]procCount{}, byPath: map[string][]procCount{}, paths: map[string]string{}, activePanes: map[string]string{}, outputByServer: map[string]map[string]string{}, lastUpdated: now, version: 1}}
	var errs []error
	ownerIdentity := ""
	if self.session != "" {
		ownerIdentity, _ = tmuxRunOut("display-message", "-p", "#{pid}:#{start_time}")
	}
	var topology []string
	for _, endpoint := range endpoints {
		prefix := "[" + endpoint.label + "] "
		localPrev := map[paneKey]paneCapture{}
		for k, v := range prev {
			if bare, ok := strings.CutPrefix(k.session, prefix); ok {
				k.session = bare
				localPrev[k] = v
			}
		}
		localPinned := map[string]bool{}
		for name, value := range pinned {
			if bare, ok := strings.CutPrefix(name, prefix); ok {
				localPinned[bare] = value
			}
		}
		// Keep the inline self key local to its owning server; other servers
		// can reuse its pane/window IDs. Floating UI panes use their marker.
		localSelf := paneKey{}
		if ownerIdentity != "" {
			identity, _ := tmuxRunOutOn(endpoint.spec, "display-message", "-p", "#{pid}:#{start_time}")
			if identity == ownerIdentity {
				localSelf = self
			}
		}
		msg := collectWatchMessage(endpoint.spec, localSelf, opts, localPrev, prevErrs, localPinned, now)
		if msg.err != nil {
			errs = append(errs, msg.err)
			continue
		}
		for name, value := range msg.info.bySession {
			result.info.bySession[qualifyWatchSession(endpoint.label, name)] = value
		}
		for path, value := range msg.info.byPath {
			result.info.byPath[path] = append(result.info.byPath[path], value...)
		}
		for name, path := range msg.info.paths {
			result.info.paths[qualifyWatchSession(endpoint.label, name)] = path
		}
		for name, id := range msg.info.activePanes {
			result.info.activePanes[qualifyWatchSession(endpoint.label, name)] = id
		}
		result.info.outputByServer[endpoint.spec.cacheKey()] = msg.info.outputSignals
		topology = append(topology, endpoint.label+"|"+msg.info.topology)
		for _, pane := range msg.info.panes {
			pane.session = qualifyWatchSession(endpoint.label, pane.session)
			result.info.panes = append(result.info.panes, pane)
		}
		for k, v := range msg.captures {
			k.session = qualifyWatchSession(endpoint.label, k.session)
			result.captures[k] = v
		}
	}
	slices.Sort(topology)
	result.info.topology = strings.Join(topology, "\n")
	result.info.totalWaiting = len(result.info.bySession)
	// Healthy servers still update if one endpoint disconnects. Periodic
	// endpoint discovery and monitor retries recover that endpoint later.
	if len(errs) == len(endpoints) {
		result.err = errors.Join(errs...)
		result.consecutiveErrs = prevErrs + 1
	}
	return result
}

func watchTopology(states map[paneKey]paneState) string {
	parts := make([]string, 0, len(states))
	for _, p := range states {
		if isInternalTmuxQSPane(p.internalMarker) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%s.%d|%s|%s|%s", p.key.session, p.key.window, p.key.paneIndex, p.paneID, p.cmd, p.dir))
	}
	slices.Sort(parts)
	return fmt.Sprintf("%d\n%s", len(parts), strings.Join(parts, "\n"))
}

func watchSourceRefreshCmd(src sourceKind, generation uint64, current string, info map[string]sessionInfo, topology string) tea.Cmd {
	load := loadCmd(src, generation, current, info)
	return func() tea.Msg {
		msg := load()
		if items, ok := msg.(itemsMsg); ok {
			items.backgroundTopology = topology
			return items
		}
		if failure, ok := msg.(loadErrMsg); ok {
			return watchSourceRefreshErrMsg{generation: generation, err: failure.err}
		}
		return msg
	}
}

// Capability queries during reconnect share the owner's cancellation.
type watchCapabilityRunner struct{ ctx context.Context }

func (r watchCapabilityRunner) runOut(server tmuxServerSpec, args ...string) (string, error) {
	return tmuxRunOutContextOn(r.ctx, server, args...)
}
func (r watchCapabilityRunner) runLines(server tmuxServerSpec, args ...string) ([]string, error) {
	out, err := r.runOut(server, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func (h *watchEventHub) needDiscovery(interval time.Duration) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastDiscovery.IsZero() || time.Since(h.lastDiscovery) >= interval {
		h.lastDiscovery = time.Now()
		return true
	}
	return false
}

type watchSourceRefreshErrMsg struct {
	generation uint64
	err        error
}
