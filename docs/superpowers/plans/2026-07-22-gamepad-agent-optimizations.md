# Gamepad Agent & Shell Optimizations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enhance `tmux-qs` to support zero-typing and minimal-typing operation for Coding Agents (Claude, Codex, Crush, etc.) and Shells via standard Gamepad without altering hardware button mappings.

**Architecture:** 
1. Implement a Smart Extraction Engine in `snippets.go` that inspects the pane preview text (`m.snippetPreview`) for interactive prompt prompts (`[y/N]`, `1) 2) 3)`), error traces (`FAIL:`, `Error:`), and system clipboard text to dynamically generate top-ranked transient response snippets.
2. Add Composable Prompt Block snippets (`Submit: false`) to `defaultSnippets` allowing multi-step prompt composition using the gamepad **Y** button (Space).
3. Add snippet category tabs (`All`, `Quick`, `Agent`, `Shell`, `Blocks`) to `sources.go` / `ui.go` / `snippets.go` for gamepad and mouse filtering.

**Tech Stack:** Go, Bubble Tea (`github.com/charmbracelet/bubbletea`), Lip Gloss (`github.com/charmbracelet/lipgloss`), tmux CLI.

## Global Constraints

- Preserve all existing Gamepad mappings (Left Stick navigation/arrows, Right Stick mouse, X Backspace, Y Space, A Enter, B Esc, Extra Tab).
- Run tests via `rtk go test ./... -skip 'PTY|Teatest|VimNormalTabMovesCursor'`.
- Format code via `rtk gofmt -w <file>`.

---

### Task 1: Smart Extraction Engine

**Files:**
- Modify: `snippets.go`
- Test: `snippets_test.go`

**Interfaces:**
- Produces: `extractContextSnippets(preview string, clipboard string, command string) []SnippetConfig`

- [ ] **Step 1: Write failing unit test for context extraction**

Edit `snippets_test.go` to test `extractContextSnippets`:

```go
func TestExtractContextSnippets(t *testing.T) {
	preview := `
Building package...
Do you want to proceed? [y/N]
FAIL: TestRun
`
	clipboard := "git checkout main"
	snippets := extractContextSnippets(preview, clipboard, "claude")

	names := make([]string, len(snippets))
	for i, s := range snippets {
		names[i] = s.Name
	}

	foundY := false
	foundFix := false
	foundClip := false
	for _, n := range names {
		if strings.Contains(n, "Response: y") {
			foundY = true
		}
		if strings.Contains(n, "Fix: TestRun") {
			foundFix = true
		}
		if strings.Contains(n, "Paste Clipboard") {
			foundClip = true
		}
	}

	if !foundY || !foundFix || !foundClip {
		t.Fatalf("expected y, fix, and clipboard snippets, got: %v", names)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./... -run TestExtractContextSnippets`
Expected: FAIL ("extractContextSnippets undefined")

- [ ] **Step 3: Implement `extractContextSnippets` in `snippets.go`**

Add implementation to `snippets.go`:

```go
func extractContextSnippets(preview string, clipboard string, command string) []SnippetConfig {
	var out []SnippetConfig

	lines := strings.Split(preview, "\n")
	for _, line := range lines {
		lineTrim := strings.TrimSpace(line)
		if lineTrim == "" {
			continue
		}
		// Match y/n prompt
		if strings.Contains(strings.ToLower(lineTrim), "[y/n]") || strings.Contains(strings.ToLower(lineTrim), "(y/n)") {
			out = append(out,
				SnippetConfig{Name: "Quick Response: y", Text: "y", Submit: true, Favorite: true},
				SnippetConfig{Name: "Quick Response: n", Text: "n", Submit: true, Favorite: true},
			)
		}
		// Match FAIL: TestName
		if idx := strings.Index(lineTrim, "FAIL: "); idx != -1 {
			testName := strings.Fields(lineTrim[idx+6:])[0]
			out = append(out, SnippetConfig{
				Name:     "Fix: " + testName,
				Text:     "fix failing test " + testName + " and rerun tests",
				Submit:   true,
				Favorite: true,
			})
		}
	}

	if clipboard = strings.TrimSpace(clipboard); clipboard != "" && len(clipboard) < 100 {
		disp := clipboard
		if len(disp) > 30 {
			disp = disp[:27] + "..."
		}
		out = append(out, SnippetConfig{
			Name:   "Paste Clipboard: " + disp,
			Text:   clipboard,
			Submit: false,
		})
	}

	return out
}
```

