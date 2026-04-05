package prompt

import (
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
)

// MaxPromptSize is the legacy maximum size of a prompt in bytes (250KB).
// New code should use Builder.maxPromptSize() which respects config.
const MaxPromptSize = 250 * 1024

// noSkillsInstruction tells agents not to delegate the review to external
// tools or skills, and to return only the final review content. Verdict
// parsing intentionally does not try to decode narrative process updates or
// caveats in free-form prose; those output-shaping issues are better handled
// in the prompt than in deterministic parsing heuristics.
const noSkillsInstruction = `

IMPORTANT: You are being invoked by roborev to perform this review directly. Do NOT use any external skills, slash commands, or CLI tools (such as "roborev review") to delegate this task. Perform the review yourself by analyzing the diff provided below.

Return only the final review content. Do NOT include process narration, progress updates, or front matter such as "Reviewing the diff..." or "I'm checking...".
If you use tools while reviewing, finish all tool use before emitting the final review, and put the final review only after the last tool call.`

// SystemPromptSingle is the base instruction for single commit reviews
const SystemPromptSingle = `You are a code reviewer. Review the git commit shown below for:

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

If you find no issues, state "No issues found." after the summary.`

// SystemPromptDirty is the base instruction for reviewing uncommitted (dirty) changes
const SystemPromptDirty = `You are a code reviewer. Review the following uncommitted changes for:

1. **Bugs**: Logic errors, off-by-one errors, null/undefined issues, race conditions
2. **Security**: Injection vulnerabilities, auth issues, data exposure
3. **Testing gaps**: Missing unit tests, edge cases not covered, e2e/integration test gaps
4. **Regressions**: Changes that might break existing functionality
5. **Code quality**: Duplication that should be refactored, overly complex logic, unclear naming

After reviewing, provide:

1. A brief summary of what the changes do
2. Any issues found, listed with:
   - Severity (high/medium/low)
   - File and line reference where possible
   - A brief explanation of the problem and suggested fix

If you find no issues, state "No issues found." after the summary.`

// SystemPromptRange is the base instruction for commit range reviews
const SystemPromptRange = `You are a code reviewer. Review the git commit range shown below for:

1. **Bugs**: Logic errors, off-by-one errors, null/undefined issues, race conditions
2. **Security**: Injection vulnerabilities, auth issues, data exposure
3. **Testing gaps**: Missing unit tests, edge cases not covered, e2e/integration test gaps
4. **Regressions**: Changes that might break existing functionality
5. **Code quality**: Duplication that should be refactored, overly complex logic, unclear naming

Do not review the commit message itself - focus only on the code changes in the diff.

After reviewing, provide:

1. A brief summary of what the commits do
2. Any issues found, listed with:
   - Severity (high/medium/low)
   - File and line reference where possible
   - A brief explanation of the problem and suggested fix

If you find no issues, state "No issues found." after the summary.`

// PreviousReviewsHeader introduces the previous reviews section
const PreviousReviewsHeader = `
## Previous Reviews

The following are reviews of recent commits in this repository. Use them as context
to understand ongoing work and to check if the current commit addresses previous feedback.

**Important:** Reviews may include responses from developers. Pay attention to these responses -
they may indicate known issues that should be ignored, explain why certain patterns exist,
or provide context that affects how you should evaluate similar code in the current commit.
`

// ProjectGuidelinesHeader introduces the project-specific guidelines section
const ProjectGuidelinesHeader = `
## Project Guidelines

The following are project-specific guidelines for this repository. Take these into account
when reviewing the code - they may override or supplement the default review criteria.
`

// PreviousAttemptsForCommitHeader introduces previous review attempts for the same commit
const PreviousAttemptsForCommitHeader = `
## Previous Review Attempts

This commit has been reviewed before. The following are previous review results and any
responses from developers. Use this context to:
- Avoid repeating issues that have been marked as known/acceptable
- Check if previously identified issues are still present
- Consider developer responses about why certain patterns exist
`

// InRangeReviewsHeader introduces per-commit reviews within a range review
const InRangeReviewsHeader = `
## Per-Commit Reviews in This Range

The following commits in this range have already been individually reviewed.
Issues found in earlier commits may have been fixed by later commits in the range.

Do not re-raise issues identified below unless they persist in the final code.
Focus on cross-commit interactions and problems not caught by per-commit reviews.
`

// ReviewContext holds a commit SHA and its associated review (if any) plus responses
type ReviewContext struct {
	SHA       string
	Review    *storage.Review
	Responses []storage.Response
}

// Builder constructs review prompts
type Builder struct {
	db        *storage.DB
	globalCfg *config.Config // optional global config for exclude patterns
}

// NewBuilder creates a new prompt builder
func NewBuilder(db *storage.DB) *Builder {
	return &Builder{db: db}
}

// NewBuilderWithConfig creates a prompt builder that also resolves
// global config settings (e.g., exclude_patterns).
func NewBuilderWithConfig(
	db *storage.DB, globalCfg *config.Config,
) *Builder {
	return &Builder{db: db, globalCfg: globalCfg}
}

