# Go-template prompt builder refactor

## Summary

Refactor `internal/prompt/prompt.go` so the assembled review prompts are rendered from Go templates instead of being manually concatenated with `strings.Builder` and `fmt.Fprintf`. The refactor should fully cover single-commit, range, and dirty prompt construction while preserving the current rendered wording and section ordering as closely as possible.

The design uses a single assembled template per prompt kind. Prompt size control remains explicit in Go: the builder renders the template with progressively smaller budgets, measures the rendered output length, and adjusts optional content or diff mode until the result fits.

## Goals

- Replace brittle string-by-string prompt assembly with template-based rendering.
- Preserve current prompt wording, section ordering, and review semantics as closely as possible.
- Preserve the current trimming priorities and large-diff fallback behavior.
- Keep the public prompt-builder API unchanged.
- Keep system-prompt selection in `internal/prompt/templates.go` unchanged.

## Non-goals

- Rewording review instructions or changing review policy.
- Changing caller APIs or config behavior.
- Introducing a new generic section engine or metadata framework.
- Refactoring prompt budgeting into a separate subsystem beyond what is needed for this issue.

## Current problems

Today, `buildSinglePrompt`, `buildRangePrompt`, and `BuildDirty` mix together:

- data collection
- formatting concerns
- markdown layout
- truncation and budgeting logic
- diff fallback selection

That makes the prompt shape harder to reason about and more fragile as conditions grow. It also splits prompt structure across many ad hoc writes instead of making the final document shape explicit.

## Proposed design

### 1. Use one assembled template per prompt kind

Add embedded templates under `internal/prompt/templates/` for the full assembled prompt body for:

- single commit prompts
- commit range prompts
- dirty prompts

These templates should render the entire prompt body after the system prompt prefix, including optional sections and the current diff or fallback section.

Each template should preserve the current headings and text closely, including sections such as:

- `## Project Guidelines`
- `## Previous Reviews`
- `## Previous Review Attempts`
- `## Current Commit`
- `## Commit Range`
- `## Uncommitted Changes`
- `### Diff`
- `### Combined Diff`

### 2. Introduce explicit prompt view models

Add internal data structs in `internal/prompt` that represent the full render state for a prompt. The structs should contain already-prepared strings and flags needed by the template, rather than pushing formatting logic into the template.

The render model should distinguish between:

- required content that must be preserved
- optional context that can be trimmed
- full diff mode
- fallback diff mode
- dirty diff truncation mode

This keeps templates declarative while leaving behavior decisions in Go.

### 3. Keep budgeting in Go by rendering against different budgets

The builder should render templates, measure the resulting output length, and adjust inputs until the result fits within the prompt budget.

For single and range prompts:

1. Collect full data.
2. Render with full optional context and full diff.
3. If over budget, shrink optional context according to current priority rules and re-render.
4. If still over budget because the diff cannot fit, switch to the existing large-diff fallback content and re-render.
5. Apply the hard cap only as a final guardrail.

For dirty prompts:

1. Collect full data.
2. Render with full optional context and full inline diff.
3. If over budget, shrink optional context and re-render.
4. If still over budget, render the existing truncated inline diff form and re-render.
5. Apply the hard cap only as a final guardrail.

The important change is that sizing decisions are made by measuring rendered template output instead of precomputing the full prompt from many manual writes.

### 4. Preserve current trimming priorities

The refactor should keep the current semantic priority order:

- Preserve the system prompt prefix.
- Preserve the current commit or current range section.
- Trim optional context before sacrificing current commit or range metadata.
- For single and range prompts, switch to the existing fallback diff content when the full diff does not fit.
- For dirty prompts, keep the current truncated-inline-diff behavior.

This preserves the user-visible behavior while changing only how the prompt is assembled.

## Implementation outline

### Template files

Add full-body templates for the assembled prompt kinds to `internal/prompt/templates/` and load them through the existing embedded template mechanism.

These templates should be limited to layout and conditional rendering. They should not perform complex formatting logic or budgeting decisions.

### Prompt rendering helpers

Add internal helpers that:

- build the render model for each prompt kind
- render a specific assembled template with that model
- iteratively shrink optional content or switch diff mode when the rendered prompt is too large

The existing helpers for large-diff fallback text can be retained and used as content sources for the template model.

### Existing APIs

Keep these entry points unchanged:

- `Build`
- `BuildWithAdditionalContext`
- `BuildDirty`
- `GetSystemPrompt`

The refactor should remain internal to the prompt package.

## Testing strategy

Update and extend tests in `internal/prompt/prompt_test.go` and related prompt-package tests.

### Coverage to preserve

- Expected sections still appear.
- Section ordering remains the same.
- Previous reviews and responses still render correctly.
- Project guidelines still render only when present.
- Additional context still renders in the correct position.

### New focused coverage

- Single-prompt over-budget cases trim optional context before current commit metadata.
- Range-prompt over-budget cases trim optional context before current range metadata.
- Codex large-diff fallback text still renders correctly.
- Non-Codex large-diff fallback text still renders correctly.
- Dirty prompts still use truncated inline diff behavior when needed.
- Rendered prompts stay under the configured budget after iterative rendering.

### Assertion style

Use direct output assertions and exact-shape checks where helpful, especially around template-driven output and fallback wording.

## Risks and mitigations

### Risk: output drift

Because prompt wording is relied on implicitly by tests and agent behavior, template migration could accidentally change whitespace, headings, or section order.

**Mitigation:** preserve current wording closely and add targeted assertions around exact rendered structure.

### Risk: budgeting regressions

Template rendering can make size accounting less obvious than the current manual writes.

**Mitigation:** keep budgeting orchestration explicit in Go and test over-budget scenarios directly.

### Risk: templates taking on business logic

If too much logic moves into templates, the result becomes hard to test and maintain.

**Mitigation:** keep templates declarative and provide already-decided data through the render model.

## Rollout

Make the refactor in place within `internal/prompt`. Do not change callers. The work can land as a single focused change set with accompanying tests.

## Success criteria

- Prompt assembly for single, range, and dirty prompts is template-driven.
- Existing prompt wording and section ordering remain materially unchanged.
- Current trimming and fallback behavior are preserved.
- Tests cover both normal and over-budget rendering behavior.
- No caller-facing API changes are required.
