# Configurable Dynamic Snippets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable user-configurable pattern-matching rules (`[[dynamic_snippet]]`) in `config.toml` to dynamically generate context-aware snippets from pane preview output, supporting multi-string matches (`matches = ["錯誤", "error"]`) and regex capture groups (`$1`, `$2`).

**Architecture:** 
1. Define `DynamicSnippetConfig` struct in `config.go` and extend `Config` to parse `[[dynamic_snippet]]` array from TOML.
2. Update `extractContextSnippets` in `snippets.go` to iterate over custom and default dynamic snippet rules, matching line text against `matches` patterns and substituting capture variables (`$1`, `$2`).
3. Connect `m.cfg().DynamicSnippets` in `startSnippetPickerForTarget` and add documentation in `README.md`.

**Tech Stack:** Go, TOML (`github.com/pelletier/go-toml/v2`), Bubble Tea, Go `regexp` package.

## Global Constraints

- Preserve all existing Gamepad mappings and static snippet behaviors.
- Run tests via `rtk go test ./... -skip 'PTY|Teatest|VimNormalTabMovesCursor'`.
- Format code via `rtk gofmt -w <file>`.

---

### Task 1: TOML Configuration for Dynamic Snippets

**Files:**
- Modify: `config.go`
- Test: `config_test.go`

**Interfaces:**
- Produces: `DynamicSnippetConfig` struct and `Config.DynamicSnippets` field in `config.go`.

- [ ] **Step 1: Write failing unit test for dynamic snippet config parsing**

Add test in `config_test.go`:

```go
func TestParseDynamicSnippetsFromTOML(t *testing.T) {
	tomlData := []byte(`
[[dynamic_snippet]]
name = "Check Error: $1"
matches = ["錯誤", "error", "FAIL:\\s+(\\w+)"]
text = "inspect error $1"
submit = true
favorite = true
`)
	cfg, err := parseConfigBytes(tomlData)
	if err != nil {
		t.Fatalf("failed to parse TOML: %v", err)
	}
	if len(cfg.DynamicSnippets) != 1 {
		t.Fatalf("expected 1 dynamic snippet rule, got %d", len(cfg.DynamicSnippets))
	}
	rule := cfg.DynamicSnippets[0]
	if rule.Name != "Check Error: $1" || len(rule.Matches) != 3 || rule.Matches[0] != "錯誤" || !rule.Submit || !rule.Favorite {
		t.Fatalf("unexpected dynamic snippet rule: %#v", rule)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestParseDynamicSnippetsFromTOML`
Expected: FAIL ("cfg.DynamicSnippets undefined")

- [ ] **Step 3: Implement `DynamicSnippetConfig` in `config.go`**

Add struct to `config.go`:

```go
type DynamicSnippetConfig struct {
	Name     string   `toml:"name"`
	Matches  []string `toml:"matches"`
	Text     string   `toml:"text"`
	Commands []string `toml:"commands"`
	Submit   bool     `toml:"submit"`
	Favorite bool     `toml:"favorite"`
	Disabled bool     `toml:"disabled"`
}
```

And add `DynamicSnippets []DynamicSnippetConfig` to `Config` struct in `config.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TestParseDynamicSnippetsFromTOML`
Expected: PASS

- [ ] **Step 5: Commit Task 1**

```bash
git add config.go config_test.go
git commit -m "feat: add DynamicSnippetConfig struct and TOML parsing"
```

---

### Task 2: Dynamic Pattern Match Engine with Regex Captures

**Files:**
- Modify: `snippets.go`
- Test: `snippets_test.go`

**Interfaces:**
- Produces: `extractContextSnippets(preview string, clipboard string, command string, rules []DynamicSnippetConfig) []SnippetConfig`

- [ ] **Step 1: Write failing unit test for multi-string and regex capture dynamic snippets**

Add test in `snippets_test.go`:

```go
func TestExtractContextSnippetsWithCustomRules(t *testing.T) {
	preview := `
Line 1: 發生系統錯誤
Line 2: FAIL: TestAuth
`
	rules := []DynamicSnippetConfig{
		{
			Name:    "檢視: $1",
			Matches: []string{"錯誤", "error"},
			Text:    "inspect error $1",
			Submit:  true,
		},
		{
			Name:    "Fix Test: $1",
			Matches: []string{"FAIL:\\s+(\\w+)"},
			Text:    "fix $1",
			Submit:  true,
		},
	}

	snippets := extractContextSnippets(preview, "", "zsh", rules)
	foundError := false
	foundFix := false
	for _, s := range snippets {
		if strings.HasPrefix(s.Name, "檢視:") {
			foundError = true
		}
		if s.Name == "Fix Test: TestAuth" && s.Text == "fix TestAuth" {
			foundFix = true
		}
	}
	if !foundError || !foundFix {
		t.Fatalf("expected custom error and fix snippets, got: %#v", snippets)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestExtractContextSnippetsWithCustomRules`
