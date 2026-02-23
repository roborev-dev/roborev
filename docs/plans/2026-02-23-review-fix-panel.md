# Review Fix Panel Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the full-screen fix prompt modal with an inline split-pane fix panel that appears at the bottom of the review view, keeping the review content visible and scrollable above it.

**Architecture:** Add `reviewFixPanelOpen`, `reviewFixPanelFocused`, and `reviewFixPanelPending` booleans to `tuiModel`. The review renderer checks these flags to split its height budget, rendering a boxed fix input area below the review content. Tab toggles focus between panels; 'F' from queue fetches the review and sets the pending flag so the panel opens automatically on load. Remove `tuiViewFixPrompt`, `renderFixPromptView()`, `handleFixPromptKey()`, and `fixPromptFromView`.

**Tech Stack:** Go, charmbracelet/bubbletea TUI, lipgloss, tui.go / tui_handlers.go

---

### Task 1: Add model fields and remove old state

**Files:**
- Modify: `cmd/roborev/tui.go:262-270` (fix task state block in `tuiModel`)

**Step 1: Add the three new boolean fields and remove `fixPromptFromView`**

In `tuiModel`, find the fix task state block (around line 264):
```go
// Fix task state
fixJobs           []storage.ReviewJob
fixSelectedIdx    int
fixPromptText     string
fixPromptJobID    int64
fixPromptFromView tuiView  // <-- REMOVE THIS
fixShowHelp       bool
patchText         string
patchScroll       int
patchJobID        int64
```

Replace with:
```go
// Fix task state
fixJobs              []storage.ReviewJob
fixSelectedIdx       int
fixPromptText        string
fixPromptJobID       int64
fixShowHelp          bool
patchText            string
patchScroll          int
patchJobID           int64

// Inline fix panel (review view)
reviewFixPanelOpen    bool // true when fix panel is visible in review view
reviewFixPanelFocused bool // true when keyboard focus is on the fix panel
reviewFixPanelPending bool // true when 'F' from queue; panel opens on review load
```

**Step 2: Remove `tuiViewFixPrompt` from the view iota**

In `tui.go` around line 112, find:
```go
tuiViewTasks     // Background fix tasks view
tuiViewFixPrompt // Fix prompt confirmation modal
tuiViewPatch     // Patch viewer for fix jobs
```

Change to:
```go
tuiViewTasks // Background fix tasks view
tuiViewPatch // Patch viewer for fix jobs
```

**Step 3: Build to confirm it compiles (will fail — fix remaining references next)**

```bash
cd /Users/mvanniekerk/.superset/worktrees/roborev/review-fix
go build ./... 2>&1 | head -40
```

Expected: compile errors referencing `tuiViewFixPrompt`, `fixPromptFromView`, `renderFixPromptView`, `handleFixPromptKey`.

---

### Task 2: Remove `tuiViewFixPrompt` references from `tui.go`

**Files:**
- Modify: `cmd/roborev/tui.go`

**Step 1: Remove the `tuiViewFixPrompt` branch from `View()`**

Find in `View()` (around line 2177):
```go
if m.currentView == tuiViewFixPrompt {
    return m.renderFixPromptView()
}
```
Delete these two lines.

**Step 2: Delete `renderFixPromptView()`**

Find and delete the entire function (around line 3667–3689):
```go
// renderFixPromptView renders the fix prompt confirmation modal.
func (m tuiModel) renderFixPromptView() string {
    ...
}
```

**Step 3: Build**

```bash
go build ./... 2>&1 | head -40
```

Expected: errors in `tui_handlers.go` only.

---

### Task 3: Remove `tuiViewFixPrompt` references from `tui_handlers.go`

**Files:**
- Modify: `cmd/roborev/tui_handlers.go`

**Step 1: Remove `tuiViewFixPrompt` case from `handleKeyMsg()`**

Find (around line 24):
```go
case tuiViewFixPrompt:
    return m.handleFixPromptKey(msg)
```
Delete these two lines.

**Step 2: Delete `handleFixPromptKey()`**

