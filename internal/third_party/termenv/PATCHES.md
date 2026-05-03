# Local patches to muesli/termenv

This directory contains a vendored copy of
[`github.com/muesli/termenv`](https://github.com/muesli/termenv) at upstream
version `v0.16.0`. It is wired into the build via a `replace` directive in the
top-level `go.mod`:

```
replace github.com/muesli/termenv => ./internal/third_party/termenv
```

The vendoring exists to apply local patches that the upstream maintainers have
not adopted. Do not edit these files unless you are intentionally maintaining
the patch set.

## Why we vendor

`roborev` is a single binary that contains both a TUI (`roborev tui`) and many
non-TUI subcommands (`roborev remap`, `roborev post-commit`, `roborev --help`,
etc.). The TUI package transitively imports `bubbletea`, `lipgloss`, and
`termenv`, so package-level initialization of those dependencies runs even for
non-TUI invocations.

When `roborev`'s `post-rewrite` git hook fires during a rebase, it runs
`roborev remap --quiet`. Inside `tmux` with `TERM=xterm-256color`, upstream
`termenv` will probe the terminal for its background color (OSC 11) and
cursor position (`CSI 6 n`) before any roborev code dispatches. tmux happily
forwards both query and response, but in some setups (notably nested tmux
under `superset` / `kata`) the response bytes leak past tmux into the parent
shell, which displays them as garbage:

```
^[]11;rgb:1515/1111/1010^[\
^[[18;91R
```

Splitting the binary or removing the TUI is not acceptable, and gating the
TUI imports behind build tags would forfeit the single-binary distribution
model.

## What the patch does

`termenv_unix.go` already short-circuits `termStatusReport` when `TERM` starts
with `screen`, `tmux`, or `dumb`, but that check does not fire when tmux is
configured to advertise `TERM=xterm-256color` (a common setup for true-color
support).

Our patch adds an additional guard: whenever the `TMUX` environment variable
is set, `termStatusReport` returns `ErrStatusReport` without writing anything
to the TTY. `TMUX` is set automatically by tmux for every process inside a
session, so this reliably suppresses the probe inside tmux without affecting
non-tmux terminals.

The change is localized to the top of `termStatusReport` in `termenv_unix.go`
and is marked with a `// roborev patch:` comment so it is easy to find and
re-apply on upstream upgrades.

## How to upgrade

1. Drop the new upstream tarball into this directory, replacing every file
   except `PATCHES.md`.
2. Re-apply the `// roborev patch:` block in `termenv_unix.go`.
3. Update the TMUX guard test in `termenv_unix_tmux_test.go` if the
   surrounding code drifted.
4. Run `go test ./internal/third_party/termenv/...` to confirm.
