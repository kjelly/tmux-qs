package main

import (
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type TmuxVersion struct {
	Major  int
	Minor  int
	Suffix string
	Raw    string
}

type TmuxCapabilities struct {
	Version            TmuxVersion
	ProgressBar        bool
	ClipboardQuery     bool
	GetClipboardOption bool
	GetClipboardMode   string
	FloatingPane       bool
}

var versionRegex = regexp.MustCompile(`^(\d+)\.(\d+)(.*)$`)

func parseTmuxVersion(raw string) TmuxVersion {
	rawTrimmed := strings.TrimSpace(raw)
	v := TmuxVersion{Raw: rawTrimmed}
	if rawTrimmed == "" {
		return v
	}
	matches := versionRegex.FindStringSubmatch(rawTrimmed)
	if len(matches) == 4 {
		maj, _ := strconv.Atoi(matches[1])
		min, _ := strconv.Atoi(matches[2])
		v.Major = maj
		v.Minor = min
		v.Suffix = matches[3]
	}
	return v
}

func (s tmuxServerSpec) cacheKey() string {
	if s.flag == "" || s.value == "" {
		return "default"
	}
	if s.flag == "-L" {
		return "L\x00" + s.value
	}
	if s.flag == "-S" {
		return "S\x00" + filepath.Clean(s.value)
	}
	return s.flag + "\x00" + s.value
}

type capabilityRunner interface {
	runLines(server tmuxServerSpec, args ...string) ([]string, error)
	runOut(server tmuxServerSpec, args ...string) (string, error)
}

type realCapabilityRunner struct{}

func (realCapabilityRunner) runLines(server tmuxServerSpec, args ...string) ([]string, error) {
	return tmuxRunLinesOn(server, args...)
}

func (realCapabilityRunner) runOut(server tmuxServerSpec, args ...string) (string, error) {
	return tmuxRunOutOn(server, args...)
}

type capabilityEntry struct {
	caps TmuxCapabilities
	err  error
	done chan struct{}
}

var (
	capabilitiesMu       sync.Mutex
	capabilitiesCache    = make(map[string]TmuxCapabilities)
	capabilitiesInFlight = make(map[string]*capabilityEntry)
)

func resetCapabilityCacheForTest() {
	capabilitiesMu.Lock()
	defer capabilitiesMu.Unlock()
	capabilitiesCache = make(map[string]TmuxCapabilities)
	capabilitiesInFlight = make(map[string]*capabilityEntry)
}

func setCapabilitiesForTest(server tmuxServerSpec, caps TmuxCapabilities) func() {
	capabilitiesMu.Lock()
	defer capabilitiesMu.Unlock()
	key := server.cacheKey()
	prev, hadPrev := capabilitiesCache[key]
	capabilitiesCache[key] = caps
	return func() {
		capabilitiesMu.Lock()
		defer capabilitiesMu.Unlock()
		if hadPrev {
			capabilitiesCache[key] = prev
		} else {
			delete(capabilitiesCache, key)
		}
	}
}

func getTmuxCapabilities(server tmuxServerSpec) TmuxCapabilities {
	return getTmuxCapabilitiesWithRunner(server, realCapabilityRunner{})
}

func getTmuxCapabilitiesWithRunner(server tmuxServerSpec, runner capabilityRunner) TmuxCapabilities {
	key := server.cacheKey()

	capabilitiesMu.Lock()
	if caps, ok := capabilitiesCache[key]; ok {
		capabilitiesMu.Unlock()
		return caps
	}

	entry, inFlight := capabilitiesInFlight[key]
	if inFlight {
		capabilitiesMu.Unlock()
		<-entry.done
		return entry.caps
	}

	entry = &capabilityEntry{done: make(chan struct{})}
	capabilitiesInFlight[key] = entry
	capabilitiesMu.Unlock()

	caps, err := probeServerCapabilitiesWithRunner(server, runner)

	capabilitiesMu.Lock()
	delete(capabilitiesInFlight, key)
	if err == nil {
		capabilitiesCache[key] = caps
	}
	capabilitiesMu.Unlock()

	entry.caps = caps
	entry.err = err
	close(entry.done)

	return caps
}

var validProgressStates = map[string]bool{
	"hidden":        true,
	"normal":        true,
	"error":         true,
	"indeterminate": true,
	"paused":        true,
}

var validClipboardModes = map[string]bool{
	"off":     true,
	"buffer":  true,
	"request": true,
	"both":    true,
}

func probeServerCapabilitiesWithRunner(server tmuxServerSpec, runner capabilityRunner) (TmuxCapabilities, error) {
	var caps TmuxCapabilities

	// 1. Pane, version, and progress base probe
	lines, err := runner.runLines(server, "list-panes", "-a", "-F", "#{pane_id}\t#{version}\t#{pane_pb_state}")
	if err != nil {
		return caps, err
	}

	var parsedRow bool
	for _, row := range lines {
		// Do not trim entire row beforehand so trailing empty fields are not stripped.
		rowClean := strings.TrimRight(row, "\r\n")
		parts := strings.Split(rowClean, "\t")
		if len(parts) >= 3 {
			caps.Version = parseTmuxVersion(parts[1])
			pbState := strings.TrimSpace(parts[2])
			if validProgressStates[pbState] {
				caps.ProgressBar = true
			}
			parsedRow = true
			break
		}
	}
	if !parsedRow {
		return caps, errors.New("base probe failed: no valid list-panes row found")
	}

	// 2. Command base probe
	cmdLines, err := runner.runLines(server, "list-commands")
	if err != nil {
		return caps, err
	}

	for _, line := range cmdLines {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		cmdName := fields[0]

		if cmdName == "new-pane" {
			caps.FloatingPane = true
		}

		if cmdName == "refresh-client" {
			for _, f := range fields[1:] {
				if strings.HasPrefix(f, "[-") && strings.HasSuffix(f, "]") {
					flags := f[2 : len(f)-1]
					if strings.ContainsRune(flags, 'l') {
						caps.ClipboardQuery = true
						break
					}
				}
			}
		}
	}

	// 4. Clipboard option
	optOut, err := runner.runOut(server, "show-options", "-sv", "get-clipboard")
	if err == nil {
		mode := strings.TrimSpace(optOut)
		if validClipboardModes[mode] {
			caps.GetClipboardOption = true
			caps.GetClipboardMode = mode
		}
	}

	return caps, nil
}