Find and delete the entire function (around line 1431–1463):
```go
// handleFixPromptKey handles key input in the fix prompt confirmation modal.
func (m tuiModel) handleFixPromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    ...
}
```

**Step 3: Update `handleFixKey()` — queue branch**

Find `handleFixKey()` (around line 1374). The current queue branch (inside `if m.currentView == tuiViewQueue`) ends with:
```go
// Open fix prompt modal
m.fixPromptJobID = job.ID
m.fixPromptText = "" // Empty means use default prompt from server
m.fixPromptFromView = m.currentView
m.currentView = tuiViewFixPrompt
return m, nil
```

Replace with:
```go
// Fetch the review and open the inline fix panel when it loads
m.fixPromptJobID = job.ID
m.fixPromptText = ""
m.reviewFixPanelPending = true
m.reviewFromView = tuiViewQueue
return m, m.fetchReview(job.ID)
```

**Step 4: Update `handleFixKey()` — review branch**

The current review branch (inside `if m.currentView == tuiViewReview`) ends with:
```go
// Open fix prompt modal
m.fixPromptJobID = job.ID
m.fixPromptText = "" // Empty means use default prompt from server
m.fixPromptFromView = m.currentView
m.currentView = tuiViewFixPrompt
return m, nil
```

Replace with:
```go
// Open inline fix panel within review view
m.fixPromptJobID = job.ID
m.fixPromptText = ""
m.reviewFixPanelOpen = true
m.reviewFixPanelFocused = true
return m, nil
```

**Step 5: Build**

```bash
go build ./... 2>&1 | head -40
```

Expected: clean build.

**Step 6: Run tests**

```bash
go test ./cmd/roborev/... 2>&1 | tail -20
```

Expected: some tests may fail if they reference `tuiViewFixPrompt`. Note which ones.

---

### Task 4: Consume `reviewFixPanelPending` on review load

**Files:**
- Modify: `cmd/roborev/tui.go` — `tuiReviewMsg` handler (around line 1813)

**Step 1: Find the `tuiReviewMsg` case in `Update()`**

```go
case tuiReviewMsg:
    if msg.jobID != m.selectedJobID {
        return m, nil
    }
    m.consecutiveErrors = 0
    m.currentReview = msg.review
    m.currentResponses = msg.responses
    m.currentBranch = msg.branchName
    m.currentView = tuiViewReview
    m.reviewScroll = 0
```

Add after `m.reviewScroll = 0`:
```go
if m.reviewFixPanelPending {
    m.reviewFixPanelPending = false
    m.reviewFixPanelOpen = true
    m.reviewFixPanelFocused = true
    m.fixPromptJobID = msg.review.JobID
}
```

**Step 2: Build and run tests**

```bash
go build ./... && go test ./cmd/roborev/... 2>&1 | tail -20
```

Expected: clean build, no new failures.

**Step 3: Commit**

```bash
git add cmd/roborev/tui.go cmd/roborev/tui_handlers.go
git commit -m "refactor: remove tuiViewFixPrompt modal, add inline fix panel state"
```

---

### Task 5: Add fix panel key handler

**Files:**
- Modify: `cmd/roborev/tui_handlers.go`

**Step 1: Add `handleReviewFixPanelKey()` function**

Add this new function after `handleFixKey()`:

```go
// handleReviewFixPanelKey handles key input when the inline fix panel is focused.
func (m tuiModel) handleReviewFixPanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    switch msg.String() {
    case "ctrl+c":
        return m, tea.Quit
    case "esc":
        m.reviewFixPanelOpen = false
        m.reviewFixPanelFocused = false
        m.fixPromptText = ""
        m.fixPromptJobID = 0
        return m, nil
    case "tab":
        m.reviewFixPanelFocused = false
        return m, nil
    case "enter":
        jobID := m.fixPromptJobID
        prompt := m.fixPromptText
        m.reviewFixPanelOpen = false
        m.reviewFixPanelFocused = false
        m.fixPromptText = ""
        m.fixPromptJobID = 0
        return m, m.triggerFix(jobID, prompt)
    case "backspace":
        if len(m.fixPromptText) > 0 {
            runes := []rune(m.fixPromptText)
            m.fixPromptText = string(runes[:len(runes)-1])
        }
        return m, nil
    default:
        if len(msg.Runes) > 0 {
            for _, r := range msg.Runes {
                if unicode.IsPrint(r) {
                    m.fixPromptText += string(r)
                }
            }
        }
        return m, nil
    }
}
```

