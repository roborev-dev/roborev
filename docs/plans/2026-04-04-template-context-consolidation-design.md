# Prompt template context consolidation

## Summary

Refactor `internal/prompt/` to replace the current set of narrow template-only `...View` structs with a smaller, consolidated template context model. The goal is not to enable user-customizable templates in this change, but to stop baking the current built-in template needs into the shape of the data model so that future customization is not blocked by overly tight, one-off structs.

This keeps the current direction of making prompt construction template-driven end to end, but changes the internal model from "many tiny wrappers designed around today’s exact templates" to "a broader, intentional prompt context API that built-in templates happen to use first."

## Goals

- Reduce the number of narrow prompt/template structs in `internal/prompt/`.
- Introduce a consolidated template-facing context model that can plausibly become the basis for future customizable templates.
- Preserve the hard architectural boundary that git access, DB lookups, config loading, and fallback selection happen before constructing the presentation context.
- Keep fitting, trimming, and fallback selection logic template-driven and free from stringbuilder-style prompt assembly.
- Move prompt-context manipulation helpers toward pointer receiver methods on the consolidated context types where that improves clarity.
- Preserve current prompt behavior for single, range, dirty, address, and system prompt rendering.

## Non-goals

- Enabling user-defined templates in this change.
- Using storage / DB structs directly as template inputs.
- Reintroducing map-driven or loosely typed prompt rendering.
- Moving git, DB, or config logic into template context methods.
- Changing prompt wording, trimming priorities, or fallback behavior except where required to preserve current behavior during the refactor.

## Current problems

The current template layer in `internal/prompt/prompt_body_templates.go` contains many very small structs:

- `singlePromptView`
- `rangePromptView`
- `dirtyPromptView`
- `addressPromptView`
- `systemPromptView`
- `optionalSectionsView`
- `currentCommitSectionView`
- `commitRangeSectionView`
- `dirtyChangesSectionView`
- `diffSectionView`
- multiple fallback-specific view structs
- multiple history/comment wrapper structs

Those types are explicit, which is better than procedural string assembly, but they are too tightly shaped around the current built-in templates. That creates two problems:

1. The type surface is larger than it needs to be, which makes render/fitting logic harder to understand.
2. The template data contract is too constrained. If templates ever become customizable, users would only be able to access whatever fields the current built-in templates happened to need.

## Design principles

### 1. Keep data acquisition separate from presentation

All git access, DB lookups, config resolution, review history loading, fallback command generation, and review-type normalization must happen before the presentation context is built.

The architecture should be:

1. Acquire data from git / config / storage
2. Normalize and derive prompt-specific state
3. Build a consolidated presentation context
4. Render and fit templates from that context

The presentation context layer must remain pure and deterministic.

### 2. Use a broader template context, not DB models

The consolidated model should not use `storage.ReviewJob`, `storage.Review`, `storage.Response`, or other persistence structs directly as the template API.

Those types describe persisted entities and storage concerns. The template layer needs normalized prompt concepts such as:

- the current review target
- optional history/context sections
- rendered diff/fallback intent
- trimming state
- prompt metadata used only by prompt/system templates

The template context should therefore be a prompt-domain model, not a storage-domain model.

### 3. Consolidate by concern, not by prompt file

The new model should use one shared root context plus a small number of reusable nested contexts grouped by responsibility. This avoids both extremes:

- too many one-off view structs
- one giant unstructured god object

### 4. Put prompt-state behavior next to the prompt-state data

Context mutation and presentation-oriented helpers should move onto the consolidated types as pointer/value receiver methods when doing so makes the fitting logic clearer.

Allowed responsibilities for context methods:

- cloning
- trimming optional sections in priority order
- dropping or blanking range metadata in priority order
- testing whether data is present
- returning derived display text that depends only on already-loaded state

Forbidden responsibilities for context methods:

- git access
- DB/storage access
- config loading
- template parsing/execution side effects

## Proposed context model

### Single-source-of-truth rule

Every datum in the consolidated template model must have exactly one home. The same concept must not appear in both `Meta` and a prompt-specific child context, or in both `Diff` and `Fallback`.

The model should follow this ownership table:

| Datum | Owner |
|------|-------|
| agent name / prompt kind / review type | `PromptMeta` |
| current date / no-skills instruction | `SystemTemplateContext` |
| single/range/dirty optional review context | `ReviewTemplateContext.Optional` |
| current single/range/dirty subject | `ReviewTemplateContext.Subject` |
| inline diff body | `ReviewTemplateContext.Diff` |
| structured fallback commands / fallback mode | `ReviewTemplateContext.Fallback` |
| address-prompt findings, diff, severity, prior attempts, guidelines, job id | `AddressTemplateContext` |

