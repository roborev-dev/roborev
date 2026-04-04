# Go-template Prompt Builder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor `internal/prompt/` so every review-prompt construction path is template-driven, including system prompt fallbacks, shared prompt sections, single/range/dirty bodies, and address prompts.

**Architecture:** Keep prompt orchestration in Go and move prompt structure into nested `text/template` templates backed by typed view structs per prompt family. Builders should gather data, choose fallback mode, and enforce prompt budgets, while templates render shared sections and final document shape. The finished package should have no stringbuilder-style markdown assembly for prompts.

**Tech Stack:** Go, `text/template`, `embed.FS`, existing prompt tests in `internal/prompt/*_test.go`, `testify`.

---

## File structure

- Modify: `internal/prompt/prompt.go` — replace remaining prompt markdown assembly in single/range/dirty/address paths with typed view construction.
- Modify: `internal/prompt/templates.go` — render default system prompts from templates instead of Go string constants and concatenation.
- Modify: `internal/prompt/prompt_body_templates.go` — evolve the current flat body renderers into nested typed renderers for single/range/dirty/address prompts.
- Modify: `internal/prompt/prompt_body_templates_test.go` — add renderer and fit tests for nested sections.
- Modify: `internal/prompt/prompt_test.go` — add public regression coverage for address prompts and section ordering.
- Modify: `internal/prompt/templates_test.go` — add fallback-template regression coverage for system prompts.
- Create: `internal/prompt/templates/prompt_sections.tmpl` — shared partials for guidelines, previous reviews, previous attempts, commit/range sections, and diff blocks.
- Create: `internal/prompt/templates/assembled_address.tmpl` — assembled address prompt body.
- Modify: `internal/prompt/templates/assembled_single.tmpl` — switch from flat field concatenation to named nested partials.
- Modify: `internal/prompt/templates/assembled_range.tmpl` — switch from flat field concatenation to named nested partials.
- Modify: `internal/prompt/templates/assembled_dirty.tmpl` — switch from flat field concatenation to named nested partials.
- Create: `internal/prompt/templates/default_review.tmpl` — default review fallback prompt template.
- Create: `internal/prompt/templates/default_security.tmpl` — default security fallback prompt template.
- Create: `internal/prompt/templates/default_address.tmpl` — default address fallback prompt template.
- Create: `internal/prompt/templates/default_design_review.tmpl` — default design-review fallback prompt template.

### Task 1: Convert prompt body templates to typed nested sections

**Files:**
- Modify: `internal/prompt/prompt_body_templates.go`
- Modify: `internal/prompt/prompt_body_templates_test.go`
- Create: `internal/prompt/templates/prompt_sections.tmpl`
- Modify: `internal/prompt/templates/assembled_single.tmpl`
- Modify: `internal/prompt/templates/assembled_range.tmpl`
- Modify: `internal/prompt/templates/assembled_dirty.tmpl`

- [ ] **Step 1: Write the failing nested-renderer test**

Add this test to `internal/prompt/prompt_body_templates_test.go`:

```go
func TestRenderSinglePromptBodyUsesNestedSections(t *testing.T) {
	view := singlePromptView{
		Optional: optionalSectionsView{
			ProjectGuidelines: &markdownSectionView{
				Heading: "## Project Guidelines",
				Body:    "Prefer composition over inheritance.",
			},
			AdditionalContext: "## Pull Request Discussion\n\nNewest comment first.\n\n",
		},
		Current: currentCommitSectionView{
			Commit:  "abc1234",
			Subject: "template prompt rendering",
			Author:  "Test User",
		},
		Diff: diffSectionView{
			Heading: "### Diff",
			Body:    "```diff\n+line\n```\n",
		},
	}

	body, err := renderSinglePrompt(view)
	require.NoError(t, err)

	assert.Contains(t, body, "## Project Guidelines")
	assert.Contains(t, body, "## Pull Request Discussion")
	assert.Contains(t, body, "## Current Commit")
	assert.Contains(t, body, "**Subject:** template prompt rendering")
	assert.Contains(t, body, "### Diff")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/prompt -run TestRenderSinglePromptBodyUsesNestedSections -count=1`