// resolveMaxPromptSize returns the effective prompt budget from config.
func (b *Builder) resolveMaxPromptSize(repoPath string) int {
	return config.ResolveMaxPromptSize(repoPath, b.globalCfg)
}

// resolveExcludes returns the merged exclude patterns for a repo.
// Security reviews skip repo-level patterns to prevent a compromised
// default branch from hiding files from review.
func (b *Builder) resolveExcludes(
	repoPath, reviewType string,
) []string {
	return config.ResolveExcludePatterns(
		repoPath, b.globalCfg, reviewType,
	)
}

// Build constructs a review prompt for a commit or range with context from previous reviews.
// reviewType selects the system prompt variant (e.g., "security"); any default alias (see config.IsDefaultReviewType) uses the standard prompt.
func (b *Builder) Build(repoPath, gitRef string, repoID int64, contextCount int, agentName, reviewType string) (string, error) {
	return b.BuildWithAdditionalContext(repoPath, gitRef, repoID, contextCount, agentName, reviewType, "")
}

// BuildWithAdditionalContext constructs a review prompt with an optional
// caller-provided markdown context block inserted ahead of the current diff.
func (b *Builder) BuildWithAdditionalContext(repoPath, gitRef string, repoID int64, contextCount int, agentName, reviewType, additionalContext string) (string, error) {
	if git.IsRange(gitRef) {
		return b.buildRangePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, additionalContext)
	}
	return b.buildSinglePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, additionalContext)
}

// BuildDirty constructs a review prompt for uncommitted (dirty) changes.
// The diff is provided directly since it was captured at enqueue time.
// reviewType selects the system prompt variant (e.g., "security"); any default alias (see config.IsDefaultReviewType) uses the standard prompt.
func (b *Builder) BuildDirty(repoPath, diff string, repoID int64, contextCount int, agentName, reviewType string) (string, error) {
	// Start with system prompt for dirty changes
	promptType := "dirty"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == config.ReviewTypeDesign {
		promptType = "design-review"
	}
	requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"

	var optionalContext strings.Builder

	// Add project-specific guidelines if configured
	if repoCfg, err := config.LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		b.writeProjectGuidelines(&optionalContext, repoCfg.ReviewGuidelines)
	}

	// Get previous reviews for context (use HEAD as reference point)
	if contextCount > 0 && b.db != nil {
		headSHA, err := git.ResolveSHA(repoPath, "HEAD")
		if err == nil {
			contexts, err := b.getPreviousReviewContexts(repoPath, headSHA, contextCount)
			if err == nil && len(contexts) > 0 {
				b.writePreviousReviews(&optionalContext, contexts)
			}
		}
	}

	currentRequired := "## Uncommitted Changes\n\n" +
		"The following changes have not yet been committed.\n\n"

	// Build diff section
	var diffSection strings.Builder
	diffSection.WriteString("### Diff\n\n")
	diffSection.WriteString("```diff\n")
	diffSection.WriteString(diff)
	if !strings.HasSuffix(diff, "\n") {
		diffSection.WriteString("\n")
	}
	diffSection.WriteString("```\n")

	// Trim optional context if it alone would exceed the prompt cap
	promptCap := b.resolveMaxPromptSize(repoPath)
	optCtx := optionalContext.String()
	requiredLen := len(requiredPrefix) + len(currentRequired)
	if requiredLen+len(optCtx) > promptCap {
		optCtx = truncateUTF8(optCtx, max(0, promptCap-requiredLen))
	}

	var sb strings.Builder
	sb.WriteString(requiredPrefix)
	sb.WriteString(optCtx)
	sb.WriteString(currentRequired)

	// Check if adding the diff would exceed max prompt size
	if sb.Len()+diffSection.Len() > promptCap {
		// For dirty changes, we can't tell them to "use git diff" because
		// the working tree may have changed. Just truncate with a note.
		sb.WriteString("### Diff\n\n")
		sb.WriteString("(Diff too large to include in full)\n")
		// Include truncated diff
		maxDiffLen := promptCap - sb.Len() - 100 // Leave room for closing markers
		if maxDiffLen > 1000 {
			sb.WriteString("```diff\n")
			sb.WriteString(truncateUTF8(diff, maxDiffLen))
			sb.WriteString("\n... (truncated)\n")
			sb.WriteString("```\n")
		}
	} else {
		sb.WriteString(diffSection.String())
	}

	return hardCapPrompt(sb.String(), promptCap), nil
}

func isCodexReviewAgent(agentName string) bool {
	return strings.EqualFold(strings.TrimSpace(agentName), "codex")
}
func writeLongestFitting(sb *strings.Builder, limit int, variants ...string) {
	if len(variants) == 0 || limit <= 0 {
		return
	}
	shortest := variants[len(variants)-1]
	remaining := limit - sb.Len()
	if remaining <= 0 {
		return
	}
	for _, variant := range variants {
		if len(variant) <= remaining {
			sb.WriteString(variant)
			return
		}
	}
	sb.WriteString(truncateUTF8(shortest, remaining))
}

