# Go-template prompt builder refactor

## Summary

Refactor `internal/prompt/` so prompt construction is template-driven end to end. That includes system-prompt fallbacks in `internal/prompt/templates.go`, assembled prompt bodies in `internal/prompt/prompt.go`, shared optional sections such as project guidelines and previous reviews, and oversized-diff fallback text. By the end of this workstream, prompt rendering should use typed Go view models plus nested `text/template` partials instead of manual `strings.Builder` / `fmt.Fprintf` assembly.

This design builds on the branch’s current partial migration (`prompt_body_templates.go` and `assembled_{single,range,dirty}.tmpl`) but pushes it to the intended finish line: no stringbuilder-style prompt assembly remains in the prompt package for review, range, dirty, address, security, or design-review flows.

## Goals

- Make prompt structure explicit through nested Go templates.
- Cover all prompt families involved in review flows: single, range, dirty, address, security, and design-review.
- Remove stringbuilder-style prompt assembly from prompt construction code.
- Use typed view structs per prompt family rather than map-driven rendering.
- Preserve current wording, section ordering, trimming priorities, and fallback behavior unless an intentional wording change is called out.
- Keep exported entry points unchanged, including `Build`, `BuildWithAdditionalContext`, `BuildDirty`, `BuildAddressPrompt`, and `GetSystemPrompt`.

## Non-goals

- Reworking caller APIs or config resolution.
- Changing review policy or output expectations.
- Introducing a generic metadata engine or schema language for prompts.
- Reorganizing unrelated packages outside `internal/prompt/`.
- Doing speculative abstraction beyond what is needed to remove ad hoc prompt assembly.

## Current problems

Today the prompt package still spreads prompt construction across:

- hard-coded fallback constants in `prompt.go`
- agent-specific template files in `templates/`
- body renderers that currently concatenate preformatted strings
- helper methods like `writeProjectGuidelines`, `writePreviousReviews`, `writePreviousAttemptsForGitRef`, and `BuildAddressPrompt` that manually append markdown
- oversized-diff fallback variants built from string formatting helpers

That leaves the package in a mixed state where some prompt shape lives in templates and some lives in procedural string assembly. The result is harder to reason about, harder to test structurally, and easy to regress as more conditions are added.

## Approved design

### 1. Template the whole prompt system, not just final bodies

The end state uses templates for:

- system prompt fallbacks
- top-level prompt bodies
- shared optional sections
- current commit / range / dirty sections
- oversized-diff fallback variants

`GetSystemPrompt` remains the public entry point, but its default fallback content moves out of Go string constants and into templates. Agent-specific template selection still works the same way; the difference is that the fallback path is also template-rendered.

### 2. Use typed nested view models per prompt family

Each prompt family gets a typed view struct with nested section structs. The important split is between orchestration and rendering:

- Go code gathers data, resolves review type aliases, chooses fallback mode, and applies size fitting.
- Templates render the document shape from typed data.

Representative view types:

- `singlePromptView`
- `rangePromptView`
- `dirtyPromptView`
- `addressPromptView`
- `systemPromptView`

Representative nested section types:

- `guidelinesSectionView`
- `previousReviewSectionView`
- `reviewAttemptSectionView`
- `currentCommitSectionView`
- `commitRangeSectionView`
- `dirtyChangesSectionView`
- `diffSectionView`
- `diffFallbackView`

The exact type names can vary, but the structure should remain typed and explicit. The goal is to model prompt sections directly, not to shuttle around large pre-rendered markdown strings.

### 3. Use nested templates for shared sections

Instead of one flat template per prompt body with opaque string slots, templates should call shared named partials. Examples:

- `project_guidelines`
- `previous_reviews`
- `previous_review_attempts`
- `current_commit`
- `commit_range`
- `dirty_changes`
- `diff_block`
- `codex_commit_fallback`
- `codex_range_fallback`
- `address_findings`

This keeps shared prompt structure in one place and avoids reintroducing procedural formatting in Go helpers.

### 4. Keep fitting and prioritization in Go

Prompt budgeting remains explicit Go logic. Templates render the ideal shape; Go decides how much data is available to render.

The fitting rules stay aligned with current behavior:

- Preserve the system prompt prefix.
- Preserve the current commit / range / dirty header section.
- Trim optional context before sacrificing current metadata.
- For single/range prompts, downgrade to the existing large-diff fallback variants when the inline diff cannot fit.
- For dirty prompts, keep the current truncated-inline-diff behavior.
- Use the hard cap only as a final guardrail.

The key change is that fitting operates on typed view fields and re-rendered templates instead of handwritten prompt concatenation.

### 5. Finish the migration already underway on this branch

The branch already has:

- `internal/prompt/prompt_body_templates.go`
- `internal/prompt/prompt_body_templates_test.go`
- `internal/prompt/templates/assembled_single.tmpl`
- `internal/prompt/templates/assembled_range.tmpl`
- `internal/prompt/templates/assembled_dirty.tmpl`

