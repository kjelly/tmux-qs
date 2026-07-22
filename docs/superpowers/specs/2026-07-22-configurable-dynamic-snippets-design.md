# Configurable Dynamic Snippets Design Spec

Date: 2026-07-22
Status: Approved

## Overview

This specification extends `tmux-qs` to support user-configurable dynamic snippets in `config.toml`. Users can define pattern-matching rules on active pane output (`m.snippetPreview`) to dynamically insert context-specific snippet choices into the picker menu.

## TOML Configuration Schema

In `config.toml`:

```toml
[[dynamic_snippet]]
name = "Inspect Error: $1"
matches = ["錯誤", "error", "FAIL:\\s+(\\w+)"]
text = "inspect error: $1"
commands = ["claude", "codex"] # optional
submit = true
favorite = true
disabled = false
```

### Fields
- **`name`**: Display label. Supports capture variables (`$1`, `$2`, etc.) when `matches` uses regex capture groups.
- **`matches`**: Array of pattern strings (supports plain substring or regex). If any pattern matches a line in the pane output, the dynamic snippet is generated for that line.
- **`text`**: Command/text sent to target pane. Supports capture variables (`$1`, `$2`, etc.).
- **`commands`**: Optional array of foreground commands to restrict rule execution (e.g., `["claude", "zsh"]`). Empty means all commands.
- **`submit`**: Boolean. If true, auto-appends `Enter` after sending text.
- **`favorite`**: Boolean. If true, ranks near top.
- **`disabled`**: Boolean. If true, disables the rule (useful for overriding default dynamic rules).

## Default Dynamic Snippets

Default dynamic rules built into `tmux-qs`:
1. `matches = ["(?i)\\[y/n\\]", "(?i)\\(y/n\\)", "(?i)\\[yes/no\\]"]` -> `Quick Response: y` and `Quick Response: n`
2. `matches = ["FAIL:\\s+(\\w+)", "FAILED:\\s+(\\w+)"]` -> `Fix: $1`
3. System clipboard rule (if non-empty) -> `Paste Clipboard: <text>`

Users can override or disable default dynamic snippets by defining `[[dynamic_snippet]]` with `disabled = true`.

## Data Structures & Engine Changes

### `config.go`
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

### `snippets.go`
- Update `extractContextSnippets(preview string, clipboard string, command string, rules []DynamicSnippetConfig) []SnippetConfig`.
- For each line in `preview`:
  - Evaluate user-defined rules and default rules against `command` and line content.
  - Compile regex for each pattern in `Matches`. If matched, expand `$1`, `$2` in `Name` and `Text` via `re.ReplaceAllString(line, text)`.
  - Append generated snippet (preventing duplicate names/texts).

## Verification & Testing Plan

1. **Unit Tests (`snippets_test.go`, `config_test.go`)**:
   - Parse `[[dynamic_snippet]]` from TOML bytes (testing single/multiple `matches`).
   - Test `extractContextSnippets` with multi-string `matches` (`"錯誤"`, `"error"`).
   - Test regex capture group substitution (`$1`).
2. **Regression Suite**:
   - `rtk go test ./... -skip 'PTY|Teatest|VimNormalTabMovesCursor'` must pass cleanly.