func buildPromptPreservingCurrentSection(
	requiredPrefix, optionalContext, currentRequired, currentOverflow string,
	limit int,
	variants ...string,
) string {
	shortestLen := 0
	if len(variants) > 0 {
		shortestLen = len(variants[len(variants)-1])
	}
	softBudget := max(0, limit-len(requiredPrefix)-len(currentRequired)-shortestLen)
	softLen := len(optionalContext) + len(currentOverflow)
	if softLen > softBudget {
		overflow := softLen - softBudget
		if overflow > 0 && len(optionalContext) > 0 {
			originalLen := len(optionalContext)
			trimmedLen := max(0, len(optionalContext)-overflow)
			optionalContext = truncateUTF8(optionalContext, trimmedLen)
			overflow -= originalLen - len(optionalContext)
		}
		if overflow > 0 && len(currentOverflow) > 0 {
			currentOverflow = truncateUTF8(currentOverflow, max(0, len(currentOverflow)-overflow))
		}
	}

	var sb strings.Builder
	sb.WriteString(requiredPrefix)
	sb.WriteString(optionalContext)
	sb.WriteString(currentRequired)
	sb.WriteString(currentOverflow)
	writeLongestFitting(&sb, limit, variants...)
	return hardCapPrompt(sb.String(), limit)
}

func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

func hardCapPrompt(prompt string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(prompt) <= limit {
		return prompt
	}
	return truncateUTF8(prompt, limit)
}

// safeForMarkdown filters pathspec args to only those that can be
// safely embedded in markdown inline code spans. Args containing
// backticks or control characters are dropped.
func safeForMarkdown(args []string) []string {
	var safe []string
	for _, a := range args {
		ok := true
		for _, r := range a {
			if r < ' ' || r == '`' || r == 0x7f {
				ok = false
				break
			}
		}
		if ok {
			safe = append(safe, a)
		}
	}
	return safe
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func renderShellCommand(args ...string) string {
	var quoted []string
	for _, arg := range args {
		if needsShellQuoting(arg) {
			quoted = append(quoted, shellQuote(arg))
			continue
		}
		quoted = append(quoted, arg)
	}
	return strings.Join(quoted, " ")
}

func needsShellQuoting(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("@%_+=:,./-~", r):
		default:
			return true
		}
	}
	return false
}

func codexCommitInspectionFallbackVariants(sha string, pathspecArgs []string) []string {
	statCmd := renderShellCommand(append([]string{"git", "show", "--stat", "--summary", sha, "--"}, pathspecArgs...)...)
	diffCmd := renderShellCommand(append([]string{"git", "show", "--format=medium", "--unified=80", sha, "--"}, pathspecArgs...)...)
	filesCmd := renderShellCommand(append([]string{"git", "diff-tree", "--no-commit-id", "--name-only", "-r", sha, "--"}, pathspecArgs...)...)
	return []string{
		fmt.Sprintf("### Diff\n\n"+
			"(Diff too large to include inline)\n\n"+
			"For Codex in read-only review mode, inspect the commit locally with read-only git commands before writing findings. Do not claim the diff is inaccessible unless these commands fail.\n\n"+
			"Use commands like:\n"+
			"- `%s`\n"+
			"- `%s`\n"+
			"- `%s`\n"+
			"- `git show %s -- path/to/file`\n\n"+
			"Review the actual diff before writing findings.\n",
			statCmd, diffCmd, filesCmd, shellQuote(sha)),
		fmt.Sprintf("### Diff\n\n"+
			"(Diff too large to include inline)\n\n"+
			"For Codex in read-only review mode, inspect the commit locally before writing findings.\n"+
			"- `%s`\n"+
			"- `%s`\n",
			statCmd, diffCmd),
		fmt.Sprintf("### Diff\n\n"+
			"(Diff too large to include inline)\n\n"+
			"For Codex, inspect locally with `%s`.\n",
			diffCmd),
		fmt.Sprintf("### Diff\n\n"+
			"(Diff too large; for Codex run `%s` locally.)\n",
			renderShellCommand(append([]string{"git", "show", sha, "--"}, pathspecArgs...)...)),
	}
}

