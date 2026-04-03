# Go-template Prompt Builder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor `internal/prompt/prompt.go` so single, range, and dirty review prompts are assembled through Go templates while preserving current wording, section order, size-budget behavior, and fallback handling.

**Architecture:** Keep `GetSystemPrompt()` unchanged and move only the assembled prompt body behind dedicated templates. Represent each prompt body as explicit budget units (`OptionalContext`, `CurrentRequired`, `CurrentOverflow`, `DiffSection`) so the builder can re-render the same template after trimming optional content or swapping diff variants. Reuse the existing diff/fallback helpers, but replace the final `strings.Builder` assembly in `BuildDirty`, `buildSinglePrompt`, and `buildRangePrompt` with template rendering plus render-measure-fit loops.

**Tech Stack:** Go, `text/template`, embedded template files via `embed.FS`, existing `testify`-based prompt tests.

---

## File structure

- Modify: `internal/prompt/prompt.go:182-255` — replace dirty prompt body assembly with template rendering and render-fit budgeting.
- Modify: `internal/prompt/prompt.go:279-310` — replace `buildPromptPreservingCurrentSection` with template-based fit helpers that preserve current-section budget semantics.
- Modify: `internal/prompt/prompt.go:454-689` — replace single/range final body assembly with template rendering.
- Modify: `internal/prompt/prompt.go:691-802` — keep the existing section-formatting helpers, but have the new body renderers consume their output as `OptionalContext`.
- Create: `internal/prompt/prompt_body_templates.go` — define prompt body view structs, parse assembled body templates from `templateFS`, and expose `renderSinglePromptBody`, `renderRangePromptBody`, `renderDirtyPromptBody`, plus fit helpers.
- Create: `internal/prompt/prompt_body_templates_test.go` — focused unit tests for template rendering and fit behavior.
- Create: `internal/prompt/templates/assembled_single.tmpl` — assembled body for single-commit prompts.
- Create: `internal/prompt/templates/assembled_range.tmpl` — assembled body for range prompts.
- Create: `internal/prompt/templates/assembled_dirty.tmpl` — assembled body for dirty prompts.
- Modify: `internal/prompt/prompt_test.go:427-903` — add public regression coverage proving the template path preserves current ordering, fallback text, and cap handling.

### Task 1: Add assembled prompt body renderers

**Files:**
- Create: `internal/prompt/prompt_body_templates.go`
- Create: `internal/prompt/prompt_body_templates_test.go`
- Create: `internal/prompt/templates/assembled_single.tmpl`
- Create: `internal/prompt/templates/assembled_range.tmpl`
- Create: `internal/prompt/templates/assembled_dirty.tmpl`

- [ ] **Step 1: Write the failing renderer test**

```go
package prompt

import (
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestRenderSinglePromptBody(t *testing.T) {
    body, err := renderSinglePromptBody(singlePromptBodyView{
        OptionalContext: "## Project Guidelines\n\nPrefer composition over inheritance.\n\n",
        CurrentRequired: "## Current Commit\n\n**Commit:** abc1234\n\n",
        CurrentOverflow: "**Subject:** add template renderer\n**Author:** Test User\n\n",
        DiffSection:     "### Diff\n\n```diff\n+line\n```\n",
    })
    require.NoError(t, err)

    assert.Equal(t,
        "## Project Guidelines\n\nPrefer composition over inheritance.\n\n"+
            "## Current Commit\n\n**Commit:** abc1234\n\n"+
            "**Subject:** add template renderer\n**Author:** Test User\n\n"+
            "### Diff\n\n```diff\n+line\n```\n",
        body,
    )
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/prompt -run TestRenderSinglePromptBody -count=1`
Expected: FAIL with `undefined: renderSinglePromptBody` and `undefined: singlePromptBodyView`

- [ ] **Step 3: Write the minimal renderer implementation and templates**

`internal/prompt/prompt_body_templates.go`