**Step 2: Hook into `handleKeyMsg()`**

At the top of `handleKeyMsg()`, before the existing `switch m.currentView`, add:

```go
// Fix panel captures input when focused in review view
if m.currentView == tuiViewReview && m.reviewFixPanelOpen && m.reviewFixPanelFocused {
    return m.handleReviewFixPanelKey(msg)
}
```

**Step 3: Handle `tab` when fix panel is open but unfocused**

In `handleGlobalKey()`, there is currently no `tab` case. Add one:

```go
case "tab":
    return m.handleTabKey()
```

Add the handler function:

```go
// handleTabKey shifts focus to the fix panel when it is open in review view.
func (m tuiModel) handleTabKey() (tea.Model, tea.Cmd) {
    if m.currentView == tuiViewReview && m.reviewFixPanelOpen && !m.reviewFixPanelFocused {
        m.reviewFixPanelFocused = true
    }
    return m, nil
}
```

**Step 4: Handle `esc` closing the fix panel when unfocused**

In `handleEscKey()`, find the `tuiViewReview` branch (around line 1069). After the existing `} else if m.currentView == tuiViewReview {` block opens but before it does anything, add a check:

```go
} else if m.currentView == tuiViewReview {
    // If fix panel is open (but unfocused), esc closes it
    if m.reviewFixPanelOpen {
        m.reviewFixPanelOpen = false
        m.reviewFixPanelFocused = false
        m.fixPromptText = ""
        m.fixPromptJobID = 0
        return m, nil
    }
    // existing esc-from-review logic follows...
```

**Step 5: Build**

```bash
go build ./... 2>&1 | head -20
```

Expected: clean build.

**Step 6: Commit**

```bash
git add cmd/roborev/tui_handlers.go
git commit -m "feat: add inline fix panel key handler with Tab focus toggle"
```

---

### Task 6: Write failing tests for fix panel key handling

**Files:**
- Modify: `cmd/roborev/tui_review_test.go`

**Step 1: Write failing tests**

Add these tests to `tui_review_test.go`:

