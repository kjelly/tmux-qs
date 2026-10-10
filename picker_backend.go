package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var errFallbackPopup = errors.New("floating backend fallback to popup")

type PickerOpenRequest struct {
	Context      popupContext
	PopupSpec    string
	OpenSnippets bool
	AllServers   bool
}

type PickerToggleRequest struct {
	Context      popupContext
	PopupSpec    string
	OpenSnippets bool
	AllServers   bool
}

type PickerToggleResult struct {
	Consumed bool
	Closed   bool
}

type PickerBackend interface {
	Open(PickerOpenRequest) error
	Toggle(PickerToggleRequest) (PickerToggleResult, error)
}

func resolvePickerBackend(cfg Config, caps TmuxCapabilities) PickerBackend {
	b := strings.ToLower(strings.TrimSpace(cfg.UI.Backend))
	if b == "" || b == "popup" {
		return &popupBackend{}
	}
	if b == "floating" {
		if caps.FloatingPane && (!cfg.UI.FloatingModal || caps.FloatingModal) {
			return &floatingBackend{modal: cfg.UI.FloatingModal}
		}
		return &popupBackend{}
	}
	log.Printf("config: unknown ui.backend %q, falling back to popup", cfg.UI.Backend)
	return &popupBackend{}
}

type popupBackend struct{}

func (p *popupBackend) Open(req PickerOpenRequest) error {
	return openInPopup(req.PopupSpec, req.OpenSnippets, req.AllServers)
}

func (p *popupBackend) Toggle(req PickerToggleRequest) (PickerToggleResult, error) {
	consumed := runToggle(req.PopupSpec, true)
	return PickerToggleResult{Consumed: consumed, Closed: consumed}, nil
}

type floatingBackend struct {
	modal  bool
	runner capabilityRunner
}

func (f *floatingBackend) commandRunner() capabilityRunner {
	if f.runner != nil {
		return f.runner
	}
	return realCapabilityRunner{}
}

func floatingPresentationArgs(caps TmuxCapabilities, modal bool) []string {
	if !modal {
		return nil
	}
	args := []string{"-O"}
	if caps.FloatingTitle {
		args = append(args, "-T", "tmux-qs")
	}
	if caps.FloatingBorder {
		args = append(args, "-B", "single")
	}
	// Do not enable -D (intercepts Esc), -K (captures prefix), or -C
	// (changes generic mouse behavior). The TUI owns navigation and exit.
	return args
}

var paneIDRegex = regexp.MustCompile(`^%[0-9]+$`)

func generateInstanceNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func tmuxPaneExists(server tmuxServerSpec, paneID string) bool {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return false
	}
	out, err := tmuxRunOutOn(server, "display-message", "-p", "-t", paneID, "#{pane_id}")
	return err == nil && strings.TrimSpace(out) == paneID
}

func cleanupFailedFloatingPane(server tmuxServerSpec, paneID string) error {
	return cleanupFailedFloatingPaneWithRunner(server, paneID, realCapabilityRunner{})
}

func cleanupFailedFloatingPaneWithRunner(server tmuxServerSpec, paneID string, runner capabilityRunner) error {
	_, _ = runner.runOut(server, "kill-pane", "-t", paneID)
	out, err := runner.runOut(server, "display-message", "-p", "-t", paneID, "#{pane_id}")
	if err == nil && strings.TrimSpace(out) == paneID {
		return fmt.Errorf("fatal: failed to clean up floating pane %s", paneID)
	}
	return nil
}

func (f *floatingBackend) Open(req PickerOpenRequest) error {
	client := strings.TrimSpace(req.Context.client)
	pane := strings.TrimSpace(req.Context.pane)
	cwd := strings.TrimSpace(req.Context.cwd)
	server := getTmuxServer()

	if client == "" || pane == "" || cwd == "" {
		return errFallbackPopup
	}

	caps := getTmuxCapabilities(server)
	if !caps.FloatingPane || (f.modal && !caps.FloatingModal) {
		return errFallbackPopup
	}

	runner := f.commandRunner()
	dimOut, err := runner.runOut(server, "display-message", "-p", "-t", pane, "#{window_width}\t#{window_height}")
	if err != nil {
		return errFallbackPopup
	}
	dimFields := strings.Split(strings.TrimSpace(dimOut), "\t")
	if len(dimFields) < 2 {
		return errFallbackPopup
	}
	W, errW := strconv.Atoi(dimFields[0])
	H, errH := strconv.Atoi(dimFields[1])
	if errW != nil || errH != nil || W <= 0 || H <= 0 {
		return errFallbackPopup
	}

	geom := parseGeometrySpec(req.PopupSpec)
	floatArgs, err := geom.floatingArgs(W, H)
	if err != nil {
		return errFallbackPopup
	}

	nonce, err := generateInstanceNonce()
	if err != nil {
		return errFallbackPopup
	}

	self, err := os.Executable()
	if err != nil {
		return errFallbackPopup
	}

	newPaneArgs := []string{"new-pane", "-t", pane, "-c", cwd}
	newPaneArgs = append(newPaneArgs, floatArgs...)
	newPaneArgs = append(newPaneArgs, floatingPresentationArgs(caps, f.modal)...)
	newPaneArgs = append(newPaneArgs,
		"-e", "TMUX_QS_FLOATING=1",
		"-e", "TMUX_QS_CLIENT="+client,
		"-e", "TMUX_QS_CALLER_PANE="+pane,
		"-e", "TMUX_QS_CALLER_CWD="+cwd,
	)
	if server.flag != "" && server.value != "" {
		newPaneArgs = append(newPaneArgs, "-e", "TMUX_QS_SERVER="+server.flag+"="+server.value)
	}
	newPaneArgs = append(newPaneArgs, "-P", "-F", "#{pane_id}")
	newPaneArgs = append(newPaneArgs, self, "--no-popup")
	if vimEnabled {
		newPaneArgs = append(newPaneArgs, "--vim")
	}
	if req.OpenSnippets {
		newPaneArgs = append(newPaneArgs, "--snippets")
	}
	if req.AllServers {
		newPaneArgs = append(newPaneArgs, "--all-servers")
	}

	out, err := runner.runOut(server, newPaneArgs...)
	if err != nil {
		// tmux enforces the single-modal constraint atomically. A modal
		// belonging to another client must never be dismissed or bypassed.
		if f.modal && strings.Contains(out+err.Error(), "already has a modal pane") {
			return fmt.Errorf("floating picker: window already has a modal pane")
		}
		return errFallbackPopup
	}
	paneID := strings.TrimSpace(out)
	if !paneIDRegex.MatchString(paneID) {
		return errFallbackPopup
	}

	// Set options sequentially: internal marker first
	if _, err := runner.runOut(server, "set-option", "-p", "-t", paneID, "@tmux_qs", "1"); err != nil {
		if cleanErr := cleanupFailedFloatingPaneWithRunner(server, paneID, runner); cleanErr != nil {
			return cleanErr
		}
		return errFallbackPopup
	}
	if _, err := runner.runOut(server, "set-option", "-p", "-t", paneID, "@tmux_qs_client", client); err != nil {
		if cleanErr := cleanupFailedFloatingPaneWithRunner(server, paneID, runner); cleanErr != nil {
			return cleanErr
		}
		return errFallbackPopup
	}
	if _, err := runner.runOut(server, "set-option", "-p", "-t", paneID, "@tmux_qs_instance", nonce); err != nil {
		if cleanErr := cleanupFailedFloatingPaneWithRunner(server, paneID, runner); cleanErr != nil {
			return cleanErr
		}
		return errFallbackPopup
	}

	return nil
}