Expected: FAIL with `undefined: singlePromptView` and `undefined: renderSinglePrompt`

- [ ] **Step 3: Add nested view structs and section partials**

Replace the flat body views in `internal/prompt/prompt_body_templates.go` with nested typed views like:

```go
type markdownSectionView struct {
	Heading string
	Body    string
}

type previousReviewView struct {
	Commit    string
	Output    string
	Comments  []reviewCommentView
	Available bool
}

type reviewCommentView struct {
	Responder string
	Response  string
}

type reviewAttemptView struct {
	Label    string
	Agent    string
	Output   string
	Comments []reviewCommentView
}

type optionalSectionsView struct {
	ProjectGuidelines *markdownSectionView
	AdditionalContext string
	PreviousReviews   []previousReviewView
	PreviousAttempts  []reviewAttemptView
}

type currentCommitSectionView struct {
	Commit  string
	Subject string
	Author  string
	Message string
}

type commitRangeEntryView struct {
	Commit  string
	Subject string
}

type commitRangeSectionView struct {
	Entries []commitRangeEntryView
}

type dirtyChangesSectionView struct {
	Description string
}

type diffSectionView struct {
	Heading  string
	Body     string
	Fallback string
}

type singlePromptView struct {
	Optional optionalSectionsView
	Current  currentCommitSectionView
	Diff     diffSectionView
}

type rangePromptView struct {
	Optional optionalSectionsView
	Current  commitRangeSectionView
	Diff     diffSectionView
}

type dirtyPromptView struct {
	Optional optionalSectionsView
	Current  dirtyChangesSectionView
	Diff     diffSectionView
}
```

Create `internal/prompt/templates/prompt_sections.tmpl` with named partials:

```gotemplate
{{define "project_guidelines"}}{{with .ProjectGuidelines}}{{.Heading}}

{{.Body}}

{{end}}{{end}}
{{define "additional_context"}}{{- .AdditionalContext -}}{{end}}
{{define "current_commit"}}## Current Commit

**Commit:** {{.Commit}}

**Subject:** {{.Subject}}
**Author:** {{.Author}}
{{if .Message}}

**Message:**
{{.Message}}
{{end}}

{{end}}
{{define "diff_block"}}{{.Heading}}

{{if .Fallback}}{{.Fallback}}{{else}}{{.Body}}{{end}}{{end}}
```

Update `assembled_single.tmpl`, `assembled_range.tmpl`, and `assembled_dirty.tmpl` to call named templates instead of concatenating raw fields.

- [ ] **Step 4: Add explicit renderer entry points**

In `internal/prompt/prompt_body_templates.go`, expose family-specific renderers:

```go
func renderSinglePrompt(view singlePromptView) (string, error) {
	return executePromptTemplate("assembled_single.tmpl", view)
}

func renderRangePrompt(view rangePromptView) (string, error) {
	return executePromptTemplate("assembled_range.tmpl", view)
}

func renderDirtyPrompt(view dirtyPromptView) (string, error) {
	return executePromptTemplate("assembled_dirty.tmpl", view)
}
```

Parse the shared section templates together with the assembled templates.

- [ ] **Step 5: Run the focused renderer tests**

Run: `go test ./internal/prompt -run 'TestRenderSinglePromptBodyUsesNestedSections|TestRenderPromptBodiesPreserveRawText' -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/prompt/prompt_body_templates.go internal/prompt/prompt_body_templates_test.go internal/prompt/templates/prompt_sections.tmpl internal/prompt/templates/assembled_single.tmpl internal/prompt/templates/assembled_range.tmpl internal/prompt/templates/assembled_dirty.tmpl
git commit -m "refactor(prompt): add nested prompt body templates"
```

### Task 2: Migrate single, range, and dirty builders away from prompt string assembly

