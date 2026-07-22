# Gamepad Agent & Shell Optimizations Design Spec

Date: 2026-07-22
Status: Approved

## Overview

This specification details the optimizations in `tmux-qs` to facilitate seamless control of Coding Agents (Claude, Codex, Crush, OpenCode, etc.) and Shell sessions using standard Gamepads without requiring physical/virtual keyboards or voice input.

All existing Gamepad hardware button mappings are strictly preserved:
- **Left Stick Up/Down**: Navigate list cursor
- **Left Stick Left/Right**: Send Arrow Left/Right to target pane
- **Right Stick**: Mouse pointer
- **Left Mouse Click**: Select list row / click tabs
- **Mouse Wheel**: Scroll list up/down
- **X**: Backspace (delete input char)
- **Y**: Space (Open snippet list; in snippet list, send selected item and keep list open)
- **A**: Enter (Connect / Confirm / Send selected snippet and close list)
- **B**: Esc (Back / Exit TUI)
- **Extra Button**: Tab (Switch view between sessions and windows/panes)

## Core Architectural Changes

### 1. Smart Extraction Engine (`snippets.go`)

When the Snippet picker opens (`startSnippetPickerForTarget`), `tmux-qs` already captures a 20-30 line snapshot of the target pane (`m.snippetPreview`). The Smart Extraction Engine parses this preview snapshot for contextual shortcuts:

1. **Interactive Prompt Responses**:
   - Matches regex patterns such as `(?i)\[y/n\]`, `(?i)\[yes/no\]`, `(y/n)`, `1) ... 2) ... 3) ...`.
   - Generates top-priority transient snippets:
     - `Response: y` (`Submit: true`, `Favorite: true`)
     - `Response: n` (`Submit: true`, `Favorite: true`)
     - `Response: 1`, `Response: 2`, `Response: 3` (`Submit: true`, `Favorite: true`)
2. **Error & Test Failure Extraction**:
   - Matches failure lines such as `FAIL: Test<Name>` or `Error: <Summary>`.
   - Generates quick fix snippets: `Fix: Test<Name>` or `Inspect error: <Summary>` (`Submit: true`, `Favorite: true`).
3. **Clipboard Integration**:
   - Inspects system clipboard via `getClipboard()`.
   - If non-empty, generates a `Paste Clipboard: "<preview text>"` snippet (`Submit: false`).

### 2. Composable Prompt Blocks (Prompt Builder)

Snippet items support building complex prompts iteratively using the **Y button** (`Submit: false` snippets).

Built-in Block Snippets:
- **Action Prefixes** (`Submit: false`):
  - `Fix: `
  - `Explain: `
  - `Refactor: `
  - `Review: `
- **Target Subjects** (`Submit: false`):
  - `the failing test cases`
  - `the latest error traceback`
  - `the recent git changes`
- **Execution Suffixes** (`Submit: true`):
  - ` and run tests`
  - ` and create git commit`

**Usage Pattern**:
1. User presses **Y** to open Snippet list.
2. Selects `Fix: ` and presses **Y** (space sends text to pane, picker stays open).
3. Selects `the failing test cases` and presses **Y** (appends text to pane, picker stays open).
4. Selects ` and run tests` and presses **A** (appends text, sends Enter, closes picker).

### 3. Snippet Categorization & Filtering (`sources.go`, `ui.go`)

To avoid long vertical scrolling with the left analog stick:
- Snippets are grouped into dynamic categories: `All`, `Quick` (Extracted/Responses), `Agent`, `Shell`, `Blocks`.
- Horizontal category headers rendered at top of the Snippet picker.
- Mouse left-click or Left Stick Left/Right (when focused on category tab bar) switches category filtering.

## Verification & Testing Plan

1. **Unit Tests**:
   - `snippets_test.go`: Test regex parsing for `[y/N]`, `FAIL:`, and error lines.
   - `snippets_test.go`: Test ranking and category filtering of snippets.
   - `config_test.go`: Verify default block snippets and fallback behavior.
2. **Integration Verification**:
   - `rtk go test ./... -skip 'PTY|Teatest|VimNormalTabMovesCursor'` must pass cleanly.
   - Format with `rtk gofmt -w`.
