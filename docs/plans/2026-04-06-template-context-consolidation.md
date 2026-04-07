# Prompt Template Context Consolidation Implementation Plan

> **For agentic workers:** REQUIRED: Use `/skill:orchestrator-implements` (in-session, orchestrator implements), `/skill:subagent-driven-development` (in-session, subagents implement), or `/skill:executing-plans` (parallel session) to implement this plan. Steps use checkbox syntax for tracking.

**Goal:** Consolidate `internal/prompt` template data into a broader prompt-domain context model without changing prompt behavior, so future customizable templates are not blocked by today’s narrow `...View` structs.

**Architecture:** Keep git/config/storage lookups in `prompt.go`, normalize them into one universal `TemplateContext` root with prompt-family branches, and make templates plus fitting logic operate on that context and its receiver methods. Migrate incrementally through adapters so behavior stays locked to existing regression tests while the old view structs are removed.

**Tech Stack:** Go, `text/template`, embedded template files, testify, existing prompt regression tests in `internal/prompt`.

---

### Task 1: Introduce the consolidated prompt template context model

**TDD scenario:** Modifying tested code — run existing tests first

**Files:**
- Modify: `internal/prompt/prompt.go`
- Modify: `internal/prompt/prompt_body_templates.go`
- Create: `internal/prompt/template_context.go`
- Test: `internal/prompt/prompt_body_templates_test.go`
- Test: `internal/prompt/prompt_test.go`

- [ ] **Step 1: Run the current prompt package tests as baseline**

Run: `go test ./internal/prompt -count=1`
Expected: PASS

- [ ] **Step 2: Add the consolidated context types and clone/trim receiver methods**

```go
type TemplateContext struct {
    Meta    PromptMeta
    Review  *ReviewTemplateContext
    Address *AddressTemplateContext
    System  *SystemTemplateContext
}

type ReviewTemplateContext struct {
    Kind     ReviewKind
    Optional ReviewOptionalContext
    Subject  SubjectContext
    Diff     DiffContext
    Fallback FallbackContext
}
```

Include receiver helpers for:
- cloning review/optional/subject context
- optional trim order: previous attempts → previous reviews → additional context → project guidelines
- single-message/author/subject trimming
- range subject blanking and trailing entry dropping

- [ ] **Step 3: Rename the existing storage-backed `ReviewContext` to avoid the template-context collision**

```go
type HistoricalReviewContext struct {
    SHA       string
    Review    *storage.Review
    Responses []storage.Response
}
```

Update helper signatures that currently take or return the old `ReviewContext` history type.

- [ ] **Step 4: Add focused unit coverage for the new receiver methods and naming bridge**

Add tests that directly assert:
- optional trim order
- clone isolation
- range subject blanking/drop order
- historical review helpers still preserve previous-review ordering

Run: `go test ./internal/prompt -run 'Test(TemplateContext|ReviewOptionalContext|HistoricalReviewContext|SelectRichestRangePromptView)' -count=1`
Expected: PASS

- [ ] **Step 5: Commit Task 1**

```bash
git add internal/prompt/template_context.go internal/prompt/prompt.go internal/prompt/prompt_body_templates.go internal/prompt/prompt_body_templates_test.go internal/prompt/prompt_test.go
git commit -m "refactor(prompt): add consolidated template context model"
```

### Task 2: Move rendering and templates onto the universal `TemplateContext`

**TDD scenario:** Modifying tested code — run existing tests first

**Files:**
- Modify: `internal/prompt/prompt_body_templates.go`
- Modify: `internal/prompt/templates/prompt_sections.md.gotmpl`
- Modify: `internal/prompt/templates/assembled_single.md.gotmpl`
- Modify: `internal/prompt/templates/assembled_range.md.gotmpl`
- Modify: `internal/prompt/templates/assembled_dirty.md.gotmpl`
- Modify: `internal/prompt/templates/assembled_address.md.gotmpl`
- Modify: `internal/prompt/templates.go`
- Test: `internal/prompt/prompt_body_templates_test.go`
- Test: `internal/prompt/templates_test.go`

- [ ] **Step 1: Add rendering tests that exercise the new nested template field paths**

Cover at least:
- review templates render through `.Review.Optional`, `.Review.Subject`, `.Review.Diff`, `.Review.Fallback`
- address templates render through `.Address.*`
- system templates render through `.System.*`

Run: `go test ./internal/prompt -run 'TestRender(SystemPrompt|SinglePrompt|RangePrompt|DirtyPrompt|AddressPrompt)' -count=1`
Expected: FAIL where templates still expect the old root view shapes

- [ ] **Step 2: Update render helpers to accept the universal root context**

```go
func renderReviewPrompt(name string, ctx TemplateContext) (string, error)
func renderAddressPrompt(ctx TemplateContext) (string, error)
func renderSystemPrompt(name string, ctx TemplateContext) (string, error)
```

Keep wrappers if they improve readability, but all template execution should take `TemplateContext`.

- [ ] **Step 3: Rewrite built-in templates to use the new shared root contract**

Examples:

```gotmpl
{{template "optional_sections" .Review.Optional}}
{{template "current_commit" .Review.Subject.Single}}
{{template "diff_block" .Review}}
```

and:

```gotmpl
{{template "address_attempts" .Address}}
{{template "address_findings" .Address}}
```

- [ ] **Step 4: Preserve the explicit fallback contract**