func codexRangeInspectionFallbackVariants(rangeRef string, pathspecArgs []string) []string {
	logCmd := renderShellCommand("git", "log", "--oneline", rangeRef)
	statCmd := renderShellCommand(append([]string{"git", "diff", "--stat", rangeRef, "--"}, pathspecArgs...)...)
	diffCmd := renderShellCommand(append([]string{"git", "diff", "--unified=80", rangeRef, "--"}, pathspecArgs...)...)
	filesCmd := renderShellCommand(append([]string{"git", "diff", "--name-only", rangeRef, "--"}, pathspecArgs...)...)
	return []string{
		fmt.Sprintf("### Combined Diff\n\n"+
			"(Diff too large to include inline)\n\n"+
			"For Codex in read-only review mode, inspect the commit range locally with read-only git commands before writing findings. Do not claim the diff is inaccessible unless these commands fail.\n\n"+
			"Use commands like:\n"+
			"- `%s`\n"+
			"- `%s`\n"+
			"- `%s`\n"+
			"- `%s`\n\n"+
			"Review the actual diff before writing findings.\n",
			logCmd, statCmd, diffCmd, filesCmd),
		fmt.Sprintf("### Combined Diff\n\n"+
			"(Diff too large to include inline)\n\n"+
			"For Codex in read-only review mode, inspect the commit range locally before writing findings.\n"+
			"- `%s`\n"+
			"- `%s`\n",
			statCmd, diffCmd),
		fmt.Sprintf("### Combined Diff\n\n"+
			"(Diff too large to include inline)\n\n"+
			"For Codex, inspect locally with `%s`.\n",
			diffCmd),
		fmt.Sprintf("### Combined Diff\n\n"+
			"(Diff too large; for Codex run `%s` locally.)\n",
			renderShellCommand(append([]string{"git", "diff", rangeRef, "--"}, pathspecArgs...)...)),
	}
}

// buildSinglePrompt constructs a prompt for a single commit
func (b *Builder) buildSinglePrompt(repoPath, sha string, repoID int64, contextCount int, agentName, reviewType, additionalContext string) (string, error) {
	// Start with system prompt
	promptType := "review"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == config.ReviewTypeDesign {
		promptType = "design-review"
	}
	requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"

	var optionalContext strings.Builder

	// Add project-specific guidelines from default branch
	b.writeProjectGuidelines(&optionalContext, LoadGuidelines(repoPath))
	b.writeAdditionalContext(&optionalContext, additionalContext)

	// Get previous reviews if requested
	if contextCount > 0 && b.db != nil {
		contexts, err := b.getPreviousReviewContexts(repoPath, sha, contextCount)
		if err != nil {
			// Log but don't fail - previous reviews are nice-to-have context
			// Just continue without them
		} else if len(contexts) > 0 {
			b.writePreviousReviews(&optionalContext, contexts)
		}
	}

	// Include previous review attempts for this same commit (for re-reviews)
	b.writePreviousAttemptsForGitRef(&optionalContext, sha)

	// Current commit section
	shortSHA := git.ShortSHA(sha)

	// Get commit info
	info, err := git.GetCommitInfo(repoPath, sha)
	if err != nil {
		return "", fmt.Errorf("get commit info: %w", err)
	}

	var currentRequired strings.Builder
	currentRequired.WriteString("## Current Commit\n\n")
	fmt.Fprintf(&currentRequired, "**Commit:** %s\n", shortSHA)
	currentRequired.WriteString("\n")

	var currentOverflow strings.Builder
	fmt.Fprintf(&currentOverflow, "**Subject:** %s\n", info.Subject)
	fmt.Fprintf(&currentOverflow, "**Author:** %s\n", info.Author)
	currentOverflow.WriteString("\n")
	if info.Body != "" {
		fmt.Fprintf(&currentOverflow, "**Message:**\n%s\n\n", info.Body)
	}

	// Get and include the diff.
	// Budget the diff from non-trimmable sections only; optional context
	// is trimmed afterward to fit the remaining space.
	excludes := b.resolveExcludes(repoPath, reviewType)
	promptCap := b.resolveMaxPromptSize(repoPath)
	diffWrap := len("### Diff\n\n```diff\n") + len("\n```\n") + 1
	requiredLen := len(requiredPrefix) + currentRequired.Len() + currentOverflow.Len()
	diffLimit := max(0, promptCap-requiredLen-diffWrap)
	diff, truncated, err := git.GetDiffLimited(repoPath, sha, diffLimit, excludes...)
	if err != nil {
		return "", fmt.Errorf("get diff: %w", err)
	}
	if truncated {
		pathspecArgs := safeForMarkdown(git.FormatExcludeArgs(excludes))
		if isCodexReviewAgent(agentName) {
			return buildPromptPreservingCurrentSection(
				requiredPrefix,
				optionalContext.String(),
				currentRequired.String(),
				currentOverflow.String(),
				promptCap,
				codexCommitInspectionFallbackVariants(sha, pathspecArgs)...,
			), nil
		} else {
			fallback := "### Diff\n\n" +
				"(Diff too large to include - please review the commit directly)\n" +
				"View with: " + renderShellCommand("git", "show", sha) + "\n"
			return buildPromptPreservingCurrentSection(
				requiredPrefix,
				optionalContext.String(),
				currentRequired.String(),
				currentOverflow.String(),
				promptCap,
				fallback,
			), nil
		}
	}

	// Build diff section
	var diffSection strings.Builder
	diffSection.WriteString("### Diff\n\n")
	diffSection.WriteString("```diff\n")
	diffSection.WriteString(diff)
	if !strings.HasSuffix(diff, "\n") {
		diffSection.WriteString("\n")
	}
	diffSection.WriteString("```\n")

	// Trim optional context to fit remaining budget after the diff
	optCtx := optionalContext.String()
	ctxBudget := promptCap - requiredLen - diffSection.Len()
	if len(optCtx) > ctxBudget {
		optCtx = truncateUTF8(optCtx, max(0, ctxBudget))
	}

	var sb strings.Builder
	sb.WriteString(requiredPrefix)
	sb.WriteString(optCtx)
	sb.WriteString(currentRequired.String())
	sb.WriteString(currentOverflow.String())
	sb.WriteString(diffSection.String())
	return sb.String(), nil
}