That rule is intentionally stricter than the current sketch. The refactor should consolidate the model, not create multiple overlapping sources of truth.

### Root contexts

Introduce a small set of reusable root/nested contexts. The exact field names can change during implementation, but the shape should be along these lines:

```go
type TemplateContext struct {
    Meta    PromptMeta
    Review  *ReviewTemplateContext
    Address *AddressTemplateContext
    System  *SystemTemplateContext
}

type PromptMeta struct {
    AgentName  string
    PromptType string
    ReviewType string
}
```

The root context intentionally contains more than any one built-in template needs today. That is part of the point: the context should reflect the prompt domain, not only the current layout.

### Review optional/history context

```go
type ReviewOptionalContext struct {
    ProjectGuidelines *MarkdownSection
    AdditionalContext string
    PreviousReviews   []PreviousReviewTemplateContext
    PreviousAttempts  []ReviewAttemptTemplateContext
}
```

This replaces the current optional section view wrappers with a single reusable review-prompt context.

Suggested methods:

```go
func (o *ReviewOptionalContext) TrimNext() bool
func (o ReviewOptionalContext) Clone() ReviewOptionalContext
func (o ReviewOptionalContext) IsEmpty() bool
```

`TrimNext` should preserve the current removal priority used by fitting logic.

### Review template context

```go
type ReviewTemplateContext struct {
    Kind     ReviewKind
    Optional ReviewOptionalContext
    Subject  SubjectContext
    Diff     DiffContext
    Fallback FallbackContext
}

type ReviewKind string

const (
    ReviewKindSingle ReviewKind = "single"
    ReviewKindRange  ReviewKind = "range"
    ReviewKindDirty  ReviewKind = "dirty"
)
```

This lets single/range/dirty prompts share a common template-facing shape instead of each needing a separate root view type.

### Subject context

```go
type SubjectContext struct {
    Single *SingleSubjectContext
    Range  *RangeSubjectContext
    Dirty  *DirtySubjectContext
}

type SingleSubjectContext struct {
    Commit  string
    Subject string
    Author  string
    Message string
}

type RangeSubjectContext struct {
    Count   int
    Entries []RangeEntryContext
}

type RangeEntryContext struct {
    Commit  string
    Subject string
}

type DirtySubjectContext struct {
    Description string
}
```

Exactly one of `Single`, `Range`, or `Dirty` should be populated, consistent with `ReviewTemplateContext.Kind`.

### Runtime concept mapping

To avoid ambiguity, the new model should use the current runtime concepts as follows:

| Current concept | Meaning | New model field |
|----------------|---------|-----------------|
| `PromptType` | system prompt family / entry-point family (`review`, `dirty`, `range`, `address`, `security`, `design-review`, `run`) | `PromptMeta.PromptType` |
| `ReviewType` | review policy variant (`""`, `security`, `design`, etc.) | `PromptMeta.ReviewType` |
| `ReviewKind` | reviewed artifact shape (`single`, `range`, `dirty`) | `ReviewTemplateContext.Kind` |

Rules:

- `PromptMeta.PromptType` must not be used to infer whether the subject is single/range/dirty when `ReviewTemplateContext` is present.
- `ReviewTemplateContext.Kind` applies only to review prompts, never address/system-only prompts.
- `PromptMeta.ReviewType` continues to drive system prompt selection rules, including aliases such as design-review handling.

Suggested methods:

```go
func (s SubjectContext) Clone() SubjectContext
func (s *SubjectContext) TrimSingleMessage() bool
func (s *SubjectContext) TrimSingleAuthor() bool
func (s *SubjectContext) TrimSingleSubjectTo(maxBytes int) bool
func (s *SubjectContext) BlankNextRangeSubject() bool
func (s *SubjectContext) DropLastRangeEntry() bool
```

The methods should express the current fitting strategy directly instead of scattering equivalent logic across package-level helpers.

### Diff and fallback contexts

```go
type DiffContext struct {
    Heading string
    Body    string
}

type FallbackContext struct {
    Mode    FallbackMode
    Commit  *CommitFallbackContext
    Range   *RangeFallbackContext
    Dirty   *DirtyFallbackContext
    Generic *GenericFallbackContext
}
```

The exact nested fallback types can be consolidated further during implementation. The important shift is that fallback-related template data lives under one coherent branch of the context model rather than as several unrelated one-purpose structs.

Suggested methods:

```go
func (d DiffContext) Clone() DiffContext
func (d DiffContext) HasBody() bool
func (f FallbackContext) Clone() FallbackContext
func (f FallbackContext) IsEmpty() bool
```

Templates should render fallback sections from structured fallback fields, not from a second duplicate markdown string stored beside them.

### Address and system contexts