```go
package prompt

import (
    "bytes"
    "text/template"
)

type singlePromptBodyView struct {
    OptionalContext string
    CurrentRequired string
    CurrentOverflow string
    DiffSection     string
}

type rangePromptBodyView struct {
    OptionalContext string
    CurrentRequired string
    CurrentOverflow string
    DiffSection     string
}

type dirtyPromptBodyView struct {
    OptionalContext string
    CurrentRequired string
    DiffSection     string
}

var promptBodyTemplates = template.Must(template.New("prompt-bodies").ParseFS(
    templateFS,
    "templates/assembled_single.tmpl",
    "templates/assembled_range.tmpl",
    "templates/assembled_dirty.tmpl",
))

func renderSinglePromptBody(view singlePromptBodyView) (string, error) {
    return executePromptBodyTemplate("assembled_single.tmpl", view)
}

func renderRangePromptBody(view rangePromptBodyView) (string, error) {
    return executePromptBodyTemplate("assembled_range.tmpl", view)
}

func renderDirtyPromptBody(view dirtyPromptBodyView) (string, error) {
    return executePromptBodyTemplate("assembled_dirty.tmpl", view)
}

func executePromptBodyTemplate(name string, view any) (string, error) {
    var buf bytes.Buffer
    if err := promptBodyTemplates.ExecuteTemplate(&buf, name, view); err != nil {
        return "", err
    }
    return buf.String(), nil
}
```

`internal/prompt/templates/assembled_single.tmpl`

```gotemplate
{{- .OptionalContext -}}{{- .CurrentRequired -}}{{- .CurrentOverflow -}}{{- .DiffSection -}}
```

`internal/prompt/templates/assembled_range.tmpl`

```gotemplate
{{- .OptionalContext -}}{{- .CurrentRequired -}}{{- .CurrentOverflow -}}{{- .DiffSection -}}
```

`internal/prompt/templates/assembled_dirty.tmpl`

```gotemplate
{{- .OptionalContext -}}{{- .CurrentRequired -}}{{- .DiffSection -}}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/prompt -run TestRenderSinglePromptBody -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/prompt/prompt_body_templates.go internal/prompt/prompt_body_templates_test.go internal/prompt/templates/assembled_single.tmpl internal/prompt/templates/assembled_range.tmpl internal/prompt/templates/assembled_dirty.tmpl
git commit -m "refactor(prompt): add assembled prompt body renderers"
```

### Task 2: Migrate single-commit prompts to the template path

**Files:**
- Modify: `internal/prompt/prompt.go:279-310`
- Modify: `internal/prompt/prompt.go:454-570`
- Modify: `internal/prompt/prompt_body_templates.go`
- Modify: `internal/prompt/prompt_body_templates_test.go`
- Test: `internal/prompt/prompt_test.go:427-665`

- [ ] **Step 1: Write the failing fit test**

Add this test to `internal/prompt/prompt_body_templates_test.go`:

```go
func TestFitSinglePromptBodyTrimsOptionalContextBeforeCurrentOverflow(t *testing.T) {
    view := singlePromptBodyView{
        OptionalContext: strings.Repeat("g", 128),
        CurrentRequired: "## Current Commit\n\n**Commit:** abc1234\n\n",
        CurrentOverflow: "**Subject:** large change\n**Author:** Test User\n\n",
        DiffSection:     "### Diff\n\n(Diff too large; for Codex run `git show abc1234 --` locally.)\n",
    }

    body, err := fitSinglePromptBody(len(view.CurrentRequired)+len(view.CurrentOverflow)+len(view.DiffSection), view)
    require.NoError(t, err)

    assert.Contains(t, body, "## Current Commit")
    assert.Contains(t, body, "**Subject:** large change")
    assert.NotContains(t, body, strings.Repeat("g", 128))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/prompt -run TestFitSinglePromptBodyTrimsOptionalContextBeforeCurrentOverflow -count=1`
Expected: FAIL with `undefined: fitSinglePromptBody`

- [ ] **Step 3: Implement the single-prompt fit helper and migrate `buildSinglePrompt`**

Add this helper to `internal/prompt/prompt_body_templates.go`:

```go
func fitSinglePromptBody(limit int, view singlePromptBodyView) (string, error) {
    body, err := renderSinglePromptBody(view)
    if err != nil {
        return "", err
    }
    if len(body) <= limit {
        return body, nil
    }

    overflow := len(body) - limit
    if overflow > 0 && len(view.OptionalContext) > 0 {
        originalLen := len(view.OptionalContext)
        view.OptionalContext = truncateUTF8(view.OptionalContext, max(0, len(view.OptionalContext)-overflow))
        overflow -= originalLen - len(view.OptionalContext)
    }
    if overflow > 0 && len(view.CurrentOverflow) > 0 {
        view.CurrentOverflow = truncateUTF8(view.CurrentOverflow, max(0, len(view.CurrentOverflow)-overflow))
    }

    body, err = renderSinglePromptBody(view)
    if err != nil {
        return "", err
    }
    return hardCapPrompt(body, limit), nil
}
```

