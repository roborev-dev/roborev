# Design: Inline Fix Panel in Review View

Date: 2026-02-23

## Summary

When viewing a review, the user can trigger a fix without leaving the review context. A fix input panel appears at the bottom of the review view, with the review content remaining visible and scrollable above it. The user can Tab between the review (scroll mode) and fix panel (input mode).

This replaces the existing full-screen `tuiViewFixPrompt` modal, which is removed.

## State Changes

Two new fields on `tuiModel`:

- `reviewFixPanelOpen bool` — true when the fix panel is visible
- `reviewFixPanelFocused bool` — true when keyboard focus is on the fix panel
- `reviewFixPanelPending bool` — set when 'F' is pressed from queue; causes the panel to open focused as soon as the review loads

Existing fields reused: `fixPromptText`, `fixPromptJobID`.

`tuiViewFixPrompt` constant and `renderFixPromptView()` / `handleFixPromptKey()` are removed.

## Entry Points

**'F' from queue view**: validates the job (completed, not a fix job), then fetches the review (navigates to `tuiViewReview`) with `reviewFixPanelPending = true`. When the review loads, the pending flag is consumed: `reviewFixPanelOpen` and `reviewFixPanelFocused` are set to true.

**'F' from review view**: validates the current review's job (completed, not a fix job), then sets `reviewFixPanelOpen = true`, `reviewFixPanelFocused = true`, `fixPromptJobID` from the job.

## Rendering

`renderReviewView()` checks `reviewFixPanelOpen`. When open, the available height is split:

- Review content region: `height - fixPanelHeight` lines (where `fixPanelHeight` = 4: top border, input line, bottom border, help line)
- Fix panel region: rendered below

**Fix panel — focused** (fix panel has keyboard input):
```
┌─ Fix: enter instructions (or leave blank for default) ──────────────┐
│ > custom prompt text_                                               │
└──────────────────────────────────────────────────────────────────────┘
tab: scroll review | enter: submit | esc: cancel
```

**Fix panel — unfocused** (review has keyboard input):
```
┌─ Fix (Tab to focus) ────────────────────────────────────────────────┐
│   custom prompt text                                                │
└──────────────────────────────────────────────────────────────────────┘
```

The border title changes to indicate focus state. When fix panel is focused, the review's scroll help line is dimmed to indicate focus has moved.

## Key Handling

In `handleKeyMsg()`, a new top-level condition is added before the view switch:

```
if m.currentView == tuiViewReview && m.reviewFixPanelOpen && m.reviewFixPanelFocused
```

This routes to a dedicated `handleReviewFixPanelKey()` handler for printable input, backspace, enter, tab, esc.

When fix panel is **focused**:
- Printable chars → append to `fixPromptText`
- `backspace` → delete last rune
- `enter` → call `triggerFix(fixPromptJobID, fixPromptText)`, close panel, navigate to tasks view
- `tab` → `reviewFixPanelFocused = false` (shift focus to review)
- `esc` → close panel, clear all fix panel state

When fix panel is **open but unfocused** (review has focus):
- `tab` → `reviewFixPanelFocused = true`
- `esc` → close panel entirely (clear `reviewFixPanelOpen`, `reviewFixPanelFocused`, `fixPromptText`, `fixPromptJobID`)
- All other keys → normal review scroll/nav behaviour

## Removals

- `tuiViewFixPrompt` constant removed from the `tuiView` iota
- `renderFixPromptView()` removed
- `handleFixPromptKey()` removed
- `fixPromptFromView` field removed (no longer needed; fix panel always returns to review/tasks)
- `handleFixKey()` updated: queue branch now fetches review with pending flag instead of opening the old modal

## Tests

- Update `tui_review_test.go`: add tests for fix panel open/focused state, Tab focus toggle, enter submit, esc close
- Update `tui_action_test.go`: verify 'F' from queue navigates to review with panel pending
- Remove tests for `tuiViewFixPrompt`
