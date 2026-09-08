# Session Recency Sorting Design

## Goal

Order every list row that represents a running tmux session by tmux's
`#{session_activity}` timestamp, newest first.

## Scope

- Apply the same session identity lookup to All, Waiting, Config, Tmux,
  Panes, and Windows sources.
- Treat the current session like every other running session; do not force it
  to the last row.
- Keep Files and Commands in their source order because they do not represent
  tmux sessions.
- Preserve existing pinned and waiting priority tiers, then sort inside each
  tier by session activity.

## Design

Introduce one helper that extracts the associated tmux session name from a
list item. Plain session rows keep their existing name, all-server rows drop
their server prefix, and pane-envelope rows use their second tab-separated
field. All recency and session-group checks use this helper, so display text
cannot prevent the lookup in `m.sessionInfo`.

The final sorter will only apply session-recency ordering to sources whose
rows represent sessions. For non-session sources it preserves the loader's
stable order. Regression tests cover pane-envelope sorting and the current
session being ordered by activity rather than forced to the bottom.