Replace the final body assembly in `buildSinglePrompt` with:

```go
bodyLimit := max(0, promptCap-len(requiredPrefix))
view := singlePromptBodyView{
    OptionalContext: optionalContext.String(),
    CurrentRequired: currentRequired.String(),
    CurrentOverflow: currentOverflow.String(),
    DiffSection:     diffSection.String(),
}

body, err := fitSinglePromptBody(bodyLimit, view)
if err != nil {
    return "", err
}
return requiredPrefix + body, nil
```

For the oversized-diff branch, swap the diff section into the same view and re-render instead of calling `buildPromptPreservingCurrentSection`:

```go
view := singlePromptBodyView{
    OptionalContext: optionalContext.String(),
    CurrentRequired: currentRequired.String(),
    CurrentOverflow: currentOverflow.String(),
    DiffSection:     fallback,
}
body, err := fitSinglePromptBody(bodyLimit, view)
if err != nil {
    return "", err
}
return requiredPrefix + body, nil
```

- [ ] **Step 4: Run targeted single-prompt tests**

Run: `go test ./internal/prompt -run 'TestFitSinglePromptBodyTrimsOptionalContextBeforeCurrentOverflow|TestBuildPromptCodexOversizedDiffKeepsCurrentCommitMetadataWhenTrimming|TestBuildPromptNonCodexSmallCapStaysWithinCap' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/prompt/prompt.go internal/prompt/prompt_body_templates.go internal/prompt/prompt_body_templates_test.go
git commit -m "refactor(prompt): template single commit prompt bodies"
```

### Task 3: Migrate range prompts to the template path

**Files:**
- Modify: `internal/prompt/prompt.go:572-689`
- Modify: `internal/prompt/prompt_body_templates.go`
- Modify: `internal/prompt/prompt_body_templates_test.go`
- Test: `internal/prompt/prompt_test.go:442-708`

- [ ] **Step 1: Write the failing range fit test**

Add this test to `internal/prompt/prompt_body_templates_test.go`:

```go
func TestFitRangePromptBodyTrimsOptionalContextBeforeRangeOverflow(t *testing.T) {
    view := rangePromptBodyView{
        OptionalContext: strings.Repeat("g", 128),
        CurrentRequired: "## Commit Range\n\nReviewing 2 commits:\n\n",
        CurrentOverflow: "- abc1234 first change\n- def5678 second change\n\n",
        DiffSection:     "### Combined Diff\n\n(Diff too large; for Codex run `git diff abc1234..def5678 --` locally.)\n",
    }

    body, err := fitRangePromptBody(len(view.CurrentRequired)+len(view.CurrentOverflow)+len(view.DiffSection), view)
    require.NoError(t, err)

    assert.Contains(t, body, "## Commit Range")
    assert.Contains(t, body, "- abc1234 first change")
    assert.NotContains(t, body, strings.Repeat("g", 128))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/prompt -run TestFitRangePromptBodyTrimsOptionalContextBeforeRangeOverflow -count=1`
Expected: FAIL with `undefined: fitRangePromptBody`

- [ ] **Step 3: Implement the range fit helper and migrate `buildRangePrompt`**

Add this helper to `internal/prompt/prompt_body_templates.go`:

```go
func fitRangePromptBody(limit int, view rangePromptBodyView) (string, error) {
    body, err := renderRangePromptBody(view)
    if err != nil {
        return "", err
    }
    if len(body) <= limit {
        return body, nil
    }

    overflow := len(body) - limit
    if overflow > 0 && len(view.OptionalContext) > 0 {
        originalLen := len(view.OptionalContext)
        view.OptionalContext = truncateUTF8(view.OptionalContext, max(0, len(view.OptionalContext)-overflow))
        overflow -= originalLen - len(view.OptionalContext)
    }
    if overflow > 0 && len(view.CurrentOverflow) > 0 {
        view.CurrentOverflow = truncateUTF8(view.CurrentOverflow, max(0, len(view.CurrentOverflow)-overflow))
    }

    body, err = renderRangePromptBody(view)
    if err != nil {
        return "", err
    }
    return hardCapPrompt(body, limit), nil
}
```

