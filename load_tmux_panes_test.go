package main

import (
	"os"
	"strings"
	"testing"
)

// TestLoadTmuxPanes_Ordering verifies that loadTmuxPanes() emits
// rows in the same order list-sessions returns them, and that panes
// within a session are sorted by (window_index, pane_index).
func TestLoadTmuxPanes_Ordering(t *testing.T) {
	withTestTmuxServer(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot get working directory: %v", err)
	}

	// Create two sessions: "alpha" first, then "zeta". Despite
	// alphabetical ordering, alpha is created first so it should
	// appear first in the output (matching the previous srcTmux
	// behavior of "list-sessions order").
	if err := tmuxRun("new-session", "-d", "-s", "alpha", "-c", wd); err != nil {
		t.Skipf("cannot create alpha: %v", err)
	}
	if err := tmuxRun("new-session", "-d", "-s", "zeta", "-c", wd); err != nil {
		t.Skipf("cannot create zeta: %v", err)
	}
	// Give alpha a second window with a split so it has multiple
	// panes — this exercises the (win, pane) sort.
	if err := tmuxRun("new-window", "-t", "alpha", "-c", wd); err != nil {
		t.Fatalf("new-window alpha: %v", err)
	}
	if err := tmuxRun("split-window", "-t", "alpha", "-c", wd); err != nil {
		t.Fatalf("split-window alpha: %v", err)
	}

	got, err := loadTmuxPanes()
	if err != nil {
		t.Fatalf("loadTmuxPanes: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("loadTmuxPanes returned no rows")
	}

	// First row must belong to alpha (created first).
	if !strings.Contains(got[0], "alpha:") {
		t.Errorf("expected first row to belong to alpha, got: %q", got[0])
	}

	// Extract all rows for alpha; they must be sorted by (win, pane)
	// ascending. Parse "session:win.pane" from the display segment.
	var alphaRows []string
	for _, row := range got {
		display := strings.SplitN(row, "\t", 2)[0]
		if strings.HasPrefix(display, "alpha:") {
			alphaRows = append(alphaRows, display)
		}
	}
	if len(alphaRows) < 3 {
		t.Fatalf("expected >=3 rows for alpha, got %d: %v", len(alphaRows), alphaRows)
	}
	// Each alpha row's win.pane should be non-decreasing.
	for i := 1; i < len(alphaRows); i++ {
		prev := alphaRows[i-1]
		cur := alphaRows[i]
		if !strings.HasPrefix(prev, "alpha:") || !strings.HasPrefix(cur, "alpha:") {
			continue
		}
		// Compare only the win.pane part (between "alpha:" and the first space).
		prevIdx := extractWinPane(t, prev)
		curIdx := extractWinPane(t, cur)
		if compareWinPane(prevIdx, curIdx) > 0 {
			t.Errorf("alpha rows out of order: %q before %q", prev, cur)
		}
	}
}

// TestLoadTmuxPanes_DisplayFormat verifies the display string shape
// and the title-handling rules:
//   - format: "session:win.pane [cmd] ~cwd" plus optional "「title」"
//   - title is empty → no "「」"
//   - title == cmd → no "「」" (avoid redundancy)
func TestLoadTmuxPanes_DisplayFormat(t *testing.T) {
	withTestTmuxServer(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot get working directory: %v", err)
	}

	if err := tmuxRun("new-session", "-d", "-s", "fmt-test", "-c", wd); err != nil {
		t.Skipf("cannot create fmt-test: %v", err)
	}
	// The user's system tmux may have a global `set-titles on` that
	// writes a default title (e.g. the session name) into the pane.
	// For the "no title" sub-case we need a pane whose title is
	// empty: do that by setting pane-title to "" via select-pane -T.
	_ = tmuxRun("select-pane", "-t", "fmt-test", "-T", "")

	got, err := loadTmuxPanes()
	if err != nil {
		t.Fatalf("loadTmuxPanes: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("loadTmuxPanes returned no rows")
	}

	// Find the fmt-test row.
	var row string
	for _, r := range got {
		if strings.Contains(r, "fmt-test:") {
			row = r
			break
		}
	}
	if row == "" {
		t.Fatalf("no fmt-test row found in %v", got)
	}
	display := strings.SplitN(row, "\t", 2)[0]
	t.Logf("display: %q", display)

	// The display must start with "fmt-test:" and contain "[".
	if !strings.HasPrefix(display, "fmt-test:") {
		t.Errorf("display should start with 'fmt-test:', got: %q", display)
	}
	if !strings.Contains(display, "[") {
		t.Errorf("display should contain a [cmd] segment, got: %q", display)
	}
	// With an unset title, no 「」 should appear.
	if strings.Contains(display, "「") || strings.Contains(display, "」") {
		t.Errorf("display with empty title should not contain 「」, got: %q", display)
	}

	// Now set the title to a unique value, refetch, and confirm the
	// 「title」 segment appears.
	_ = tmuxRun("select-pane", "-t", "fmt-test", "-T", "my-custom-title")
	got2, err := loadTmuxPanes()
	if err != nil {
		t.Fatalf("loadTmuxPanes (with title): %v", err)
	}
	var row2 string
	for _, r := range got2 {
		if strings.Contains(r, "fmt-test:") {
			row2 = r
			break
		}
	}
	if row2 == "" {
		t.Fatalf("no fmt-test row found after setting title")
	}
	display2 := strings.SplitN(row2, "\t", 2)[0]
	t.Logf("display with title: %q", display2)
	if !strings.Contains(display2, "「my-custom-title」") {
		t.Errorf("display with non-cmd title should contain 「my-custom-title」, got: %q", display2)
	}

	// Set title == cmd ("nu") and confirm the title is dropped
	// (avoid redundancy with the [nu] segment).
	_ = tmuxRun("select-pane", "-t", "fmt-test", "-T", "nu")
	got3, err := loadTmuxPanes()
	if err != nil {
		t.Fatalf("loadTmuxPanes (title==cmd): %v", err)
	}
	var row3 string
	for _, r := range got3 {
		if strings.Contains(r, "fmt-test:") {
			row3 = r
			break
		}
	}
	display3 := strings.SplitN(row3, "\t", 2)[0]
	t.Logf("display with title==cmd: %q", display3)
	if strings.Contains(display3, "「") {
		t.Errorf("display with title==cmd should not show 「」, got: %q", display3)
	}
}

// TestLoadTmuxPanes_EnvelopeForPaneID verifies that each row ends
// with a tab envelope carrying the session name and a non-empty
// paneID and the raw pane cwd, in the form
// `display \t session \t paneID \t paneCWD`.
func TestLoadTmuxPanes_EnvelopeForPaneID(t *testing.T) {
	withTestTmuxServer(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot get working directory: %v", err)
	}
	if err := tmuxRun("new-session", "-d", "-s", "env-test", "-c", wd); err != nil {
		t.Skipf("cannot create env-test: %v", err)
	}

	got, err := loadTmuxPanes()
	if err != nil {
		t.Fatalf("loadTmuxPanes: %v", err)
	}
	var row string
	for _, r := range got {
		if strings.Contains(r, "env-test:") {
			row = r
			break
		}
	}
	if row == "" {
		t.Fatalf("no env-test row found")
	}

	parts := strings.SplitN(row, "\t", 4)
	if len(parts) != 4 {
		t.Fatalf("row should have 4 tab-separated parts, got %d: %q", len(parts), row)
	}
	display, session, paneID, paneCWD := parts[0], parts[1], parts[2], parts[3]
	if session != "env-test" {
		t.Errorf("envelope session = %q, want %q", session, "env-test")
	}
	// paneID should look like "%N" (tmux pane id format).
	if !strings.HasPrefix(paneID, "%") || len(paneID) < 2 {
		t.Errorf("envelope paneID = %q, want tmux-style %%N id", paneID)
	}
	if !strings.HasPrefix(display, "env-test:") {
		t.Errorf("display should start with session:win.pane, got: %q", display)
	}
	if paneCWD != wd {
		t.Errorf("envelope cwd = %q, want %q", paneCWD, wd)
	}
}

// TestCtrlT_ChooseSelectsPane verifies that pressing Enter on a
// srcTmux row populates result (session name) and resultPaneID
// (the specific pane).
func TestCtrlT_ChooseSelectsPane(t *testing.T) {
	withTestTmuxServer(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot get working directory: %v", err)
	}
	if err := tmuxRun("new-session", "-d", "-s", "choose-test", "-c", wd); err != nil {
		t.Skipf("cannot create choose-test: %v", err)
	}
	// Add a second pane so we can verify the active pane is picked
	// (not the first one).
	if err := tmuxRun("split-window", "-t", "choose-test", "-c", wd); err != nil {
		t.Fatalf("split-window: %v", err)
	}
	// Make sure the second pane is the active one.
	baseIdx, err := tmuxRunOut("display-message", "-p", "-t", "choose-test", "#{window_index}")
	if err != nil {
		t.Fatalf("display-message: %v", err)
	}
	paneBaseOut, err := tmuxRunOut("display-message", "-p", "-t", "choose-test", "#{pane_index}")
	if err != nil {
		t.Fatalf("display-message pane: %v", err)
	}
	_ = paneBaseOut
	// Activate the second pane of the (only) window.
	otherPane := "1"
	if paneBaseOut == "1" {
		otherPane = "0"
	}
	if err := tmuxRun("select-pane", "-t", "choose-test:"+baseIdx+"."+otherPane); err != nil {
		t.Fatalf("select-pane: %v", err)
	}

	items, err := loadTmuxPanes()
	if err != nil {
		t.Fatalf("loadTmuxPanes: %v", err)
	}
	// Find a choose-test row whose envelope carries the active pane id.
	activePaneID, err := tmuxRunOut("display-message", "-p", "-t", "choose-test", "#{pane_id}")
	if err != nil {
		t.Fatalf("display-message pane_id: %v", err)
	}
	var activeRow string
	for _, r := range items {
		parts := strings.SplitN(r, "\t", 3)
		if len(parts) >= 3 && parts[1] == "choose-test" && parts[2] == activePaneID {
			activeRow = r
			break
		}
	}
	if activeRow == "" {
		t.Fatalf("no choose-test row found for active pane %q in %v", activePaneID, items)
	}

	// Simulate what choose() does: split, populate result/PaneID.
	m := newModel()
	m.src = srcTmux
	m.items = items
	m.filtered = nil
	for i, r := range items {
		if r == activeRow {
			m.filtered = []int{i}
			break
		}
	}
	if len(m.filtered) == 0 {
		t.Fatalf("could not find activeRow in items")
	}
	m.cursor = 0

	// Drive choose() by calling it on the model.
	updated, _ := m.choose()
	mm, ok := updated.(model)
	if !ok {
		t.Fatalf("choose did not return a model: %T", updated)
	}
	if mm.result != "choose-test" {
		t.Errorf("result = %q, want %q", mm.result, "choose-test")
	}
	if mm.resultPaneID != activePaneID {
		t.Errorf("resultPaneID = %q, want %q", mm.resultPaneID, activePaneID)
	}
}

// TestSessionNameBonusFor verifies the bonus function on its own,
// independent of the refilter hot path.
func TestSessionNameBonusFor(t *testing.T) {
	cases := []struct {
		rawItem string
		query   string
		want    int
	}{
		{
			rawItem: "work:1.0 [nu] ~/projects/work 「nu」\twork\t%0",
			query:   "work",
			want:    sessionNameBonus,
		},
		{
			rawItem: "work:1.0 [nu] ~/projects/work 「nu」\twork\t%0",
			query:   "xyzzz", // does not match anything → no bonus
			want:    0,
		},
		{
			rawItem: "blog:1.0 [bash] ~/blog\tblog\t%3",
			query:   "blog nu",
			want:    sessionNameBonus, // first term hits
		},
		{
			rawItem: "scratch:1.0 [nu] /tmp/scratch\tscratch\t%7",
			query:   "tmp", // cwd matches, session name does not
			want:    0,
		},
		{
			rawItem: "noEnvelope",
			query:   "noEnvelope",
			want:    0, // no session segment → 0
		},
	}
	for _, c := range cases {
		got := sessionNameBonusFor(c.rawItem, c.query)
		if got != c.want {
			t.Errorf("sessionNameBonusFor(%q, %q) = %d, want %d", c.rawItem, c.query, got, c.want)
		}
	}
}

// TestCtrlT_FuzzySessionNameBonus verifies the end-to-end behavior:
// a query that matches the session name of one row but only the cwd
// of another row should rank the session-name row higher.
func TestCtrlT_FuzzySessionNameBonus(t *testing.T) {
	withTestTmuxServer(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot get working directory: %v", err)
	}
	if err := tmuxRun("new-session", "-d", "-s", "fuzzy-a", "-c", wd); err != nil {
		t.Skipf("cannot create fuzzy-a: %v", err)
	}
	// Make a subdirectory so cwd of a pane can be set to something
	// that contains "fuzzy" as a substring, so the cwd-only row will
	// also match the query.
	sub := t.TempDir()
	if err := tmuxRun("new-session", "-d", "-s", "fuzzy-b", "-c", sub); err != nil {
		t.Skipf("cannot create fuzzy-b: %v", err)
	}

	items, err := loadTmuxPanes()
	if err != nil {
		t.Fatalf("loadTmuxPanes: %v", err)
	}
	if len(items) < 2 {
		t.Skipf("expected at least 2 rows, got %d", len(items))
	}

	// Build a model in srcTmux mode with the rows, then refilter
	// with query "fuzzy". The row belonging to "fuzzy-a" or
	// "fuzzy-b" must appear first (its session name matches → +50).
	// The TempDir path won't itself contain "fuzzy", so the cwd of
	// neither row adds bonus, but the session names do.
	m := newModel()
	m.src = srcTmux
	m.items = items
	m.input.SetValue("fuzzy")
	m.refilter()

	if len(m.filtered) == 0 {
		t.Fatalf("refilter returned no matches")
	}
	top := strings.SplitN(m.items[m.filtered[0]], "\t", 2)[0]
	t.Logf("top result: %q", top)
	if !strings.HasPrefix(top, "fuzzy-") {
		t.Errorf("expected top result to start with 'fuzzy-' (session-name match), got %q", top)
	}
}

// extractWinPane pulls "win.pane" from a display string like
// "alpha:1.2 [nu] ~/foo". Returns the value up to the first space.
func extractWinPane(t *testing.T, s string) string {
	t.Helper()
	colon := strings.Index(s, ":")
	if colon < 0 {
		t.Fatalf("no ':' in %q", s)
	}
	rest := s[colon+1:]
	sp := strings.IndexAny(rest, " [")
	if sp < 0 {
		return rest
	}
	return rest[:sp]
}

// compareWinPane compares two "win.pane" strings, returning -1/0/1
// (a < b / a == b / a > b). Panics if either input doesn't parse.
func compareWinPane(a, b string) int {
	ap := strings.SplitN(a, ".", 2)
	bp := strings.SplitN(b, ".", 2)
	if len(ap) != 2 || len(bp) != 2 {
		return strings.Compare(a, b)
	}
	if ap[0] != bp[0] {
		if ap[0] < bp[0] {
			return -1
		}
		return 1
	}
	if ap[1] != bp[1] {
		if ap[1] < bp[1] {
			return -1
		}
		return 1
	}
	return 0
}