```go
func TestReviewFixPanelOpenFromReview(t *testing.T) {
    m := newTuiModel("http://localhost")
    m.currentView = tuiViewReview
    done := storage.JobStatusDone
    job := storage.ReviewJob{ID: 1, Status: done}
    m.currentReview = &storage.Review{JobID: 1, Job: &job}
    m.jobs = []storage.ReviewJob{job}
    m.selectedIdx = 0
    m.selectedJobID = 1

    m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
    got := m2.(tuiModel)

    if !got.reviewFixPanelOpen {
        t.Error("Expected reviewFixPanelOpen to be true")
    }
    if !got.reviewFixPanelFocused {
        t.Error("Expected reviewFixPanelFocused to be true")
    }
    if got.fixPromptJobID != 1 {
        t.Errorf("Expected fixPromptJobID=1, got %d", got.fixPromptJobID)
    }
    if got.currentView != tuiViewReview {
        t.Errorf("Expected to stay in tuiViewReview, got %v", got.currentView)
    }
}

func TestReviewFixPanelTabTogglesReviewFocus(t *testing.T) {
    m := newTuiModel("http://localhost")
    m.currentView = tuiViewReview
    m.reviewFixPanelOpen = true
    m.reviewFixPanelFocused = true

    // Tab shifts focus to review
    m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyTab})
    got := m2.(tuiModel)
    if got.reviewFixPanelFocused {
        t.Error("Expected reviewFixPanelFocused to be false after Tab")
    }

    // Tab again shifts focus back to fix panel
    m3, _ := got.handleKeyMsg(tea.KeyMsg{Type: tea.KeyTab})
    got2 := m3.(tuiModel)
    if !got2.reviewFixPanelFocused {
        t.Error("Expected reviewFixPanelFocused to be true after second Tab")
    }
}

func TestReviewFixPanelTextInput(t *testing.T) {
    m := newTuiModel("http://localhost")
    m.currentView = tuiViewReview
    m.reviewFixPanelOpen = true
    m.reviewFixPanelFocused = true

    for _, ch := range "hello" {
        m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
        m = m2.(tuiModel)
    }

    if m.fixPromptText != "hello" {
        t.Errorf("Expected fixPromptText='hello', got %q", m.fixPromptText)
    }
}

func TestReviewFixPanelTextNotCapturedWhenUnfocused(t *testing.T) {
    m := newTuiModel("http://localhost")
    m.currentView = tuiViewReview
    m.reviewFixPanelOpen = true
    m.reviewFixPanelFocused = false // review has focus

    m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
    got := m2.(tuiModel)
    if got.fixPromptText != "" {
        t.Errorf("Expected fixPromptText to remain empty, got %q", got.fixPromptText)
    }
}

func TestReviewFixPanelEscWhenFocusedClosesPanel(t *testing.T) {
    m := newTuiModel("http://localhost")
    m.currentView = tuiViewReview
    m.reviewFixPanelOpen = true
    m.reviewFixPanelFocused = true
    m.fixPromptText = "some text"

    m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEsc})
    got := m2.(tuiModel)
    if got.reviewFixPanelOpen {
        t.Error("Expected panel to close on Esc when focused")
    }
    if got.fixPromptText != "" {
        t.Error("Expected fixPromptText to be cleared on Esc")
    }
    if got.currentView != tuiViewReview {
        t.Errorf("Expected to stay in tuiViewReview, got %v", got.currentView)
    }
}

func TestReviewFixPanelEscWhenUnfocusedClosesPanel(t *testing.T) {
    m := newTuiModel("http://localhost")
    m.currentView = tuiViewReview
    m.reviewFixPanelOpen = true
    m.reviewFixPanelFocused = false // review has focus
    done := storage.JobStatusDone
    m.currentReview = &storage.Review{Job: &storage.ReviewJob{Status: done}}
    m.reviewFromView = tuiViewQueue

    m2, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEsc})
    got := m2.(tuiModel)
    if got.reviewFixPanelOpen {
        t.Error("Expected panel to close on Esc when unfocused")
    }
    // Should stay in review view (not navigate back to queue)
    if got.currentView != tuiViewReview {
        t.Errorf("Expected to stay in tuiViewReview, got %v", got.currentView)
    }
}

func TestReviewFixPanelPendingConsumedOnLoad(t *testing.T) {
    m := newTuiModel("http://localhost")
    m.reviewFixPanelPending = true
    m.selectedJobID = 5

    review := &storage.Review{ID: 1, JobID: 5}
    msg := tuiReviewMsg{review: review, jobID: 5}
    m2, _ := m.Update(msg)
    got := m2.(tuiModel)

    if got.reviewFixPanelPending {
        t.Error("Expected reviewFixPanelPending to be cleared")
    }
    if !got.reviewFixPanelOpen {
        t.Error("Expected reviewFixPanelOpen to be true")
    }
    if !got.reviewFixPanelFocused {
        t.Error("Expected reviewFixPanelFocused to be true")
    }
}
```

**Step 2: Run to confirm failures**

```bash
go test ./cmd/roborev/... -run "TestReviewFixPanel" -v 2>&1
```

Expected: FAIL — fields don't exist yet if running before Task 1, or various assertion failures.

---

### Task 7: Run tests and verify they pass

**Step 1: Run the fix panel tests**

```bash
go test ./cmd/roborev/... -run "TestReviewFixPanel" -v 2>&1
```

Expected: all PASS.

**Step 2: Run full test suite**

