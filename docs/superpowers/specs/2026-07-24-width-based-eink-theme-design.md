# Width-based E-ink theme design

## Goal

Make `~/bin/tmux-set-background`, `tmux-qs`, and Neovim select the same
terminal theme from the current tmux client width:

- Use a light theme when the width exactly matches a configured E-ink width.
- Use a dark theme for every other width.
- Do not use `LC_IS_EINK` to select the theme.

The initial E-ink widths are `167` and `165`. More widths can be added without
editing all three consumers.

## Shared configuration

The tmux global user option `@eink-widths` is the single source of truth. It is
a comma-separated list of positive integer widths:

```tmux
set-option -g @eink-widths "167,165"
```

Consumers trim whitespace, ignore invalid and empty entries, and compare widths
exactly. If the option is missing or contains no valid widths, they use the
built-in default `167,165`. This keeps startup deterministic while allowing the
list to be changed in one place.

The existing `--eink` command and `*-eink` grouped-session names no longer
select a theme. They retain their existing session-management behavior.

## Components

### `~/bin/tmux-set-background`

The script initializes `@eink-widths` to `167,165` only when the option is
missing. It reads all connected clients and selects the palette independently
for each client by exact width membership.

Matching clients receive the existing white E-ink terminal and tmux palette.
All other clients receive the existing dark terminal and tmux palette. The
script removes its `LC_IS_EINK` lookup and the current `<= 200` heuristic.

### `tmux-qs`

`tmux-qs` reads the current target client's `client_width` and the global
`@eink-widths` option. Width membership directly produces the light/dark
decision. It no longer consults:

- `LC_IS_EINK` from the process or tmux environment,
- the `*-eink` session suffix,
- `EINK_WIDTH`, or
- `window-style` as a theme source.

`TMUX_QS_THEME=light|dark` remains an explicit application-level override.
Without that override, the TUI checks at startup, immediately after resize, and
on its existing two-second polling interval so an already-running interface
repaints after a client moves or resizes.

Width parsing and theme selection are isolated in small functions and covered
by unit tests. Existing adaptive Lip Gloss colors remain unchanged.

### Neovim

Neovim reads the current tmux client width and `@eink-widths` at startup and on
`VimResized`. It sets `background=light` for a match and
`background=dark` otherwise, allowing both directions of a live transition.
The existing `vscode-eink` colorscheme and transparent-background refresh
continue to follow the `background` option.

Theme selection no longer uses `LC_IS_EINK`, `COLORFGBG`, or `EINK_WIDTH`.
Outside tmux, no client width can match, so Neovim selects the dark theme.
Failures to query tmux are handled the same way: select dark and do not block
startup.

## Documentation

`README.md` and `help.go` describe:

- `@eink-widths` and its default,
- exact width matching,
- the dark fallback, and
- the fact that `LC_IS_EINK` and `*-eink` no longer select a theme.

No gamepad, keyboard, mouse, or snippet behavior changes.

## Verification

Implementation follows test-driven development for the Go behavior:

1. Add failing unit tests for option parsing, exact membership, invalid values,
   missing-option fallback, and removal of session/environment shortcuts.
2. Implement the smallest Go change that makes those tests pass.
3. Run formatting and the project-prescribed unit test command.

Integration verification uses an isolated tmux server. Clients or test queries
at widths `167` and `165` must select light; representative widths such as
`166`, `168`, and `220` must select dark. The script's resulting tmux styles
and the TUI's resolved theme are inspected. Neovim is launched headlessly
inside the isolated tmux environment to verify that startup and resize checks
set both light and dark backgrounds.

External configuration files are backed up before editing, and unrelated
uncommitted repository changes are preserved.
