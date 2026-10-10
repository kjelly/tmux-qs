package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

func topologyLines(lines []string) string {
	slices.Sort(lines)
	return fmt.Sprintf("%d\n%s", len(lines), strings.Join(lines, "\n"))
}

// Source signatures use the same metadata pass as waiting collection.
// Sessions depend on names, rooted paths and counts, not transient pane cwd
// or foreground commands. Pane sources include their displayed fields.
func watchSourceTopologiesFromLines(lines []string) (map[sourceKind]string, string) {
	sessions := map[string]sessionInfo{}
	var panes, tmuxPanes []string
	server := ""
	for _, line := range lines {
		p := strings.Split(line, "\t")
		if len(p) < 28 {
			continue
		}
		server = p[19]
		internal := isInternalTmuxQSPane(p[13])
		if !isEinkSessionName(p[0]) {
			si := sessions[p[0]]
			si.path = p[26]
			si.windows, _ = strconv.Atoi(p[27])
			if !internal {
				si.panes++
			}
			sessions[p[0]] = si
		}
		if internal {
			continue
		}
		row := strings.Join([]string{p[0], p[25], p[2], p[9], p[4], p[3]}, "\t")
		panes = append(panes, row)
		if !isEinkSessionName(p[0]) {
			tmuxPanes = append(tmuxPanes, row+"\t"+p[10])
		}
	}
	var rows []string
	for name, si := range sessions {
		rows = append(rows, fmt.Sprintf("%s\t%s\t%d\t%d", name, si.path, si.windows, si.panes))
	}
	sessionSignal := topologyLines(rows)
	return map[sourceKind]string{srcDefault: sessionSignal, srcAll: sessionSignal, srcTmux: topologyLines(tmuxPanes), srcPanes: topologyLines(panes)}, server
}

// Capture the baseline before loading rows. A topology change during a load
// can cause one correction, but cannot silently become the new baseline.
func loadWatchSourceTopology(src sourceKind) string {
	if src != srcDefault && src != srcAll && src != srcTmux && src != srcPanes {
		return ""
	}
	if loadConfig().toWatchingConfig().DisableEvents {
		return ""
	}
	endpoints := []tmuxServerEndpoint{{spec: getTmuxServer()}}
	if allServersMode && (src == srcDefault || src == srcAll) {
		endpoints = watchEndpoints()
	}
	var parts []string
	for _, endpoint := range endpoints {
		if !getTmuxCapabilities(endpoint.spec).EventMonitors {
			continue
		}
		lines, err := tmuxRunLinesOn(endpoint.spec, "list-panes", "-a", "-F", watchPaneMetadataFormat())
		if err != nil {
			return ""
		}
		signals, _ := watchSourceTopologiesFromLines(lines)
		if len(endpoints) == 1 && endpoint.label == "" {
			return signals[src]
		}
		parts = append(parts, endpoint.label+"|"+signals[src])
	}
	if len(parts) == 0 {
		return ""
	}
	return topologyLines(parts)
}