```bash
go test ./cmd/roborev/... 2>&1 | tail -20
```

Expected: all PASS (or only pre-existing failures).

**Step 3: Commit tests**

```bash
git add cmd/roborev/tui_review_test.go
git commit -m "test: add fix panel key handling tests"
```

---

### Task 8: Render the fix panel in `renderReviewView()`

**Files:**
- Modify: `cmd/roborev/tui.go` — `renderReviewView()` (around line 2616)

**Step 1: Define fix panel height constant**

At the top of `renderReviewView()`, after `review := m.currentReview`, add:

```go
// Fix panel occupies 4 lines when open: top border, input, bottom border, help
const fixPanelHeight = 4
```

**Step 2: Reduce `visibleLines` when panel is open**

Find the line:
```go
visibleLines := max(m.height-headerHeight, 1)
```

Change to:
```go
panelReserve := 0
if m.reviewFixPanelOpen {
    panelReserve = fixPanelHeight
}
visibleLines := max(m.height-headerHeight-panelReserve, 1)
```

**Step 3: Render the fix panel after the padding loop**

Find the block that pads with clear-to-end-of-line sequences (around line 2770):
```go
// Pad with clear-to-end-of-line sequences to prevent ghost text
for linesWritten < visibleLines {
    b.WriteString("\x1b[K\n")
    linesWritten++
}
```

After this block, add:

```go
// Render inline fix panel when open
if m.reviewFixPanelOpen {
    boxWidth := max(m.width-2, 20)
    dashes := strings.Repeat("─", boxWidth-2)

    if m.reviewFixPanelFocused {
        // Focused: bright border with title prompt
        title := "Fix: enter instructions (or leave blank for default)"
        if runewidth.StringWidth(title) > boxWidth-4 {
            title = runewidth.Truncate(title, boxWidth-4, "")
        }
        b.WriteString(fmt.Sprintf("┌─ %s %s┐\x1b[K\n", title, strings.Repeat("─", max(boxWidth-4-runewidth.StringWidth(title), 0))))

        // Input line with cursor
        inputDisplay := m.fixPromptText
        if runewidth.StringWidth(inputDisplay) > boxWidth-6 {
            // Show tail of input so cursor is always visible
            runes := []rune(inputDisplay)
            for runewidth.StringWidth(string(runes)) > boxWidth-6 {
                runes = runes[1:]
            }
            inputDisplay = string(runes)
        }
        padding := max(boxWidth-4-runewidth.StringWidth(inputDisplay)-1, 0)
        b.WriteString(fmt.Sprintf("│ > %s_%s │\x1b[K\n", inputDisplay, strings.Repeat(" ", padding)))

        b.WriteString("└" + dashes + "┘\x1b[K\n")
        b.WriteString(tuiHelpStyle.Render("tab: scroll review | enter: submit | esc: cancel"))
    } else {
        // Unfocused: dimmed border with hint
        title := "Fix (Tab to focus)"
        b.WriteString(tuiStatusStyle.Render(fmt.Sprintf("┌─ %s %s┐", title, strings.Repeat("─", max(boxWidth-4-runewidth.StringWidth(title), 0)))))
        b.WriteString("\x1b[K\n")

        inputDisplay := m.fixPromptText
        if inputDisplay == "" {
            inputDisplay = "(blank = default)"
        }
        if runewidth.StringWidth(inputDisplay) > boxWidth-4 {
            inputDisplay = runewidth.Truncate(inputDisplay, boxWidth-4, "")
        }
        padding := max(boxWidth-4-runewidth.StringWidth(inputDisplay), 0)
        b.WriteString(tuiStatusStyle.Render(fmt.Sprintf("│  %s%s  │", inputDisplay, strings.Repeat(" ", padding))))
        b.WriteString("\x1b[K\n")

        b.WriteString(tuiStatusStyle.Render("└" + dashes + "┘"))
        b.WriteString("\x1b[K\n")
        b.WriteString(tuiHelpStyle.Render("F: fix | tab: focus fix panel"))
    }
    b.WriteString("\x1b[K")
}
```