Update fallback partials so they render from structured fallback fields under `.Review.Fallback`, not duplicate ad hoc strings stored on multiple structs.

- [ ] **Step 5: Run rendering/system-template regression coverage**

Run: `go test ./internal/prompt -run 'TestRender|TestRenderSystemPrompt_AllEmbeddedAgentSpecificTemplates' -count=1`
Expected: PASS

- [ ] **Step 6: Commit Task 2**

```bash
git add internal/prompt/prompt_body_templates.go internal/prompt/templates/prompt_sections.md.gotmpl internal/prompt/templates/assembled_single.md.gotmpl internal/prompt/templates/assembled_range.md.gotmpl internal/prompt/templates/assembled_dirty.md.gotmpl internal/prompt/templates/assembled_address.md.gotmpl internal/prompt/templates.go internal/prompt/prompt_body_templates_test.go internal/prompt/templates_test.go
git commit -m "refactor(prompt): route templates through unified context"
```

### Task 3: Migrate builders and fitters to mutate the consolidated context

**TDD scenario:** Modifying tested code — run existing tests first

**Files:**
- Modify: `internal/prompt/prompt.go`
- Modify: `internal/prompt/prompt_body_templates.go`
- Test: `internal/prompt/prompt_test.go`
- Test: `internal/prompt/prompt_body_templates_test.go`

- [ ] **Step 1: Add/refresh builder-level regressions around preserved behavior**

Ensure coverage still explicitly checks:
- single/range guidelines come from `LoadGuidelines`
- dirty/address guidelines use working-tree config
- address prompts still include the full original diff without current excludes
- dirty truncated fallback and range fallback selection behavior remain unchanged

Run: `go test ./internal/prompt -run 'Test(Build|LoadGuidelines|SelectRichestRangePromptView|FitRangePrompt|BuildAddressPrompt)' -count=1`
Expected: FAIL anywhere the new context migration breaks preserved behavior

- [ ] **Step 2: Replace old view assembly in builder code with context constructors**

Introduce helpers that build a `TemplateContext` from already loaded data, for example:

```go
func buildSingleTemplateContext(...) TemplateContext
func buildRangeTemplateContext(...) TemplateContext
func buildDirtyTemplateContext(...) TemplateContext
func buildAddressTemplateContext(...) TemplateContext
func buildSystemTemplateContext(...) TemplateContext
```

These helpers must receive already-loaded git/config/storage data and must not perform I/O themselves.

- [ ] **Step 3: Move fitting mutations onto context receiver methods**

Keep orchestration in `prompt.go`, but drive mutations through methods like:
- `ctx.Review.Optional.TrimNext()`
- `ctx.Review.Subject.TrimSingleMessage()`
- `ctx.Review.Subject.BlankNextRangeSubject()`
- `ctx.Review.Subject.DropLastRangeEntry()`

- [ ] **Step 4: Re-run the full prompt package suite**

Run: `go test ./internal/prompt -count=1`
Expected: PASS

- [ ] **Step 5: Commit Task 3**

```bash
git add internal/prompt/prompt.go internal/prompt/prompt_body_templates.go internal/prompt/prompt_test.go internal/prompt/prompt_body_templates_test.go
git commit -m "refactor(prompt): fit prompts from consolidated context"
```

### Task 4: Remove obsolete narrow views and run full verification

**TDD scenario:** Modifying tested code — run existing tests first

**Files:**
- Modify: `internal/prompt/prompt_body_templates.go`
- Modify: `internal/prompt/prompt.go`
- Modify: `internal/prompt/templates_test.go`
- Modify: `internal/prompt/prompt_body_templates_test.go`
- Modify: `internal/prompt/prompt_test.go`

- [ ] **Step 1: Delete the obsolete narrow template view types and dead adapters**

Remove the old prompt-specific wrappers once all callers/templates use `TemplateContext`:
- `singlePromptView`
- `rangePromptView`
- `dirtyPromptView`
- `addressPromptView`
- `systemPromptView`
- `optionalSectionsView`
- `diffSectionView`
- fallback-specific one-off view structs no longer needed

- [ ] **Step 2: Remove any render helpers that only existed for the old shape**

Keep only the helpers needed by the consolidated context contract and current tests.

- [ ] **Step 3: Run repo verification**

Run:
- `go fmt ./...`
- `go vet ./...`
- `go test ./...`

Expected: PASS

- [ ] **Step 4: Commit Task 4**

```bash
git add internal/prompt/prompt_body_templates.go internal/prompt/prompt.go internal/prompt/templates_test.go internal/prompt/prompt_body_templates_test.go internal/prompt/prompt_test.go
git commit -m "refactor(prompt): remove narrow template view wrappers"
```

### Task 5: Final review and landing prep

**TDD scenario:** Trivial change — use judgment

**Files:**
- Modify: `docs/plans/2026-04-06-template-context-consolidation.md` (only if implementation notes are required)

- [ ] **Step 1: Review the final diff for accidental behavior changes**

Run: `git diff origin/main...HEAD -- internal/prompt`
Expected: only the intended context consolidation and test/template updates

- [ ] **Step 2: Request a code review if the subagent harness cooperates; otherwise note the timeout block in the handoff summary**

Run a reviewer or capture the blocker explicitly.

- [ ] **Step 3: Commit any final cleanups**

```bash
git add -A
git commit -m "chore(prompt): finalize template context consolidation" || true
```