Expected: FAIL ("too many arguments in call to extractContextSnippets")

- [ ] **Step 3: Update `extractContextSnippets` in `snippets.go`**

Update `extractContextSnippets` signature and implementation in `snippets.go`:

```go
func extractContextSnippets(preview string, clipboard string, command string, rules []DynamicSnippetConfig) []SnippetConfig {
	var out []SnippetConfig
	seen := make(map[string]bool)

	// Combine default rules with custom user rules
	allRules := defaultDynamicSnippetRules()
	allRules = append(allRules, rules...)

	lines := strings.Split(preview, "\n")
	for _, rule := range allRules {
		if rule.Disabled {
			continue
		}
		if len(rule.Commands) > 0 && !snippetMatchesCommand(SnippetConfig{Commands: rule.Commands}, command) {
			continue
		}
		for _, pat := range rule.Matches {
			if strings.TrimSpace(pat) == "" {
				continue
			}
			re, err := regexp.Compile(pat)
			isRegex := err == nil

			for _, line := range lines {
				lineTrim := strings.TrimSpace(line)
				if lineTrim == "" {
					continue
				}

				matched := false
				var name, text string

				if isRegex && re.MatchString(lineTrim) {
					matched = true
					name = re.ReplaceAllString(lineTrim, rule.Name)
					text = re.ReplaceAllString(lineTrim, rule.Text)
				} else if strings.Contains(strings.ToLower(lineTrim), strings.ToLower(pat)) {
					matched = true
					name = rule.Name
					text = rule.Text
				}

				if matched && name != "" {
					key := name + "|" + text
					if !seen[key] {
						seen[key] = true
						out = append(out, SnippetConfig{
							Name:     name,
							Text:     text,
							Submit:   rule.Submit,
							Favorite: rule.Favorite,
						})
					}
				}
			}
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

func defaultDynamicSnippetRules() []DynamicSnippetConfig {
	return []DynamicSnippetConfig{
		{
			Name:    "Quick Response: y",
			Matches: []string{"(?i)\\[y/n\\]", "(?i)\\(y/n\\)", "(?i)\\[yes/no\\]"},
			Text:    "y",
			Submit:  true, Favorite: true,
		},
		{
			Name:    "Quick Response: n",
			Matches: []string{"(?i)\\[y/n\\]", "(?i)\\(y/n\\)", "(?i)\\[yes/no\\]"},
			Text:    "n",
			Submit:  true, Favorite: true,
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TestExtractContextSnippetsWithCustomRules`
Expected: PASS

- [ ] **Step 5: Commit Task 2**

```bash
git add snippets.go snippets_test.go
git commit -m "feat: implement regex capture and multi-string dynamic snippet matching"
```

---

### Task 3: Connect Model Config & Verification

**Files:**
- Modify: `snippets.go`
- Test: `snippets_test.go`

- [ ] **Step 1: Connect `m.cfg().DynamicSnippets` in `startSnippetPickerForTarget`**

In `snippets.go`, pass `m.cfg().DynamicSnippets` to `extractContextSnippets` in `startSnippetPickerForTarget`.

- [ ] **Step 2: Run all snippet unit tests**

Run: `go test ./... -run Snippet`
Expected: PASS

- [ ] **Step 3: Commit Task 3**

```bash
git add snippets.go
git commit -m "feat: pass dynamic snippet config rules into picker model"
```

---

### Task 4: Documentation & Regression Suite

**Files:**
- Modify: `README.md`
- Test: All tests via `rtk go test ./... -skip 'PTY|Teatest|VimNormalTabMovesCursor'`

- [ ] **Step 1: Document `[[dynamic_snippet]]` in `README.md`**

Add TOML config example and explanation for `[[dynamic_snippet]]` in `README.md`.

- [ ] **Step 2: Format code**

Run: `rtk gofmt -w .`

- [ ] **Step 3: Run full test suite**

Run: `rtk go test ./... -skip 'PTY|Teatest|VimNormalTabMovesCursor'`
Expected: ALL PASS

- [ ] **Step 4: Commit Task 4**

```bash
git add README.md
git commit -m "docs: document [[dynamic_snippet]] configuration in README"
```