**Step 4: Build**

```bash
go build ./... 2>&1 | head -20
```

Expected: clean build.

**Step 5: Run full tests**

```bash
go test ./cmd/roborev/... 2>&1 | tail -20
```

Expected: all PASS.

**Step 6: Commit**

```bash
git add cmd/roborev/tui.go
git commit -m "feat: render inline fix panel in review view with Tab focus indicator"
```

---

### Task 9: Update help text in review view

**Files:**
- Modify: `cmd/roborev/tui.go` — `renderReviewView()` help lines (around line 2725)

**Step 1: Find the help line constants**

```go
const helpLine1 = "p: prompt | c: comment | m: commit msg | a: addressed | y: copy"
const helpLine2 = "↑/↓: scroll | ←/→: prev/next | ?: commands | esc: back"
```

**Step 2: Add 'F' to help line 1**

Change `helpLine1` to:
```go
const helpLine1 = "p: prompt | c: comment | m: commit msg | a: addressed | y: copy | F: fix"
```

**Step 3: Dim help lines when fix panel is focused**

Find where the help lines are written to `b` (around line 2789):
```go
b.WriteString(tuiHelpStyle.Render(helpLine1))
b.WriteString("\x1b[K\n")
b.WriteString(tuiHelpStyle.Render(helpLine2))
b.WriteString("\x1b[K")
```

Change to:
```go
if m.reviewFixPanelOpen && m.reviewFixPanelFocused {
    // Fix panel has focus: dim review help to indicate scroll is inactive
    b.WriteString(tuiStatusStyle.Render(helpLine1))
    b.WriteString("\x1b[K\n")
    b.WriteString(tuiStatusStyle.Render(helpLine2))
} else {
    b.WriteString(tuiHelpStyle.Render(helpLine1))
    b.WriteString("\x1b[K\n")
    b.WriteString(tuiHelpStyle.Render(helpLine2))
}
b.WriteString("\x1b[K")
```

**Step 4: Build and test**

```bash
go build ./... && go test ./cmd/roborev/... 2>&1 | tail -10
```

Expected: clean build, all tests pass.

**Step 5: Commit**

```bash
git add cmd/roborev/tui.go
git commit -m "feat: dim review help text when fix panel is focused, add F to help"
```

---

### Task 10: Update the help view text

**Files:**
- Modify: `cmd/roborev/tui.go` or wherever `renderHelpView()` is defined

**Step 1: Find the help view content**

```bash
grep -n "Fix\|fix prompt\|tuiViewFixPrompt" cmd/roborev/tui.go | head -20
```

**Step 2: Update any references to the old fix prompt modal**

Search for any help text that mentions the old modal behavior (e.g. "fix prompt", "Fix Review #") and update to reflect the inline panel (e.g. "F: trigger fix (inline panel in review view)").

**Step 3: Build and test**

```bash
go build ./... && go test ./cmd/roborev/... 2>&1 | tail -10
```

**Step 4: Commit**

```bash
git add cmd/roborev/tui.go
git commit -m "docs: update help view to reflect inline fix panel"
```

---

### Task 11: Clean up any remaining `tuiViewFixPrompt` / `fixPromptFromView` references

**Step 1: Verify no references remain**

```bash
grep -rn "tuiViewFixPrompt\|fixPromptFromView\|renderFixPromptView\|handleFixPromptKey" cmd/roborev/ 2>&1
```

Expected: no output.

**Step 2: Check test files for outdated `tuiViewFixPrompt` assertions**

```bash
grep -rn "tuiViewFixPrompt" cmd/roborev/*_test.go 2>&1
```

Remove or update any such tests to check `reviewFixPanelOpen` instead.

**Step 3: Run full test suite**

```bash
go fmt ./... && go vet ./... && go test ./cmd/roborev/... 2>&1 | tail -20
```

Expected: clean.

**Step 4: Final commit**

```bash
git add -u
git commit -m "chore: remove all remaining tuiViewFixPrompt references"
```
