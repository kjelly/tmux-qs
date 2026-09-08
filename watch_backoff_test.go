package main

import (
	"testing"
	"time"
)

func TestIsAllowedPaneRecognizesRuntimeWrapperTitle(t *testing.T) {
	opts := WatchingConfig{Commands: []string{"cline", "gemini"}}
	if !isAllowedPane(paneState{cmd: "node", title: "cline --resume"}, opts) {
		t.Fatal("node wrapper with exact cline title token should be allowed")
	}
	if !isAllowedPane(paneState{cmd: "python3", title: "gemini"}, opts) {
		t.Fatal("python wrapper with exact gemini title token should be allowed")
	}
	if isAllowedPane(paneState{cmd: "node", title: "not-a-cline-command"}, opts) {
		t.Fatal("title substring must not enable an unrelated pane")
	}
}

func TestNextWatchDelay(t *testing.T) {
	opts := WatchingConfig{Poll: 5 * time.Second}

	cases := []struct {
		name string
		errs int
		want time.Duration
	}{
		{"no errors uses configured poll", 0, 5 * time.Second},
		{"negative errors still uses poll", -1, 5 * time.Second},
		{"first error uses initial backoff", 1, watchInitialBackoff},
		{"second error doubles", 2, watchInitialBackoff * watchBackoffMult},
		{"third error quadruples", 3, watchInitialBackoff * watchBackoffMult * watchBackoffMult},
		{"huge error count caps at max", 100, watchMaxBackoff},
		{"boundary: error count that just hits cap", -1, watchMaxBackoff}, // overwritten below
	}
	// The last case is a placeholder; compute the actual boundary dynamically.
	for i := range cases {
		if cases[i].name == "boundary: error count that just hits cap" {
			cases[i].name = "boundary: error count that just hits cap"
			// find the smallest n where delay hits cap
			for n := 1; ; n++ {
				d := watchInitialBackoff
				for j := 1; j < n; j++ {
					d *= watchBackoffMult
					if d >= watchMaxBackoff {
						d = watchMaxBackoff
						break
					}
				}
				if d >= watchMaxBackoff {
					cases[i].errs = n
					cases[i].want = watchMaxBackoff
					break
				}
			}
		}
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextWatchDelay(opts, c.errs)
			if got != c.want {
				t.Errorf("nextWatchDelay(errs=%d) = %v, want %v", c.errs, got, c.want)
			}
		})
	}
}