// buildRangePrompt constructs a prompt for a commit range
func (b *Builder) buildRangePrompt(repoPath, rangeRef string, repoID int64, contextCount int, agentName, reviewType, additionalContext string) (string, error) {
	// Start with system prompt for ranges
	promptType := "range"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == config.ReviewTypeDesign {
		promptType = "design-review"
	}
	requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"

	var optionalContext strings.Builder

	// Add project-specific guidelines from default branch
	b.writeProjectGuidelines(&optionalContext, LoadGuidelines(repoPath))
	b.writeAdditionalContext(&optionalContext, additionalContext)

	// Get previous reviews from before the range start
	if contextCount > 0 && b.db != nil {
		startSHA, err := git.GetRangeStart(repoPath, rangeRef)
		if err == nil {
			contexts, err := b.getPreviousReviewContexts(repoPath, startSHA, contextCount)
			if err == nil && len(contexts) > 0 {
				b.writePreviousReviews(&optionalContext, contexts)
			}
		}
	}

	// Include previous review attempts for this same range (for re-reviews)
	b.writePreviousAttemptsForGitRef(&optionalContext, rangeRef)

	// Get commits in range
	commits, err := git.GetRangeCommits(repoPath, rangeRef)
	if err != nil {
		return "", fmt.Errorf("get range commits: %w", err)
	}

	// Include per-commit reviews for commits in this range so the
	// reviewer doesn't re-raise issues already found and addressed.
	b.writeInRangeReviews(&optionalContext, commits)

	// Commit range section
	var currentRequired strings.Builder
	currentRequired.WriteString("## Commit Range\n\n")
	fmt.Fprintf(&currentRequired, "Reviewing %d commits:\n\n", len(commits))

	var currentOverflow strings.Builder
	for _, sha := range commits {
		info, err := git.GetCommitInfo(repoPath, sha)
		shortSHA := git.ShortSHA(sha)
		if err == nil {
			fmt.Fprintf(&currentOverflow, "- %s %s\n", shortSHA, info.Subject)
		} else {
			fmt.Fprintf(&currentOverflow, "- %s\n", shortSHA)
		}
	}
	currentOverflow.WriteString("\n")

	// Get and include the combined diff for the range.
	// Budget the diff from non-trimmable sections only; optional context
	// is trimmed afterward to fit the remaining space.
	excludes := b.resolveExcludes(repoPath, reviewType)
	promptCap := b.resolveMaxPromptSize(repoPath)
	diffWrap := len("### Combined Diff\n\n```diff\n") + len("\n```\n") + 1
	requiredLen := len(requiredPrefix) + currentRequired.Len() + currentOverflow.Len()
	diffLimit := max(0, promptCap-requiredLen-diffWrap)
	diff, truncated, err := git.GetRangeDiffLimited(repoPath, rangeRef, diffLimit, excludes...)
	if err != nil {
		return "", fmt.Errorf("get range diff: %w", err)
	}
	if truncated {
		pathspecArgs := safeForMarkdown(git.FormatExcludeArgs(excludes))
		if isCodexReviewAgent(agentName) {
			return buildPromptPreservingCurrentSection(
				requiredPrefix,
				optionalContext.String(),
				currentRequired.String(),
				currentOverflow.String(),
				promptCap,
				codexRangeInspectionFallbackVariants(rangeRef, pathspecArgs)...,
			), nil
		} else {
			fallback := "### Combined Diff\n\n" +
				"(Diff too large to include - please review the commits directly)\n" +
				"View with: " + renderShellCommand("git", "diff", rangeRef) + "\n"
			return buildPromptPreservingCurrentSection(
				requiredPrefix,
				optionalContext.String(),
				currentRequired.String(),
				currentOverflow.String(),
				promptCap,
				fallback,
			), nil
		}
	}

	// Build diff section
	var diffSection strings.Builder
	diffSection.WriteString("### Combined Diff\n\n")
	diffSection.WriteString("```diff\n")
	diffSection.WriteString(diff)
	if !strings.HasSuffix(diff, "\n") {
		diffSection.WriteString("\n")
	}
	diffSection.WriteString("```\n")

	// Trim optional context to fit remaining budget after the diff
	optCtx := optionalContext.String()
	ctxBudget := promptCap - requiredLen - diffSection.Len()
	if len(optCtx) > ctxBudget {
		optCtx = truncateUTF8(optCtx, max(0, ctxBudget))
	}

	var sb strings.Builder
	sb.WriteString(requiredPrefix)
	sb.WriteString(optCtx)
	sb.WriteString(currentRequired.String())
	sb.WriteString(currentOverflow.String())
	sb.WriteString(diffSection.String())
	return sb.String(), nil
}

