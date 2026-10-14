package main

// All metadata and output signals are retained. Only waiting-detection
// captures are restricted; a selected unpinned pane still has a live preview.
func watchCapturesPane(k paneKey, self paneKey, opts WatchingConfig, pinned map[string]bool) bool {
	return k != self && !isEinkSessionName(k.session) && (!opts.PinnedOnly || pinned[k.session])
}

func waitingOrderingChanged(before, after waitingInfo) bool {
	return waitingMembershipChanged(before.bySession, after.bySession) || waitingMembershipChanged(before.byPath, after.byPath)
}

func waitingMembershipChanged(before, after map[string][]procCount) bool {
	for key, counts := range before {
		if (len(counts) > 0) != (len(after[key]) > 0) {
			return true
		}
	}
	for key, counts := range after {
		if (len(counts) > 0) != (len(before[key]) > 0) {
			return true
		}
	}
	return false
}