func (f *floatingBackend) Toggle(req PickerToggleRequest) (PickerToggleResult, error) {
	client := strings.TrimSpace(req.Context.client)
	if client == "" {
		// client cannot be resolved; fallback to popup
		return (&popupBackend{}).Toggle(req)
	}

	releaseLock, ok := tryAcquireToggleLock()
	if !ok {
		requestToggleClose(client)
		return PickerToggleResult{Consumed: true, Closed: false}, nil
	}
	lockReleased := false
	defer func() {
		if !lockReleased {
			releaseLock()
		}
	}()

	server := getTmuxServer()
	lines, err := f.listOwnedPickerPanes(server, req.Context.pane)
	if err != nil {
		return PickerToggleResult{Consumed: true}, fmt.Errorf("list floating pickers: %w", err)
	}
	var matchingPanes []string
	if err == nil {
		for _, l := range lines {
			parts := strings.Split(l, "\t")
			if len(parts) >= 4 && parts[1] == "1" && parts[2] == "1" && parts[3] == client {
				matchingPanes = append(matchingPanes, parts[0])
			}
		}
	}

	if len(matchingPanes) > 0 {
		for _, pid := range matchingPanes {
			if err := cleanupFailedFloatingPaneWithRunner(server, pid, f.commandRunner()); err != nil {
				return PickerToggleResult{Consumed: true}, err
			}
		}
		_ = lastSessionSwitch()
		return PickerToggleResult{Consumed: true, Closed: true}, nil
	}

	// 0 matching panes found: open floating pane
	beginToggleOpening(client)
	defer cancelToggleOpening(client)

	openErr := f.Open(PickerOpenRequest(req))
	if openErr != nil {
		if errors.Is(openErr, errFallbackPopup) {
			if consumeToggleOpening(client) {
				_ = lastSessionSwitch()
				return PickerToggleResult{Consumed: true, Closed: true}, nil
			}
			lockReleased = true
			releaseLock()
			popErr := (&popupBackend{}).Open(PickerOpenRequest(req))
			return PickerToggleResult{Consumed: true, Closed: false}, popErr
		}
		return PickerToggleResult{Consumed: false, Closed: false}, openErr
	}

	// Open succeeded. Check if close was requested during open.
	if consumeToggleOpening(client) {
		linesAfter, err := f.listOwnedPickerPanes(server, req.Context.pane)
		if err != nil {
			return PickerToggleResult{Consumed: true}, fmt.Errorf("list newly opened picker: %w", err)
		}
		for _, l := range linesAfter {
			parts := strings.Split(l, "\t")
			if len(parts) >= 4 && parts[1] == "1" && parts[2] == "1" && parts[3] == client {
				if err := cleanupFailedFloatingPaneWithRunner(server, parts[0], f.commandRunner()); err != nil {
					return PickerToggleResult{Consumed: true}, err
				}
			}
		}
		_ = lastSessionSwitch()
		return PickerToggleResult{Consumed: true, Closed: true}, nil
	}

	return PickerToggleResult{Consumed: true, Closed: false}, nil
}

func (f *floatingBackend) listOwnedPickerPanes(server tmuxServerSpec, pane string) ([]string, error) {
	args := []string{"list-panes", "-a"}
	if f.modal {
		args = []string{"list-panes", "-t", pane}
	}
	args = append(args, "-F", "#{pane_id}\t#{pane_floating_flag}\t#{@tmux_qs}\t#{@tmux_qs_client}\t#{@tmux_qs_instance}")
	return f.commandRunner().runLines(server, args...)
}
