# Width-based E-ink theme design

## Goal

Make `tmux-qs` and Neovim select the same terminal theme from the current tmux
client width, and move the existing `~/bin/tmux-set-background` behavior into
a non-interactive `tmux-qs` command:

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

### `tmux-qs`

`tmux-qs theme apply` is a non-interactive command intended for tmux hooks. It
initializes `@eink-widths` to `167,165` only when the option is missing, reads
all connected clients, and selects the palette independently for each client by
exact width membership. It exits after applying the theme and never starts the
popup or TUI.

Matching clients receive the existing white E-ink terminal and tmux palette.
All other clients receive the existing dark terminal and tmux palette.

The interactive `tmux-qs` TUI uses the same width parser and membership
function. It reads the current target client's `client_width` and the global
`@eink-widths` option. Width membership directly produces the light/dark
decision. Neither command consults:

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

### tmux hooks and legacy script

The existing hooks in `~/.tmux.conf` continue to run on client attach, session
change, resize, client focus, and pane focus, but call:

```tmux
run-shell "tmux-qs theme apply"
```

instead of `~/bin/tmux-set-background`.

The legacy script is retained until the new command and hooks pass integration
verification. It is then backed up and removed so there is one implementation
of the behavior. If `tmux-qs` is unavailable, the hook fails without changing
the existing tmux state; it must not start an interactive process.

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

- the `tmux-qs theme apply` command and hook configuration,
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
`166`, `168`, and `220` must select dark. The command's resulting tmux styles
and the TUI's resolved theme are inspected. It must also be verified that
`tmux-qs theme apply` exits without opening a popup or TUI. Neovim is launched
headlessly inside the isolated tmux environment to verify that startup and
resize checks set both light and dark backgrounds.

External configuration files are backed up before editing. The legacy
`~/bin/tmux-set-background` script is removed only after its replacement and
updated hooks are verified. Unrelated uncommitted repository changes are
preserved.