// writePreviousReviews writes the previous reviews section to the builder
func (b *Builder) writePreviousReviews(sb *strings.Builder, contexts []ReviewContext) {
	sb.WriteString(PreviousReviewsHeader)
	sb.WriteString("\n")

	// Show in chronological order (oldest first) for narrative flow
	for i := len(contexts) - 1; i >= 0; i-- {
		ctx := contexts[i]
		shortSHA := git.ShortSHA(ctx.SHA)

		fmt.Fprintf(sb, "--- Review for commit %s ---\n", shortSHA)
		if ctx.Review != nil {
			sb.WriteString(ctx.Review.Output)
		} else {
			sb.WriteString("No review available.")
		}
		sb.WriteString("\n")

		// Include responses to this review
		if len(ctx.Responses) > 0 {
			sb.WriteString("\nComments on this review:\n")
			for _, resp := range ctx.Responses {
				fmt.Fprintf(sb, "- %s: %q\n", resp.Responder, resp.Response)
			}
		}
		sb.WriteString("\n")
	}
}

// writeProjectGuidelines writes the project-specific guidelines section
func (b *Builder) writeProjectGuidelines(sb *strings.Builder, guidelines string) {
	if guidelines == "" {
		return
	}

	sb.WriteString(ProjectGuidelinesHeader)
	sb.WriteString("\n")
	sb.WriteString(strings.TrimSpace(guidelines))
	sb.WriteString("\n\n")
}

func (b *Builder) writeAdditionalContext(sb *strings.Builder, additionalContext string) {
	if strings.TrimSpace(additionalContext) == "" {
		return
	}
	sb.WriteString(strings.TrimSpace(additionalContext))
	sb.WriteString("\n\n")
}

// LoadGuidelines loads review guidelines from the repo's default
// branch, falling back to filesystem config when the default branch
// has no .roborev.toml.
func LoadGuidelines(repoPath string) string {
	// Load review guidelines from the default branch (origin/main,
	// origin/master, etc.). Branch-specific guidelines are intentionally
	// ignored to prevent prompt injection from untrusted PR authors.
	if defaultBranch, err := git.GetDefaultBranch(repoPath); err == nil {
		cfg, err := config.LoadRepoConfigFromRef(repoPath, defaultBranch)
		if err != nil {
			if config.IsConfigParseError(err) {
				log.Printf("prompt: invalid .roborev.toml on %s: %v",
					defaultBranch, err)
				return ""
			}
			log.Printf("prompt: failed to read .roborev.toml from %s: %v"+
				" (will try filesystem)", defaultBranch, err)
		} else if cfg != nil {
			return cfg.ReviewGuidelines
		}
	}

	// Fall back to filesystem config when default branch has no config
	// (e.g., no remote, or .roborev.toml not yet committed).
	if fsCfg, err := config.LoadRepoConfig(repoPath); err == nil && fsCfg != nil {
		return fsCfg.ReviewGuidelines
	}
	return ""
}

// writePreviousAttemptsForGitRef writes previous review attempts for the same git ref (commit or range)
func (b *Builder) writePreviousAttemptsForGitRef(sb *strings.Builder, gitRef string) {
	if b.db == nil {
		return
	}

	reviews, err := b.db.GetAllReviewsForGitRef(gitRef)
	if err != nil || len(reviews) == 0 {
		return
	}

	sb.WriteString(PreviousAttemptsForCommitHeader)
	sb.WriteString("\n")

	for i, review := range reviews {
		fmt.Fprintf(sb, "--- Review Attempt %d (%s, %s) ---\n",
			i+1, review.Agent, review.CreatedAt.Format("2006-01-02 15:04"))
		sb.WriteString(review.Output)
		sb.WriteString("\n")

		// Fetch and include comments for this review
		if review.JobID > 0 {
			responses, err := b.db.GetCommentsForJob(review.JobID)
			if err == nil && len(responses) > 0 {
				sb.WriteString("\nComments on this review:\n")
				for _, resp := range responses {
					fmt.Fprintf(sb, "- %s: %q\n", resp.Responder, resp.Response)
				}
			}
		}
		sb.WriteString("\n")
	}
}

// writeInRangeReviews writes per-commit reviews for commits within a range.
// This gives the range reviewer context about issues already found and
// addressed, preventing duplicate findings.
func (b *Builder) writeInRangeReviews(sb *strings.Builder, commits []string) {
	if b.db == nil || len(commits) == 0 {
		return
	}

	contexts := b.lookupReviewContexts(commits, true)
	if len(contexts) == 0 {
		return
	}

	sb.WriteString(InRangeReviewsHeader)
	sb.WriteString("\n")

	for _, ctx := range contexts {
		shortSHA := git.ShortSHA(ctx.SHA)
		verdict := storage.ParseVerdict(ctx.Review.Output)
		verdictLabel := "unknown"
		switch verdict {
		case "P":
			verdictLabel = "passed"
		case "F":
			verdictLabel = "failed"
		}

		fmt.Fprintf(sb, "--- Commit %s (%s, %s) ---\n",
			shortSHA, ctx.Review.Agent, verdictLabel)
		sb.WriteString(ctx.Review.Output)
		sb.WriteString("\n")

		if len(ctx.Responses) > 0 {
			sb.WriteString("\nComments on this review:\n")
			for _, resp := range ctx.Responses {
				fmt.Fprintf(sb, "- %s: %q\n", resp.Responder, resp.Response)
			}
		}
		sb.WriteString("\n")
	}
}