**Files:**
- Modify: `internal/prompt/prompt.go`
- Modify: `internal/prompt/prompt_body_templates.go`
- Modify: `internal/prompt/prompt_body_templates_test.go`
- Modify: `internal/prompt/prompt_test.go`

- [ ] **Step 1: Write the failing section-order regression test**

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

- [ ] **Step 2: Run the test to establish the guardrail**

Run: `go test ./internal/prompt -run TestBuildPromptWithAdditionalContextAndPreviousAttemptsPreservesSectionOrder -count=1`
Expected: PASS

- [ ] **Step 3: Replace `write*` markdown helpers with typed view builders**

In `internal/prompt/prompt.go`, replace helpers that append directly into `strings.Builder` with helpers that return typed data, for example:

```go
func (b *Builder) buildOptionalSectionsView(repoPath, gitRef, additionalContext string, contextCount int) optionalSectionsView {
	view := optionalSectionsView{}

	if guidelines := LoadGuidelines(repoPath); strings.TrimSpace(guidelines) != "" {
		view.ProjectGuidelines = &markdownSectionView{
			Heading: "## Project Guidelines",
			Body:    strings.TrimSpace(guidelines),
		}
	}
	if strings.TrimSpace(additionalContext) != "" {
		view.AdditionalContext = strings.TrimSpace(additionalContext) + "\n\n"
	}
	view.PreviousAttempts = b.previousAttemptViews(gitRef)
	return view
}
```

Do the same for previous reviews, current commit metadata, current range metadata, and dirty sections.

- [ ] **Step 4: Update the fit helpers to work on nested fields**

In `internal/prompt/prompt_body_templates.go`, make the fit helpers operate on typed fields instead of flat markdown chunks:

```go
func fitSinglePrompt(limit int, view singlePromptView) (string, error) {
	body, err := renderSinglePrompt(view)
	if err != nil {
		return "", err
	}
	if len(body) <= limit {
		return body, nil
	}

	if view.Optional.ProjectGuidelines != nil {
		view.Optional.ProjectGuidelines.Body = truncateUTF8(view.Optional.ProjectGuidelines.Body, 0)
	}
	view.Optional.AdditionalContext = ""
	view.Optional.PreviousReviews = nil
	view.Optional.PreviousAttempts = nil

	body, err = renderSinglePrompt(view)
	if err != nil {
		return "", err
	}
	return hardCapPrompt(body, limit), nil
}
```

Preserve the existing priority order: optional sections first, then overflow metadata if necessary, then hard cap as a final guardrail.

- [ ] **Step 5: Wire `buildSinglePrompt`, `buildRangePrompt`, and `BuildDirty` to nested renderers**

Update `internal/prompt/prompt.go` so each builder path:

```go
view := singlePromptView{
	Optional: optionalView,
	Current: currentCommitSectionView{
		Commit:  shortSHA,
		Subject: info.Subject,
		Author:  info.Author,
		Message: info.Body,
	},
	Diff: diffSectionView{
		Heading: "### Diff",
		Body:    renderedDiff,
		Fallback: fallbackText,
	},
}

body, err := fitSinglePrompt(bodyLimit, view)
if err != nil {
	return "", err
}
return requiredPrefix + body, nil
```

Apply the same pattern to range and dirty prompts so prompt markdown is rendered from templates instead of concatenated in Go.

- [ ] **Step 6: Run the focused prompt-body regression tests**

Run: `go test ./internal/prompt -run 'TestBuildPromptWithAdditionalContextAndPreviousAttemptsPreservesSectionOrder|TestBuildPromptCodexOversizedDiffKeepsCurrentCommitMetadataWhenTrimming|TestBuildRangePromptCodexOversizedDiffKeepsCurrentRangeMetadataWhenTrimming|TestBuildDirtySmallCapStaysWithinCap' -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/prompt/prompt.go internal/prompt/prompt_body_templates.go internal/prompt/prompt_body_templates_test.go internal/prompt/prompt_test.go
git commit -m "refactor(prompt): render prompt bodies from nested views"
```

