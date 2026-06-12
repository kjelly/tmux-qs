package main

// helpText is the static help overlay shown in modeHelp. It is split
// into a "list of bindings" the user can scan, plus a hint on how to
// dismiss. Kept short and aligned so it reads well inside a popup.
var helpText = []string{
	"  tmux-qs · key bindings",
	"",
	"  ↑ / ↓             move cursor",
	"  Tab / Shift-Tab   move cursor",
	"  Ctrl-n / Ctrl-p   move cursor",
	"  Ctrl-j / Ctrl-k   move cursor",
	"  Alt-j / Alt-k     jump to next/prev tmux session",
	"",
	"  Enter             connect to selected entry",
	"  Alt-Enter         send input-box text to session (tmux send-keys)",
	"  Alt-n             new empty session",
	"  Ctrl-r            rename selected session (uses input box)",
	"  Ctrl-d            kill selected tmux session (press twice to confirm)",
	"  Ctrl-b            enter branch mode (git)",
	"  Ctrl-y            copy entry's directory to clipboard (OSC 52)",
	"  Ctrl-Space        toggle per-pane detail panel",
	"  Esc               exit / leave branch or help mode",
	"  Ctrl-c            quit",
	"",
	"  Ctrl-a            all running tmux sessions",
	"  Ctrl-w            sessions with waiting agents",
	"  Ctrl-t / Ctrl-s   tmux sessions",
	"  Ctrl-g            user-defined sessions (config.toml)",
	"  Ctrl-x            zoxide directories",
	"  Alt-r             zoxide under current session root",
	"  Ctrl-f            scan ~ (depth 2)",
	"",
	"  ?                 toggle this help",
}