```go
type AddressTemplateContext struct {
    ProjectGuidelines *MarkdownSection
    PreviousAttempts  []AddressAttemptTemplateContext
    SeverityFilter    string
    ReviewFindings    string
    OriginalDiff      string
    JobID             int64
}

type SystemTemplateContext struct {
    NoSkillsInstruction string
    CurrentDate         string
}
```

`AddressTemplateContext` and `SystemTemplateContext` remain separate branches inside the one universal `TemplateContext` root because they represent different prompt families, but each datum still lives in only one place.

### Shared simple types

```go
type MarkdownSection struct {
    Heading string
    Body    string
}

type ReviewCommentTemplateContext struct {
    Responder string
    Response  string
}

type PreviousReviewTemplateContext struct {
    Commit    string
    Output    string
    Comments  []ReviewCommentTemplateContext
    Available bool
}

type ReviewAttemptTemplateContext struct {
    Label    string
    Agent    string
    When     string
    Output   string
    Comments []ReviewCommentTemplateContext
}

type AddressAttemptTemplateContext struct {
    Responder string
    Response  string
    When      string
}
```

These should replace the repetitive "same shape, different name" wrapper types where possible, and the implementation plan should use these names consistently rather than mixing `...Context`, `...TemplateContext`, and old `...View` names.

## Behavioral invariants to preserve

The consolidation must preserve the current prompt-building integration behavior, not merely the current template layout.

### Guideline source invariants

- Single and range review prompts must continue to source review guidelines through `LoadGuidelines(repoPath)`, which prefers the repo default branch config and only falls back to working-tree filesystem config when the default branch does not provide `.roborev.toml`.
- Dirty prompts must continue to read guidelines from the working tree via `config.LoadRepoConfig(repoPath)`.
- Address prompts must continue to read guidelines from the working tree via `config.LoadRepoConfig(repoPath)`.
- The consolidation must not normalize these into one generic "guidelines loader" that changes the security behavior for single/range prompts.

### Explicit ordering invariants

- Optional review sections must be trimmed in this exact order: previous attempts, previous reviews, additional context, project guidelines.
- Previous review context must render oldest → newest in the final prompt.
- Review attempts and address attempts must preserve their input order when rendered.

### Address-prompt diff invariant

- `BuildAddressPrompt` must continue to include the original diff without applying current exclude patterns. This behavior is intentional and is covered by `TestBuildAddressPromptShowsFullDiff`.

### Ordering and content invariants

- Previous reviews must continue to render in the same chronological order used by the current prompt package.
- Previous attempts must continue to include stored response comments where they exist.
- Section ordering for single, range, dirty, address, and system prompts must remain stable unless a future spec explicitly changes wording/layout.

### Size-budget invariants

- Prompt fitting remains byte-budget based.
- UTF-8 truncation must remain rune-safe.
- Dirty truncated fallback rendering must continue to reserve room for rendered wrapper content such as closing fences and `... (truncated)` markers.
- Hard-capping remains a final guardrail, not the primary fitting strategy.

## Rendering model

### Single shared template contract

All prompt templates should be rendered from the consolidated context model.

The implementation may still keep convenience wrappers such as:

- `renderReviewPrompt(...)`
- `renderAddressPrompt(...)`
- `renderSystemPrompt(...)`

but those wrappers should all delegate to a single shared execution layer that takes the universal `TemplateContext` root. Prompt-family-specific branches (`Review`, `Address`, `System`) are populated as needed for a given template family.

That means the template API becomes intentional and consistent across:

- assembled review prompt templates
- shared section partials
- fallback partials
- agent-specific system prompt templates
- default system prompt templates

### Fallback contract

The fallback contract needs to stay explicit so the planner does not accidentally reintroduce duplicate representations.

- Templates should receive structured fallback data via `FallbackContext`.
- Fallback partials should render from that structured data.
- `DiffContext` should contain only the inline diff body that is already ready to render.
- The model should not store the same fallback as both structured fields and a duplicate markdown blob.
- The only pre-rendered fallback body that may remain as template input is the dirty truncated snippet payload when the fitting algorithm has already produced final snippet text that must be wrapped by a template.

This preserves a single source of truth while still allowing fitting logic to choose among fallback variants before template execution.

### Context-aware receiver helpers

Methods on the context types should replace some of the current package-level prompt manipulation helpers. Examples:

- `ReviewOptionalContext.TrimNext()`
- `SubjectContext.BlankNextRangeSubject()`
- `SubjectContext.DropLastRangeEntry()`
- `TemplateContext.Clone()`
- `ReviewTemplateContext.Clone()`

The top-level fitting functions should become thin orchestrators that:

1. clone a context
2. apply one context mutation step
3. re-render
4. repeat until the prompt fits or the hard cap is needed

## Fitting and selection behavior

The current fitting behavior should be preserved, but the operations should be expressed through the consolidated contexts.