### Task 3: Move address prompts onto the same template path

**Files:**
- Modify: `internal/prompt/prompt.go`
- Modify: `internal/prompt/prompt_body_templates.go`
- Modify: `internal/prompt/prompt_test.go`
- Modify: `internal/prompt/templates/prompt_sections.tmpl`
- Create: `internal/prompt/templates/assembled_address.tmpl`

- [ ] **Step 1: Write the failing address renderer regression test**

Add this test to `internal/prompt/prompt_test.go`:

```go
func TestBuildAddressPromptRendersPreviousAttemptsAndOriginalDiff(t *testing.T) {
	repoPath, sha := setupExcludePatternRepo(t)
	b := NewBuilder(nil)

	review := &storage.Review{
		Agent:  "test",
		Output: "Found issue: check custom.dat",
		Job: &storage.ReviewJob{GitRef: sha},
	}
	attempts := []storage.Response{{Responder: "developer", Response: "Tried a narrow fix"}}

	prompt, err := b.BuildAddressPrompt(repoPath, review, attempts, "medium")
	require.NoError(t, err)

	assert.Contains(t, prompt, "## Previous Addressing Attempts")
	assert.Contains(t, prompt, "Tried a narrow fix")
	assert.Contains(t, prompt, "## Review Findings to Address")
	assert.Contains(t, prompt, "## Original Commit Diff")
}
```

- [ ] **Step 2: Run the test to verify the current behavior before refactoring**

Run: `go test ./internal/prompt -run TestBuildAddressPromptRendersPreviousAttemptsAndOriginalDiff -count=1`
Expected: PASS

- [ ] **Step 3: Add `addressPromptView` and a dedicated assembled template**

In `internal/prompt/prompt_body_templates.go`, add:

```go
type addressAttemptView struct {
	Responder string
	Response  string
	When      string
}

type addressPromptView struct {
	ProjectGuidelines *markdownSectionView
	PreviousAttempts  []addressAttemptView
	SeverityFilter    string
	ReviewFindings    string
	OriginalDiff      string
	JobID             int64
}
```

Create `internal/prompt/templates/assembled_address.tmpl`:

````gotemplate
{{template "project_guidelines" .}}
{{if .PreviousAttempts}}## Previous Addressing Attempts

{{range .PreviousAttempts}}--- Attempt by {{.Responder}} at {{.When}} ---
{{.Response}}

{{end}}{{end}}
{{if .SeverityFilter}}{{.SeverityFilter}}
{{end}}## Review Findings to Address (Job {{.JobID}})

{{.ReviewFindings}}

{{if .OriginalDiff}}## Original Commit Diff (for context)

```diff
{{.OriginalDiff}}```
{{end}}
````

- [ ] **Step 4: Render `BuildAddressPrompt` through the new template**

Replace the `strings.Builder` path in `internal/prompt/prompt.go` with typed view construction:

```go
view := addressPromptView{
	ProjectGuidelines: guidelinesView,
	PreviousAttempts:  addressAttemptViews(previousAttempts),
	SeverityFilter:    config.SeverityInstruction(minSeverity),
	ReviewFindings:    review.Output,
	OriginalDiff:      diff,
	JobID:             review.JobID,
}

body, err := renderAddressPrompt(view)
if err != nil {
	return "", err
}
return GetSystemPrompt(review.Agent, "address") + "\n" + body, nil
```

- [ ] **Step 5: Run the focused address tests**

Run: `go test ./internal/prompt -run 'TestBuildAddressPromptRendersPreviousAttemptsAndOriginalDiff|TestBuildAddressPromptShowsFullDiff' -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/prompt/prompt.go internal/prompt/prompt_body_templates.go internal/prompt/prompt_test.go internal/prompt/templates/prompt_sections.tmpl internal/prompt/templates/assembled_address.tmpl
git commit -m "refactor(prompt): template address prompts"
```

### Task 4: Template the default system prompt fallbacks