Replace the final assembly in `buildRangePrompt` with:

```go
bodyLimit := max(0, promptCap-len(requiredPrefix))
view := rangePromptBodyView{
    OptionalContext: optionalContext.String(),
    CurrentRequired: currentRequired.String(),
    CurrentOverflow: currentOverflow.String(),
    DiffSection:     diffSection.String(),
}

body, err := fitRangePromptBody(bodyLimit, view)
if err != nil {
    return "", err
}
return requiredPrefix + body, nil
```

Use the same pattern in the oversized-diff branch, assigning the fallback section into `view.DiffSection` and re-rendering.

- [ ] **Step 4: Run targeted range tests**

Run: `go test ./internal/prompt -run 'TestFitRangePromptBodyTrimsOptionalContextBeforeRangeOverflow|TestBuildRangePromptCodexOversizedDiffKeepsCurrentRangeMetadataWhenTrimming|TestBuildRangePromptNonCodexSmallCapStaysWithinCap' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/prompt/prompt.go internal/prompt/prompt_body_templates.go internal/prompt/prompt_body_templates_test.go
git commit -m "refactor(prompt): template range prompt bodies"
```

### Task 4: Migrate dirty prompts to the template path

**Files:**
- Modify: `internal/prompt/prompt.go:182-255`
- Modify: `internal/prompt/prompt_body_templates.go`
- Modify: `internal/prompt/prompt_body_templates_test.go`
- Test: `internal/prompt/prompt_test.go:741-835`

- [ ] **Step 1: Write the failing dirty fit test**

Add this test to `internal/prompt/prompt_body_templates_test.go`:

```go
func TestFitDirtyPromptBodyTrimsOptionalContextBeforeTruncatedDiff(t *testing.T) {
    view := dirtyPromptBodyView{
        OptionalContext: strings.Repeat("g", 128),
        CurrentRequired: "## Uncommitted Changes\n\nThe following changes have not yet been committed.\n\n",
        DiffSection:     "### Diff\n\n(Diff too large to include in full)\n```diff\n+line\n... (truncated)\n```\n",
    }

    body, err := fitDirtyPromptBody(len(view.CurrentRequired)+len(view.DiffSection), view)
    require.NoError(t, err)

    assert.Contains(t, body, "## Uncommitted Changes")
    assert.Contains(t, body, "(Diff too large to include in full)")
    assert.NotContains(t, body, strings.Repeat("g", 128))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/prompt -run TestFitDirtyPromptBodyTrimsOptionalContextBeforeTruncatedDiff -count=1`
Expected: FAIL with `undefined: fitDirtyPromptBody`

- [ ] **Step 3: Implement the dirty fit helper and migrate `BuildDirty`**

Add this helper to `internal/prompt/prompt_body_templates.go`:

```go
func fitDirtyPromptBody(limit int, view dirtyPromptBodyView) (string, error) {
    body, err := renderDirtyPromptBody(view)
    if err != nil {
        return "", err
    }
    if len(body) <= limit {
        return body, nil
    }

    overflow := len(body) - limit
    if overflow > 0 && len(view.OptionalContext) > 0 {
        view.OptionalContext = truncateUTF8(view.OptionalContext, max(0, len(view.OptionalContext)-overflow))
    }

    body, err = renderDirtyPromptBody(view)
    if err != nil {
        return "", err
    }
    return hardCapPrompt(body, limit), nil
}
```

Replace the final assembly in `BuildDirty` with:

```go
bodyLimit := max(0, promptCap-len(requiredPrefix))
view := dirtyPromptBodyView{
    OptionalContext: optCtx,
    CurrentRequired: currentRequired,
    DiffSection:     diffSection.String(),
}

body, err := fitDirtyPromptBody(bodyLimit, view)
if err != nil {
    return "", err
}
return requiredPrefix + body, nil
```

For the oversized-diff branch, set `view.DiffSection` to the truncated-inline block instead of appending directly to a builder, then re-render.

- [ ] **Step 4: Run targeted dirty tests**

Run: `go test ./internal/prompt -run 'TestFitDirtyPromptBodyTrimsOptionalContextBeforeTruncatedDiff|TestBuildDirtySmallCapStaysWithinCap|TestBuildDirtySmallCapTruncatesUTF8Safely' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/prompt/prompt.go internal/prompt/prompt_body_templates.go internal/prompt/prompt_body_templates_test.go
git commit -m "refactor(prompt): template dirty prompt bodies"
```