### Single prompt fitting

Preserve the current priority:

1. trim optional sections
2. drop commit message
3. drop author
4. shrink subject
5. hard cap only as final guardrail

### Range prompt fitting

Preserve the current priority:

1. trim optional sections
2. blank trailing range subjects
3. drop trailing entries
4. only then fall back to final hard cap if needed

When comparing fallback candidates, selection must still account for how much review context is sacrificed. The current regressions fixed on `gotmpl` should remain covered:

- isolated candidate evaluation
- metadata-loss-aware comparison
- optional-section-loss-aware comparison

### Dirty prompt fitting

Preserve the current behavior for:

- trimming optional context before over-shrinking dirty fallback snippets
- maintaining the fallback-only path when no snippet fits
- preserving rendered fallback wrappers such as closing fences and truncation markers

## Migration plan

### Phase 1: Introduce consolidated contexts

- Add the new shared context types to `internal/prompt/`.
- Rename or retire the current `prompt.ReviewContext` history type before introducing `ReviewTemplateContext`; the two names must not coexist with ambiguous meaning. Prefer a migration rename such as `HistoricalReviewContext` for the existing storage-backed helper type.
- Add constructors/builders that translate existing gathered prompt data into the new contexts.
- Keep the old render helpers temporarily so behavior can be migrated in small steps.

### Migration bridge and coexistence rules

The implementation plan should name the existing types/functions being phased out and the bridge strategy for each:

- `singlePromptView`
- `rangePromptView`
- `dirtyPromptView`
- `addressPromptView`
- `systemPromptView`
- `optionalSectionsView`
- `diffSectionView` and the fallback-specific view structs
- the current storage-backed `ReviewContext` in `prompt.go`
- render helpers such as `renderSinglePrompt`, `renderRangePrompt`, `renderDirtyPrompt`, `renderAddressPrompt`, and `renderSystemPrompt`

The bridge should allow incremental migration by keeping wrappers/adapters temporarily, but the target end state should be the consolidated universal `TemplateContext` model described above.

### Phase 2: Move templates to the new context shape

- Update assembled prompt templates and partials to use the consolidated context.
- Update system prompt templates to use the same broader model where appropriate.
- Remove reliance on the narrow `...View` roots.

### Phase 3: Move fitting mutations onto context methods

- Replace scattered package-level trimming/mutation helpers with context receiver methods.
- Keep builder functions responsible only for orchestration and data acquisition.

### Phase 4: Delete obsolete narrow types and helpers

- Remove the old prompt-specific wrappers once templates and fitters no longer depend on them.
- Collapse redundant render helpers that existed only to adapt to the old type surface.

## Testing strategy

Keep the current regression-heavy test approach, but shift coverage to the new consolidated model.

### Rendering tests

Verify that built-in templates still render the same visible behavior for:

- single review prompts
- range review prompts
- dirty review prompts
- address prompts
- system prompt templates
- unchanged public builder entry points (`Build`, `BuildWithAdditionalContext`, `BuildDirty`, `BuildAddressPrompt`, `GetSystemPrompt`, and `LoadGuidelines`)

### Context-method tests

Add focused tests for receiver methods such as:

- optional trimming order
- range subject blanking order
- range entry dropping order
- clone isolation
- fallback candidate comparison inputs

### Regression tests

Preserve the currently important regressions around:

- previous review ordering
- previous attempt comments
- single/range guideline sourcing through `LoadGuidelines`
- dirty/address guideline sourcing from working-tree config
- address prompts continuing to show the full original diff regardless of current exclude patterns
- dirty truncated fallback fence preservation
- dirty fallback-only optional restoration
- range diff preservation when entries are trimmed
- fallback richness selection without excessive metadata/optional-context loss
- system prompt template coverage through embedded template discovery
- the explicit optional trimming order: previous attempts → previous reviews → additional context → project guidelines

## Risks and mitigations

### Risk: consolidated context turns into a god object

Mitigation:
- use one shared root but keep concerns grouped into a small number of nested structs
- keep data acquisition out of the context layer
- keep methods presentation-oriented only

### Risk: templates become harder to read because they see a broader model

Mitigation:
- keep nested field organization clean and predictable
- prefer small reusable partials
- add helper methods for common presence checks or derived strings

### Risk: refactor breaks subtle fit behavior

Mitigation:
- preserve current regression tests
- add direct tests for context mutation methods
- migrate in phases rather than rewriting builder and templates in one step

## Recommendation

Implement the consolidation using a shared root context plus a small set of reusable nested prompt-domain contexts. Do not move to raw DB/storage models. Do not move to untyped map-based rendering. Keep all external data loading outside the presentation layer, and make context methods responsible only for deterministic prompt-state manipulation and derived presentation helpers.