**Files:**
- Modify: `internal/prompt/templates.go`
- Modify: `internal/prompt/templates_test.go`
- Create: `internal/prompt/templates/default_review.tmpl`
- Create: `internal/prompt/templates/default_security.tmpl`
- Create: `internal/prompt/templates/default_address.tmpl`
- Create: `internal/prompt/templates/default_design_review.tmpl`

- [ ] **Step 1: Write the failing fallback-template test**

Add this test to `internal/prompt/templates_test.go`:

```go
func TestGetSystemPrompt_DefaultFallbacksRenderFromTemplates(t *testing.T) {
	fixedTime := time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)
	mockNow := func() time.Time { return fixedTime }

	got := getSystemPrompt("unknown-agent", "security", mockNow)

	assert.Contains(t, got, "You are a security code reviewer")
	assert.Contains(t, got, "Do NOT use any external skills")
	assert.Contains(t, got, "Current date: 2030-06-15 (UTC)")
}
```

- [ ] **Step 2: Run the test to verify the guardrail**

Run: `go test ./internal/prompt -run TestGetSystemPrompt_DefaultFallbacksRenderFromTemplates -count=1`
Expected: PASS

- [ ] **Step 3: Move default fallback text into templates**

In `internal/prompt/templates.go`, replace the constant-switch fallback path with a typed renderer, for example:

```go
type systemPromptView struct {
	NoSkillsInstruction string
	CurrentDate         string
}

var systemPromptTemplates = template.Must(template.New("system-prompts").ParseFS(
	templateFS,
	"templates/default_review.tmpl",
	"templates/default_security.tmpl",
	"templates/default_address.tmpl",
	"templates/default_design_review.tmpl",
))

func renderSystemPrompt(name string, view systemPromptView) (string, error) {
	var buf bytes.Buffer
	if err := systemPromptTemplates.ExecuteTemplate(&buf, name, view); err != nil {
		return "", err
	}
	return buf.String(), nil
}
```

Use that renderer in the fallback branch of `getSystemPrompt` and keep the existing agent-specific file precedence.

- [ ] **Step 4: Preserve the current no-skills and date behavior structurally**

Write the template bodies so the old suffix logic becomes template data instead of string concatenation. Example `internal/prompt/templates/default_review.tmpl`:

```gotemplate
You are a code reviewer. Review the git commit shown below for:

1. **Bugs**: Logic errors, off-by-one errors, null/undefined issues, race conditions
2. **Security**: Injection vulnerabilities, auth issues, data exposure
3. **Testing gaps**: Missing unit tests, edge cases not covered, e2e/integration test gaps
4. **Regressions**: Changes that might break existing functionality
5. **Code quality**: Duplication that should be refactored, overly complex logic, unclear naming

Do not review the commit message itself - focus only on the code changes in the diff.

After reviewing, provide:

1. A brief summary of what the commit does
2. Any issues found, listed with:
   - Severity (high/medium/low)
   - File and line reference where possible
   - A brief explanation of the problem and suggested fix

If you find no issues, state "No issues found." after the summary.{{.NoSkillsInstruction}}

Current date: {{.CurrentDate}} (UTC)
```

Repeat the same pattern for security, address, and design-review fallbacks.

- [ ] **Step 5: Run the system prompt regression tests**

Run: `go test ./internal/prompt -run 'TestGetSystemPrompt_Fallbacks|TestGetSystemPrompt_DateInjection|TestGetSystemPrompt_DefaultFallbacksRenderFromTemplates' -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/prompt/templates.go internal/prompt/templates_test.go internal/prompt/templates/default_review.tmpl internal/prompt/templates/default_security.tmpl internal/prompt/templates/default_address.tmpl internal/prompt/templates/default_design_review.tmpl
git commit -m "refactor(prompt): template system prompt fallbacks"
```

### Task 5: Remove legacy prompt assembly and verify the package

**Files:**
- Modify: `internal/prompt/prompt.go`
- Modify: `internal/prompt/templates.go`
- Modify: `internal/prompt/prompt_body_templates.go`
- Modify: `internal/prompt/prompt_test.go`
- Modify: `internal/prompt/templates_test.go`