Those pieces should be kept only if they serve the final nested-template design. It is acceptable to restructure them if needed. Preserving the temporary shape is not a requirement; reaching the cleaner typed nested-template architecture is.

## Components

### Template definitions

Use embedded templates under `internal/prompt/templates/` for both system prompts and assembled bodies. Split templates by responsibility:

- family templates for single/range/dirty/address/system prompts
- shared partials for repeated sections
- fallback templates for large-diff messaging

Templates should contain layout and branching that is natural to read in a template. They should not take on budgeting or repository lookup logic.

### Renderer

Introduce a small renderer layer that owns template parsing and execution. It should expose explicit entry points such as:

- `renderSinglePrompt(...)`
- `renderRangePrompt(...)`
- `renderDirtyPrompt(...)`
- `renderAddressPrompt(...)`
- `renderSystemPrompt(...)`

It is fine for the implementation to share an internal `executeTemplate` helper, but call sites should still use prompt-family-specific entry points.

### Builder/orchestration

The existing builder methods stay responsible for:

- loading guidelines
- loading previous reviews and responses
- loading previous attempts
- reading commit/range metadata
- computing diffs and fallback modes
- enforcing size limits

Their job is to fill typed views and call renderers, not to write markdown themselves.

### Fit helpers

Keep family-specific fit helpers where needed, but operate on typed fields. For example, a single-prompt fit helper can trim nested optional sections or degrade a diff section while preserving the current commit section.

## Data flow

### Single, range, and dirty prompts

1. Resolve prompt kind and review type alias.
2. Gather git/config/db data.
3. Build a typed view with nested sections.
4. Render the prompt body through named templates and shared partials.
5. If over budget, trim optional sections or degrade diff mode in Go.
6. Re-render until the prompt fits, then prepend the rendered system prompt.

### Address prompts

1. Gather project guidelines, previous attempts, severity filter instruction, review findings, and original diff context.
2. Build an `addressPromptView`.
3. Render the full prompt through templates instead of `strings.Builder`.
4. Return the rendered string without changing public behavior.

### System prompts

1. Normalize the agent name.
2. Resolve the prompt family (`review`, `range`, `dirty`, `address`, `security`, `design-review`, `run`).
3. Prefer an agent-specific template file when present.
4. Otherwise render the default fallback template for that family.
5. Apply the current date and no-skills instruction through structured template data rather than string concatenation.

## Error handling

- Template parse failures should fail fast during tests or package initialization.
- Template execution failures should be returned with prompt-family context.
- Missing optional data should produce omitted sections, not placeholder text.
- Internal helpers may still use `bytes.Buffer` to execute templates; the ban is on ad hoc prompt assembly, not on standard library rendering internals.

## Testing strategy

Update and extend:

- `internal/prompt/prompt_body_templates_test.go`
- `internal/prompt/prompt_test.go`
- `internal/prompt/templates_test.go`

### Coverage to preserve

- Expected headings and section ordering for single/range/dirty prompts.
- Previous reviews and responses rendering.
- Previous review attempts rendering.
- Additional context ordering.
- Guidelines loading behavior.
- Codex and non-Codex fallback wording.
- Prompt cap behavior and UTF-8 safety.

### New coverage to add

- Nested section templates render the same shape as the prior inline output.
- Address prompts render through templates and preserve ordering.
- Default system prompt fallbacks render from templates for review/security/address/design-review families.
- No-skills/date injection behavior remains correct after the system-prompt migration.
- No prompt path depends on `strings.Builder`-assembled markdown sections.

## Risks and mitigations

### Risk: output drift

Moving all prompt construction into templates can change spacing or section order.

**Mitigation:** keep wording stable, add ordering assertions, and compare rendered output directly in focused tests.

### Risk: overfitting the temporary branch shape

The branch already has a partial template migration that still uses flat string slots.

**Mitigation:** prefer the approved typed nested-template structure even if it means reshaping the current in-progress files.

### Risk: templates becoming opaque

Too much logic in templates would hide behavior that belongs in Go.

**Mitigation:** keep budgeting, repo lookups, and fallback selection in Go; keep templates focused on structure.

## Rollout

Land the work in place in `internal/prompt/`. The migration can proceed incrementally, but the finished state should leave no prompt-construction path relying on stringbuilder-style markdown assembly.

## Success criteria

- `Build`, `BuildWithAdditionalContext`, `BuildDirty`, `BuildAddressPrompt`, and `GetSystemPrompt` all render through templates.
- Shared prompt sections use nested partials rather than handwritten markdown assembly.
- Existing review behavior, section ordering, and fallback semantics remain materially unchanged.
- The prompt package no longer assembles prompt markdown through `strings.Builder` / `fmt.Fprintf` style code paths.
- Regression tests cover system prompts, address prompts, and body prompts under both normal and over-budget conditions.