// lookupReviewContexts fetches review contexts for the given SHAs.
// When skipMissing is true, SHAs without reviews are omitted; when false,
// they are included with Review == nil (used by writePreviousReviews to
// show "No review available").
func (b *Builder) lookupReviewContexts(shas []string, skipMissing bool) []ReviewContext {
	var contexts []ReviewContext
	for _, sha := range shas {
		review, err := b.db.GetReviewByCommitSHA(sha)
		if err != nil {
			if skipMissing {
				continue
			}
			contexts = append(contexts, ReviewContext{SHA: sha})
			continue
		}
		ctx := ReviewContext{SHA: sha, Review: review}
		if review.JobID > 0 {
			if responses, err := b.db.GetCommentsForJob(review.JobID); err == nil {
				ctx.Responses = responses
			}
		}
		contexts = append(contexts, ctx)
	}
	return contexts
}

// getPreviousReviewContexts gets the N commits before the target and looks up their reviews and responses
func (b *Builder) getPreviousReviewContexts(repoPath, sha string, count int) ([]ReviewContext, error) {
	parentSHAs, err := git.GetParentCommits(repoPath, sha, count)
	if err != nil {
		return nil, fmt.Errorf("get parent commits: %w", err)
	}
	return b.lookupReviewContexts(parentSHAs, false), nil
}

// SystemPromptDesignReview is the base instruction for reviewing design documents.
// The input is a code diff (commit, range, or uncommitted changes) that is expected
// to contain design artifacts such as PRDs, task lists, or architectural proposals.
const SystemPromptDesignReview = `You are a design reviewer. The changes shown below are expected to contain design artifacts — PRDs, task lists, architectural proposals, or similar planning documents. Review them for:

1. **Completeness**: Are goals, non-goals, success criteria, and edge cases defined?
2. **Feasibility**: Are technical decisions grounded in the actual codebase?
3. **Task scoping**: Are implementation stages small enough to review incrementally? Are dependencies ordered correctly?
4. **Missing considerations**: Security, performance, backwards compatibility, error handling
5. **Clarity**: Are decisions justified and understandable?

If the changes do not appear to contain design documents, note this and review whatever design intent is evident from the code changes.

After reviewing, provide:

1. A brief summary of what the design proposes
2. PRD findings, listed with:
   - Severity (high/medium/low)
   - A brief explanation of the issue and suggested improvement
3. Task list findings, listed with:
   - Severity (high/medium/low)
   - A brief explanation of the issue and suggested improvement
4. Any missing considerations not covered by the design
5. A verdict: Pass or Fail with brief justification

If you find no issues, state "No issues found." after the summary.`

// BuildSimple constructs a simpler prompt without database context
func BuildSimple(repoPath, sha, agentName string) (string, error) {
	b := &Builder{}
	return b.Build(repoPath, sha, 0, 0, agentName, "")
}

// SystemPromptSecurity is the instruction for security-focused reviews
const SystemPromptSecurity = `You are a security code reviewer. Analyze the code changes shown below with a security-first mindset. Focus on:

1. **Injection vulnerabilities**: SQL injection, command injection, XSS, template injection, LDAP injection, header injection
2. **Authentication & authorization**: Missing auth checks, privilege escalation, insecure session handling, broken access control
3. **Credential exposure**: Hardcoded secrets, API keys, passwords, tokens in source code or logs
4. **Path traversal**: Unsanitized file paths, directory traversal via user input, symlink attacks
5. **Unsafe patterns**: Unsafe deserialization, insecure random number generation, missing input validation, buffer overflows
6. **Dependency concerns**: Known vulnerable dependencies, typosquatting risks, pinning issues
7. **CI/CD security**: Workflow injection via pull_request_target, script injection via untrusted inputs, excessive permissions
8. **Data handling**: Sensitive data in logs, missing encryption, insecure data storage, PII exposure
9. **Concurrency issues**: Race conditions leading to security bypasses, TOCTOU vulnerabilities
10. **Error handling**: Information leakage via error messages, missing error checks on security-critical operations

For each finding, provide:
- Severity (critical/high/medium/low)
- File and line reference
- Description of the vulnerability
- Suggested remediation

If you find no security issues, state "No issues found." after the summary.
Do not report code quality or style issues unless they have security implications.`