- [ ] **Step 1: Remove legacy prompt constants and dead write helpers**

Delete the prompt-construction code that is now obsolete, including the default fallback constants and the remaining markdown writers:

```go
const SystemPromptSingle = `...`
const SystemPromptDirty = `...`
const SystemPromptRange = `...`
const SystemPromptSecurity = `...`
const SystemPromptAddress = `...`
const SystemPromptDesignReview = `...`

func (b *Builder) writePreviousReviews(sb *strings.Builder, contexts []ReviewContext) { ... }
func (b *Builder) writeProjectGuidelines(sb *strings.Builder, guidelines string) { ... }
func (b *Builder) writeAdditionalContext(sb *strings.Builder, additionalContext string) { ... }
func (b *Builder) writePreviousAttemptsForGitRef(sb *strings.Builder, gitRef string) { ... }
```

Only delete helpers once all call sites have moved to typed view builders and template rendering. Keep standard template execution helpers that use `bytes.Buffer`; the goal is to remove ad hoc prompt assembly, not template execution internals.

Also remove any now-unused flat body view structs and renderer names superseded by the nested prompt-family renderers.

- [ ] **Step 2: Add one final no-stringbuilder regression test**

Add this test to `internal/prompt/prompt_test.go`:

```go
func TestBuildPromptStillIncludesNestedSectionsAfterTemplateMigration(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	builder := NewBuilder(nil)

	prompt, err := builder.BuildWithAdditionalContext(
		repoPath,
		commits[len(commits)-1],
		0,
		0,
		"claude-code",
		"",
		"## Pull Request Discussion\n\nNewest comment first.\n",
	)
	require.NoError(t, err)

	assert.Contains(t, prompt, "## Pull Request Discussion")
	assert.Contains(t, prompt, "## Current Commit")
	assert.Contains(t, prompt, "### Diff")
}
```

- [ ] **Step 3: Run formatting, vet, and prompt-package tests**

Run: `go fmt ./... && go vet ./... && go test ./internal/prompt -count=1`
Expected: all commands succeed

- [ ] **Step 4: Run the full test suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/prompt/prompt.go internal/prompt/templates.go internal/prompt/prompt_body_templates.go internal/prompt/prompt_body_templates_test.go internal/prompt/prompt_test.go internal/prompt/templates_test.go internal/prompt/templates/prompt_sections.tmpl internal/prompt/templates/assembled_single.tmpl internal/prompt/templates/assembled_range.tmpl internal/prompt/templates/assembled_dirty.tmpl internal/prompt/templates/assembled_address.tmpl internal/prompt/templates/default_review.tmpl internal/prompt/templates/default_security.tmpl internal/prompt/templates/default_address.tmpl internal/prompt/templates/default_design_review.tmpl
git commit -m "refactor(prompt): finish template-driven prompt rendering"
```

## Self-review

### Spec coverage

- **All prompt families covered:** Tasks 2, 3, and 4 cover single/range/dirty/address/system prompts.
- **Typed nested-template direction covered:** Task 1 introduces nested view structs and shared partials.
- **No stringbuilder-style prompt assembly remains:** Task 5 removes legacy constants and `write*` helpers after migration.
- **Budgeting and fallback behavior preserved:** Task 2 keeps fit helpers and existing fallback semantics.
- **Public API preserved:** all tasks keep the same exported builder entry points.

### Placeholder scan

- No `TODO`, `TBD`, or deferred implementation markers remain.
- Every code-changing step includes concrete file paths and code snippets.
- Every verification step includes exact commands and expected outcomes.

### Type consistency

- Prompt-body renderers use `singlePromptView`, `rangePromptView`, `dirtyPromptView`, and `addressPromptView` consistently.
- Shared nested types (`markdownSectionView`, `optionalSectionsView`, `diffSectionView`) are introduced once and reused consistently.
- System prompt rendering uses `systemPromptView` consistently in `templates.go` and `templates_test.go`.
