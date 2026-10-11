package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

// Notify HUP throughout Run and cleanup: tmux sends it when a pane/window
// or popup disappears. A second HUP must not interrupt hook removal.
func runPickerWithWatchEvents(p *tea.Program, events *watchEventHub) (tea.Model, error) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP)
	defer stop()
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			events.close()
			p.Quit()
		case <-done:
		}
	}()
	final, err := p.Run()
	events.close()
	close(done)
	<-stopped
	return final, err
}

func watchProcessIdentity(pid int) (string, string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "0", ""
	} // Non-Linux: dead-PID checks remain safe.
	end := strings.LastIndex(string(data), ") ")
	if end < 0 {
		return "0", ""
	}
	fields := strings.Fields(string(data)[end+2:])
	if len(fields) <= 19 {
		return "0", ""
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "0", ""
	}
	return fields[19], fields[0]
}

func watchOwnerAlive(pid int, start string) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	actual, state := watchProcessIdentity(pid)
	if state == "Z" || state == "X" {
		return false
	}
	return start == "0" || actual == "0" || start == actual
}

func watchOwnerScope() string {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	namespace, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(string(boot))+"\x00"+namespace)))
}

func watchMonitorName(pid int, start, instance, worker string) string {
	return fmt.Sprintf("@tmux_qs_watch_v2_%s_%d_%s_%s_%s", watchOwnerScope(), pid, start, instance, worker)
}

var watchOwnedMonitorName = regexp.MustCompile(`^@tmux_qs_watch_v2_([0-9a-f]{64})_([1-9][0-9]*)_([0-9]+)_[0-9a-f]{32}_[0-9a-f]{32}$`)

func cleanupAbandonedWatchMonitors(ctx context.Context, server tmuxServerSpec) {
	scope := watchOwnerScope()
	if scope == "" {
		return
	}
	lines, err := tmuxRunOutContextOn(ctx, server, "show-hooks", "-g", "-B")
	if err != nil {
		return
	}
	for line := range strings.SplitSeq(lines, "\n") {
		name, _, _ := strings.Cut(line, ":")
		parts := watchOwnedMonitorName.FindStringSubmatch(name)
		if parts == nil || parts[1] != scope {
			continue
		}
		pid, err := strconv.Atoi(parts[2])
		if err != nil || watchOwnerAlive(pid, parts[3]) {
			continue
		}
		_, _ = tmuxRunOutContextOn(ctx, server, "set-hook", "-gu", "-B", parts[0])
		_, _ = tmuxRunOutContextOn(ctx, server, "set-hook", "-gu", parts[0])
	}
}