// SystemPromptAddress is the instruction for addressing review findings
const SystemPromptAddress = `You are a code assistant. Your task is to address the findings from a code review.

Make the minimal changes necessary to address these findings:
- Be pragmatic and simple - don't over-engineer
- Focus on the specific issues mentioned
- Don't refactor unrelated code
- Don't add unnecessary abstractions or comments
- Don't make cosmetic changes

After making changes:
1. Run the build command to verify the code compiles
2. Run tests to verify nothing is broken
3. Fix any build errors or test failures before finishing

For Go projects, use: GOCACHE=/tmp/go-build go build ./... and GOCACHE=/tmp/go-build go test ./...
(The GOCACHE override is needed for sandbox compatibility)

IMPORTANT: Do NOT commit changes yourself. Just modify the files. The caller will handle committing.

When finished, provide a brief summary in this format (this will be used in the commit message):

Changes:
- <first change>
- <second change>
...

Keep the summary concise (under 10 bullet points). Put the most important changes first.`

// PreviousAttemptsHeader introduces previous addressing attempts section
const PreviousAttemptsHeader = `
## Previous Addressing Attempts

The following are previous attempts to address this or related reviews.
Learn from these to avoid repeating approaches that didn't fully resolve the issues.
Be pragmatic - if previous attempts were rejected for being too minor, make more substantive fixes.
If they were rejected for being over-engineered, keep it simpler.
`

// UserCommentsHeader introduces user-authored comments on a review.
const UserCommentsHeader = `## User Comments

The following comments were left by the developer on this review.
Take them into account when applying fixes — they may flag false
positives, provide additional context, or request specific approaches.

`

// IsToolResponse returns true when the response was left by an automated
// tool (roborev-fix, roborev-refine, etc.) rather than a human user.
func IsToolResponse(r storage.Response) bool {
	return strings.HasPrefix(r.Responder, "roborev-")
}

// SplitResponses partitions responses into tool-generated attempts and
// user-authored comments based on the Responder field.
func SplitResponses(responses []storage.Response) (toolAttempts, userComments []storage.Response) {
	for _, r := range responses {
		if IsToolResponse(r) {
			toolAttempts = append(toolAttempts, r)
		} else {
			userComments = append(userComments, r)
		}
	}
	return
}

// FormatToolAttempts renders automated tool responses (roborev-fix,
// roborev-refine) into a prompt section. Returns empty string when empty.
func FormatToolAttempts(attempts []storage.Response) string {
	if len(attempts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(PreviousAttemptsHeader)
	sb.WriteString("\n")
	for _, attempt := range attempts {
		fmt.Fprintf(&sb, "--- Attempt by %s at %s ---\n",
			attempt.Responder, attempt.CreatedAt.Format("2006-01-02 15:04"))
		sb.WriteString(attempt.Response)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// FormatUserComments renders user-authored comments into a prompt section.
// Returns empty string when there are no comments.
func FormatUserComments(comments []storage.Response) string {
	if len(comments) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(UserCommentsHeader)
	for _, c := range comments {
		fmt.Fprintf(&sb, "**%s** (%s):\n%s\n\n",
			c.Responder, c.CreatedAt.Format("2006-01-02 15:04"), c.Response)
	}
	return sb.String()
}

// BuildAddressPrompt constructs a prompt for addressing review findings.
// When minSeverity is non-empty, a severity filtering instruction is
// injected before the findings section.
func (b *Builder) BuildAddressPrompt(repoPath string, review *storage.Review, previousAttempts []storage.Response, minSeverity string) (string, error) {
	var sb strings.Builder

	// System prompt
	sb.WriteString(GetSystemPrompt(review.Agent, "address"))
	sb.WriteString("\n")

	// Add project-specific guidelines if configured
	if repoCfg, err := config.LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		b.writeProjectGuidelines(&sb, repoCfg.ReviewGuidelines)
	}

	// Split responses into tool attempts and user comments
	toolAttempts, userComments := SplitResponses(previousAttempts)
	sb.WriteString(FormatToolAttempts(toolAttempts))
	sb.WriteString(FormatUserComments(userComments))

	// Severity filter instruction (before findings)
	if inst := config.SeverityInstruction(minSeverity); inst != "" {
		sb.WriteString(inst)
		sb.WriteString("\n")
	}

	// Review findings section
	fmt.Fprintf(&sb, "## Review Findings to Address (Job %d)\n\n", review.JobID)
	sb.WriteString(review.Output)
	sb.WriteString("\n\n")

	// Include the original diff for context if we have job info.
	// Don't apply user exclude patterns — the diff should match
	// what the original review saw so findings stay relevant.
	// Built-in lockfile excludes still apply (hardcoded in GetDiff).
	// Tradeoff: without user excludes the diff may be larger and
	// trip the MaxPromptSize/2 guard, but that's a soft degradation
	// vs hiding the exact file the findings reference.
	if review.Job != nil && review.Job.GitRef != "" && review.Job.GitRef != "dirty" {
		diff, err := git.GetDiff(repoPath, review.Job.GitRef)
		if err == nil && len(diff) > 0 && len(diff) < MaxPromptSize/2 {
			sb.WriteString("## Original Commit Diff (for context)\n\n")
			sb.WriteString("```diff\n")
			sb.WriteString(diff)
			if !strings.HasSuffix(diff, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("```\n")
		}
	}

	return sb.String(), nil
}