### Task 5: Lock in public regressions and verify the package

**Files:**
- Modify: `internal/prompt/prompt_test.go:427-903`
- Modify: `internal/prompt/prompt_body_templates_test.go`

- [ ] **Step 1: Write the failing public regression test**

Add this test to `internal/prompt/prompt_test.go`:

```go
func TestBuildPromptWithAdditionalContextAndPreviousAttemptsPreservesSectionOrder(t *testing.T) {
    repoPath, commits := setupTestRepo(t)
    db, repoID := setupDBWithCommits(t, repoPath, commits)

    testutil.CreateCompletedReview(t, db, repoID, commits[5], "test", "First review")

    builder := NewBuilder(db)
    prompt, err := builder.BuildWithAdditionalContext(
        repoPath,
        commits[5],
        repoID,
        0,
        "claude-code",
        "",
        "## Pull Request Discussion\n\nNewest comment first.\n",
    )
    require.NoError(t, err)

    additionalContextPos := strings.Index(prompt, "## Pull Request Discussion")
    previousAttemptsPos := strings.Index(prompt, "## Previous Review Attempts")
    currentCommitPos := strings.Index(prompt, "## Current Commit")

    require.NotEqual(t, -1, additionalContextPos)
    require.NotEqual(t, -1, previousAttemptsPos)
    require.NotEqual(t, -1, currentCommitPos)
    assert.Less(t, additionalContextPos, previousAttemptsPos)
    assert.Less(t, previousAttemptsPos, currentCommitPos)
}
```

- [ ] **Step 2: Run test to verify it fails if ordering regresses**

Run: `go test ./internal/prompt -run TestBuildPromptWithAdditionalContextAndPreviousAttemptsPreservesSectionOrder -count=1`
Expected: PASS before moving on; if it fails, fix the template order before the full verification run.

- [ ] **Step 3: Remove the old final-assembly helper and keep callers on the template path**

Delete the now-unused helper from `internal/prompt/prompt.go`:

```go
func buildPromptPreservingCurrentSection(
    requiredPrefix, optionalContext, currentRequired, currentOverflow string,
    limit int,
    variants ...string,
) string {
    // delete this helper once buildSinglePrompt/buildRangePrompt use fitSinglePromptBody and fitRangePromptBody
}
```

After deletion, `prompt.go` should only keep:
- `truncateUTF8`
- `hardCapPrompt`
- diff fallback helpers
- prompt builders that call the new template renderers

- [ ] **Step 4: Run formatting, vet, and the prompt package tests**

Run: `go fmt ./... && go vet ./... && go test ./internal/prompt -count=1`
Expected: all commands succeed

- [ ] **Step 5: Run the full test suite and commit**

Run: `go test ./...`
Expected: PASS

```bash
git add internal/prompt/prompt.go internal/prompt/prompt_body_templates.go internal/prompt/prompt_body_templates_test.go internal/prompt/templates/assembled_single.tmpl internal/prompt/templates/assembled_range.tmpl internal/prompt/templates/assembled_dirty.tmpl internal/prompt/prompt_test.go
git commit -m "refactor(prompt): switch prompt assembly to templates"
```

## Self-review

### Spec coverage

- **Single/range/dirty template bodies:** covered by Tasks 1-4.
- **Preserve wording and ordering:** covered by Task 1 renderer tests and Task 5 public regression checks.
- **Trim optional context before current section metadata:** covered by Tasks 2-4 fit tests.
- **Preserve Codex/non-Codex fallback behavior:** existing tests in `internal/prompt/prompt_test.go` are kept in the targeted runs in Tasks 2 and 3.
- **Keep public APIs unchanged:** all tasks modify internals only; no caller changes are planned.

### Placeholder scan

- No `TODO`, `TBD`, or "implement later" placeholders remain.
- Every code-changing step includes exact code snippets.
- Every verification step includes an exact command and expected result.

### Type consistency

- The plan consistently uses `singlePromptBodyView`, `rangePromptBodyView`, `dirtyPromptBodyView`.
- The fit helpers are consistently named `fitSinglePromptBody`, `fitRangePromptBody`, and `fitDirtyPromptBody`.
- The renderer entry points are consistently named `renderSinglePromptBody`, `renderRangePromptBody`, and `renderDirtyPromptBody`.
