package main

import (
	"testing"
	"time"
)

func TestOutputPreviewSignalScopesPaneAndServer(t *testing.T) {
	m := model{}
	server := getTmuxServer()
	info := waitingInfo{
		outputServer:  server.cacheKey(),
		activePanes:   map[string]string{"dev": "%1", "dev-eink": "%2"},
		outputSignals: map[string]string{"%1": "generation-1", "%2": "generation-2", "%3": "generation-3"},
	}
	before := m.outputPreviewSignal(info, "dev")
	info.outputSignals["%3"] = "generation-4"
	if m.outputPreviewSignal(info, "dev") != before {
		t.Fatal("unrelated output invalidated session preview")
	}
	info.outputSignals["%2"] = "generation-5"
	if m.outputPreviewSignal(info, "dev") == before {
		t.Fatal("grouped e-ink pane output did not invalidate session preview")
	}
	if got := m.outputPreviewSignal(info, "display\tdev\t%3"); got != "generation-4" {
		t.Fatalf("exact-pane signal = %q", got)
	}
	info.outputServer = "another-server"
	if got := m.outputPreviewSignal(info, "display\tdev\t%3"); got != "" {
		t.Fatalf("foreign server reused pane signal: %q", got)
	}
}

func TestWatcherOutputInvalidatesPreviewCacheAndRejectsStaleReply(t *testing.T) {
	withCleanCacheEnv(t)
	server := getTmuxServer()
	restore := setCapabilitiesForTest(server, TmuxCapabilities{})
	t.Cleanup(restore)
	before := waitingInfo{
		outputServer:  server.cacheKey(),
		activePanes:   map[string]string{"dev": "%1", "other": "%2"},
		outputSignals: map[string]string{"%1": "old", "%2": "other-old"},
	}
	m := model{
		src: srcTmux, width: 100, height: 24,
		items: []string{"dev", "other"}, filtered: []int{0, 1},
		waiting: before, previewEntry: "dev", previewContent: "old preview",
		previewRequest: 2, loadGeneration: 1,
		previewCache: map[string]previewCacheEntry{
			"dev":   {content: "old preview", at: time.Now()},
			"other": {content: "other preview", at: time.Now()},
		},
	}
	after := waitingInfo{
		outputServer: server.cacheKey(), activePanes: before.activePanes,
		outputSignals: map[string]string{"%1": "new", "%2": "other-new"},
	}
	updated, _ := m.Update(watchMsg{info: after})
	got := updated.(model)
	if got.previewRequest != 3 || len(got.previewCache) != 0 {
		t.Fatalf("output did not invalidate selected and cached previews: request=%d cache=%v", got.previewRequest, got.previewCache)
	}
	updated, _ = got.Update(previewMsg{generation: 1, request: 2, entry: "dev", content: "stale"})
	got = updated.(model)
	if got.previewContent == "stale" || len(got.previewCache) != 0 {
		t.Fatal("old preview reply overwrote new request")
	}
	updated, _ = got.Update(previewMsg{generation: 1, request: 3, entry: "dev", content: "fresh"})
	got = updated.(model)
	if got.previewContent != "fresh" {
		t.Fatal("current preview reply was rejected")
	}
	updated, _ = got.Update(watchMsg{info: after})
	if unchanged := updated.(model); unchanged.previewRequest != got.previewRequest {
		t.Fatal("unchanged output repeatedly scheduled preview work")
	}
}

func TestWaitingPreviewSignalTracksAgentEventDetails(t *testing.T) {
	before := waitingInfo{panes: []waitingPane{{session: "dev", paneID: "%1", signal: "event", state: "approval needed"}}}
	after := waitingInfo{panes: []waitingPane{{session: "dev", paneID: "%1", signal: "event", state: "turn complete", summary: "done"}}}
	if waitingPreviewSignal(before, "dev", nil) == waitingPreviewSignal(after, "dev", nil) {
		t.Fatal("new event detail did not invalidate preview")
	}
}
