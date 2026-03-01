# Log View: Mouse Copy/Paste and Stderr Word-Wrap

Issue: https://github.com/roborev-dev/roborev/issues/396

## Problem

1. **Mouse copy/paste**: `tea.WithMouseCellMotion()` captures all mouse events,
   preventing terminal text selection in the log view. Users cannot copy log output.

2. **Stderr word-wrap**: Non-JSON lines (agent stderr) are passed through without
   word-wrapping, causing long lines to extend beyond the terminal width.

## Fix 1: Disable mouse capture in log view

Release mouse capture on entering the log view so the terminal handles text
selection natively. Re-enable when leaving.

### Changes

- `openLogView()`: add `tea.DisableMouse` to the `tea.Batch` call
- `handleLogKey()` esc/q exit: return `tea.EnableMouseCellMotion` command
- Left/right navigation stays in log view, so `openLogView` keeps mouse disabled
- Keyboard scrolling (arrows, pgup/pgdn, g/G) continues to work

**Trade-off**: Mouse wheel scrolling is unavailable in the log view. Keyboard
scroll (up/down/pgup/pgdn) remains fully functional.

## Fix 2: Word-wrap stderr lines

Wrap non-JSON lines in `RenderLogWith()` using the formatter's terminal width.

### Changes

- Add `Width() int` method to `Formatter`
- In `RenderLogWith()`, call `WrapText(line, fmtr.Width())` for non-JSON lines
  and write each wrapped line separately