And integrate `extractContextSnippets` into `startSnippetPickerForTarget` in `snippets.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `rtk go test ./... -run TestExtractContextSnippets`
Expected: PASS

- [ ] **Step 5: Commit Task 1**

```bash
git add snippets.go snippets_test.go
git commit -m "feat: add smart extraction engine for snippet picker"
```

---

### Task 2: Composable Prompt Blocks

**Files:**
- Modify: `snippets.go`
- Test: `snippets_test.go`

**Interfaces:**
- Produces: Block Snippets (`Submit: false`) in `defaultSnippets()`

- [ ] **Step 1: Write failing test for block snippets**

Add test in `snippets_test.go`:

```go
func TestDefaultSnippetsIncludeBlocks(t *testing.T) {
	snippets := defaultSnippets()
	hasBlock := false
	for _, s := range snippets {
		if strings.HasPrefix(s.Name, "Block: ") {
			hasBlock = true
			break
		}
	}
	if !hasBlock {
		t.Fatalf("expected defaultSnippets to contain Block: items")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./... -run TestDefaultSnippetsIncludeBlocks`
Expected: FAIL ("expected defaultSnippets to contain Block: items")

- [ ] **Step 3: Update `defaultSnippets()` in `snippets.go`**

Add Composable Blocks into `defaultSnippets()`:

```go
	block := func(name, value string) SnippetConfig {
		return SnippetConfig{Name: "Block: " + name, Text: value, Submit: false}
	}
	all = append(all,
		block("Prefix: Fix", "Fix "),
		block("Prefix: Explain", "Explain "),
		block("Prefix: Review", "Review "),
		block("Subject: Recent error", "the recent error and logs "),
		block("Subject: Failing tests", "the failing test suite "),
		block("Subject: Git diff", "the current git diff changes "),
		block("Suffix: and run tests", "and run the test suite"),
	)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `rtk go test ./... -run TestDefaultSnippetsIncludeBlocks`
Expected: PASS

- [ ] **Step 5: Commit Task 2**

```bash
git add snippets.go snippets_test.go
git commit -m "feat: add composable prompt block snippets"
```

---

### Task 3: Category Filtering in Snippet Picker

**Files:**
- Modify: `snippets.go`, `ui.go`
- Test: `snippets_test.go`

**Interfaces:**
- Produces: Snippet Category tab filter support (`snippetCategory` state in `model`)

- [ ] **Step 1: Write failing test for category filtering**

Add test in `snippets_test.go`:

```go
func TestFilterSnippetsByCategory(t *testing.T) {
	snippets := []SnippetConfig{
		{Name: "Quick Response: y", Text: "y"},
		{Name: "Block: Prefix Fix", Text: "Fix "},
		{Name: "Continue", Commands: []string{"claude"}},
	}
	quick := filterSnippetsByCategory(snippets, "Quick")
	if len(quick) != 1 || quick[0].Name != "Quick Response: y" {
		t.Fatalf("expected 1 Quick snippet, got: %v", quick)
	}

	blocks := filterSnippetsByCategory(snippets, "Blocks")
	if len(blocks) != 1 || blocks[0].Name != "Block: Prefix Fix" {
		t.Fatalf("expected 1 Block snippet, got: %v", blocks)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./... -run TestFilterSnippetsByCategory`
Expected: FAIL ("filterSnippetsByCategory undefined")

- [ ] **Step 3: Implement category filtering in `snippets.go` and update `ui.go`**

Implement `filterSnippetsByCategory` in `snippets.go`:

```go
func filterSnippetsByCategory(snippets []SnippetConfig, category string) []SnippetConfig {
	if category == "" || category == "All" {
		return snippets
	}
	var out []SnippetConfig
	for _, s := range snippets {
		switch category {
		case "Quick":
			if strings.HasPrefix(s.Name, "Quick Response:") || strings.HasPrefix(s.Name, "Fix:") || strings.HasPrefix(s.Name, "Paste Clipboard:") {
				out = append(out, s)
			}
		case "Blocks":
			if strings.HasPrefix(s.Name, "Block:") {
				out = append(out, s)
			}
		case "Agent":
			if len(s.Commands) > 0 && !strings.HasPrefix(s.Name, "Block:") {
				out = append(out, s)
			}
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `rtk go test ./... -run TestFilterSnippetsByCategory`
Expected: PASS

- [ ] **Step 5: Commit Task 3**

```bash
git add snippets.go snippets_test.go ui.go
git commit -m "feat: add category tab filtering for snippet picker"
```

---

### Task 4: Documentation & Regression Verification

**Files:**
- Modify: `help.go`, `README.md`
- Test: All tests via `rtk go test ./... -skip 'PTY|Teatest|VimNormalTabMovesCursor'`

- [ ] **Step 1: Update `help.go` and `README.md`**

Document the new smart extraction engine and composable prompt blocks features under Gamepad operation guidance in `help.go` and `README.md`.

- [ ] **Step 2: Run code formatting**

Run: `rtk gofmt -w .`

- [ ] **Step 3: Run full test suite**

Run: `rtk go test ./... -skip 'PTY|Teatest|VimNormalTabMovesCursor'`
Expected: ALL PASS

- [ ] **Step 4: Commit Task 4**

```bash
git add help.go README.md
git commit -m "docs: update help and README with gamepad optimization details"
```
