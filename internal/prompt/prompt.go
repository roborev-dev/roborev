package prompt

import (
	"fmt"
	"log"
	"strings"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
)

// MaxPromptSize is the maximum size of a prompt in bytes (250KB)
// If the prompt with diffs exceeds this, we fall back to just commit info
const MaxPromptSize = 250 * 1024

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

// ReviewContext holds a commit SHA and its associated review (if any) plus responses
type ReviewContext struct {
	SHA       string
	Review    *storage.Review
	Responses []storage.Response
}

// Builder constructs review prompts
type Builder struct {
	db *storage.DB
}

// NewBuilder creates a new prompt builder
func NewBuilder(db *storage.DB) *Builder {
	return &Builder{db: db}
}

// Build constructs a review prompt for a commit or range with context from previous reviews.
// reviewType selects the system prompt variant (e.g., "security"); any default alias (see config.IsDefaultReviewType) uses the standard prompt.
func (b *Builder) Build(repoPath, gitRef string, repoID int64, contextCount int, agentName, reviewType string) (string, error) {
	if git.IsRange(gitRef) {
		return b.buildRangePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType)
	}
	return b.buildSinglePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType)
}

// BuildDirty constructs a review prompt for uncommitted (dirty) changes.
// The diff is provided directly since it was captured at enqueue time.
// reviewType selects the system prompt variant (e.g., "security"); any default alias (see config.IsDefaultReviewType) uses the standard prompt.
func (b *Builder) BuildDirty(repoPath, diff string, repoID int64, contextCount int, agentName, reviewType string) (string, error) {
	var sb strings.Builder

	// Start with system prompt for dirty changes
	promptType := "dirty"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == "design" {
		promptType = "design-review"
	}
	sb.WriteString(GetSystemPrompt(agentName, promptType))
	sb.WriteString("\n")

	// Add project-specific guidelines if configured
	if repoCfg, err := config.LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		b.writeProjectGuidelines(&sb, repoCfg.ReviewGuidelines)
	}

	// Get previous reviews for context (use HEAD as reference point)
	if contextCount > 0 && b.db != nil {
		headSHA, err := git.ResolveSHA(repoPath, "HEAD")
		if err == nil {
			contexts, err := b.getPreviousReviewContexts(repoPath, headSHA, contextCount)
			if err == nil && len(contexts) > 0 {
				b.writePreviousReviews(&sb, contexts)
			}
		}
	}

	// Uncommitted changes section
	sb.WriteString("## Uncommitted Changes\n\n")
	sb.WriteString("The following changes have not yet been committed.\n\n")

	// Build diff section
	var diffSection strings.Builder
	diffSection.WriteString("### Diff\n\n")
	diffSection.WriteString("```diff\n")
	diffSection.WriteString(diff)
	if !strings.HasSuffix(diff, "\n") {
		diffSection.WriteString("\n")
	}
	diffSection.WriteString("```\n")

	// Check if adding the diff would exceed max prompt size
	if sb.Len()+diffSection.Len() > MaxPromptSize {
		// For dirty changes, we can't tell them to "use git diff" because
		// the working tree may have changed. Just truncate with a note.
		sb.WriteString("### Diff\n\n")
		sb.WriteString("(Diff too large to include in full)\n")
		// Include truncated diff
		maxDiffLen := MaxPromptSize - sb.Len() - 100 // Leave room for closing markers
		if maxDiffLen > 1000 {
			sb.WriteString("```diff\n")
			sb.WriteString(diff[:maxDiffLen])
			sb.WriteString("\n... (truncated)\n")
			sb.WriteString("```\n")
		}
	} else {
		sb.WriteString(diffSection.String())
	}

	return sb.String(), nil
}

// buildSinglePrompt constructs a prompt for a single commit
func (b *Builder) buildSinglePrompt(repoPath, sha string, repoID int64, contextCount int, agentName, reviewType string) (string, error) {
	var sb strings.Builder

	// Start with system prompt
	promptType := "review"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == "design" {
		promptType = "design-review"
	}
	sb.WriteString(GetSystemPrompt(agentName, promptType))
	sb.WriteString("\n")

	// Add project-specific guidelines from default branch
	b.writeProjectGuidelines(&sb, loadGuidelines(repoPath))

	// Get previous reviews if requested
	if contextCount > 0 && b.db != nil {
		contexts, err := b.getPreviousReviewContexts(repoPath, sha, contextCount)
		if err != nil {
			// Log but don't fail - previous reviews are nice-to-have context
			// Just continue without them
		} else if len(contexts) > 0 {
			b.writePreviousReviews(&sb, contexts)
		}
	}

	// Include previous review attempts for this same commit (for re-reviews)
	b.writePreviousAttemptsForGitRef(&sb, sha)

	// Current commit section
	shortSHA := sha
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	// Get commit info
	info, err := git.GetCommitInfo(repoPath, sha)
	if err != nil {
		return "", fmt.Errorf("get commit info: %w", err)
	}

	sb.WriteString("## Current Commit\n\n")
	fmt.Fprintf(&sb, "**Commit:** %s\n", shortSHA)
	fmt.Fprintf(&sb, "**Author:** %s\n", info.Author)
	fmt.Fprintf(&sb, "**Subject:** %s\n", info.Subject)
	if info.Body != "" {
		fmt.Fprintf(&sb, "\n**Message:**\n%s\n", info.Body)
	}
	sb.WriteString("\n")

	// Get and include the diff
	diff, err := git.GetDiff(repoPath, sha)
	if err != nil {
		return "", fmt.Errorf("get diff: %w", err)
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

	// Check if adding the diff would exceed max prompt size
	if sb.Len()+diffSection.Len() > MaxPromptSize {
		// Fall back to just commit info without diff
		sb.WriteString("### Diff\n\n")
		sb.WriteString("(Diff too large to include - please review the commit directly)\n")
		fmt.Fprintf(&sb, "View with: git show %s\n", sha)
	} else {
		sb.WriteString(diffSection.String())
	}

	return sb.String(), nil
}

// buildRangePrompt constructs a prompt for a commit range
func (b *Builder) buildRangePrompt(repoPath, rangeRef string, repoID int64, contextCount int, agentName, reviewType string) (string, error) {
	var sb strings.Builder

	// Start with system prompt for ranges
	promptType := "range"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == "design" {
		promptType = "design-review"
	}
	sb.WriteString(GetSystemPrompt(agentName, promptType))
	sb.WriteString("\n")

	// Add project-specific guidelines from default branch
	b.writeProjectGuidelines(&sb, loadGuidelines(repoPath))

	// Get previous reviews from before the range start
	if contextCount > 0 && b.db != nil {
		startSHA, err := git.GetRangeStart(repoPath, rangeRef)
		if err == nil {
			contexts, err := b.getPreviousReviewContexts(repoPath, startSHA, contextCount)
			if err == nil && len(contexts) > 0 {
				b.writePreviousReviews(&sb, contexts)
			}
		}
	}

	// Include previous review attempts for this same range (for re-reviews)
	b.writePreviousAttemptsForGitRef(&sb, rangeRef)

	// Get commits in range
	commits, err := git.GetRangeCommits(repoPath, rangeRef)
	if err != nil {
		return "", fmt.Errorf("get range commits: %w", err)
	}

	// Commit range section
	sb.WriteString("## Commit Range\n\n")
	fmt.Fprintf(&sb, "Reviewing %d commits:\n\n", len(commits))

	for _, sha := range commits {
		info, err := git.GetCommitInfo(repoPath, sha)
		shortSHA := sha
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		if err == nil {
			fmt.Fprintf(&sb, "- %s %s\n", shortSHA, info.Subject)
		} else {
			fmt.Fprintf(&sb, "- %s\n", shortSHA)
		}
	}
	sb.WriteString("\n")

	// Get and include the combined diff for the range
	diff, err := git.GetRangeDiff(repoPath, rangeRef)
	if err != nil {
		return "", fmt.Errorf("get range diff: %w", err)
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

	// Check if adding the diff would exceed max prompt size
	if sb.Len()+diffSection.Len() > MaxPromptSize {
		// Fall back to just commit info without diff
		sb.WriteString("### Combined Diff\n\n")
		sb.WriteString("(Diff too large to include - please review the commits directly)\n")
		fmt.Fprintf(&sb, "View with: git diff %s\n", rangeRef)
	} else {
		sb.WriteString(diffSection.String())
	}

	return sb.String(), nil
}

// writePreviousReviews writes the previous reviews section to the builder
func (b *Builder) writePreviousReviews(sb *strings.Builder, contexts []ReviewContext) {
	sb.WriteString(PreviousReviewsHeader)
	sb.WriteString("\n")

	// Show in chronological order (oldest first) for narrative flow
	for i := len(contexts) - 1; i >= 0; i-- {
		ctx := contexts[i]
		shortSHA := ctx.SHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}

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

// loadMergedGuidelines loads review guidelines from the repo's default
// branch (detected via git) and the given ref, then merges them so
// branch guidelines can add lines but cannot remove base lines.
// Falls back to filesystem LoadRepoConfig only when no .roborev.toml
// exists on the default branch (not when it exists with empty guidelines).
func loadGuidelines(repoPath string) string {
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

// getPreviousReviewContexts gets the N commits before the target and looks up their reviews and responses
func (b *Builder) getPreviousReviewContexts(repoPath, sha string, count int) ([]ReviewContext, error) {
	// Get parent commits from git
	parentSHAs, err := git.GetParentCommits(repoPath, sha, count)
	if err != nil {
		return nil, fmt.Errorf("get parent commits: %w", err)
	}

	var contexts []ReviewContext
	for _, parentSHA := range parentSHAs {
		ctx := ReviewContext{SHA: parentSHA}

		// Try to look up review for this commit
		review, err := b.db.GetReviewByCommitSHA(parentSHA)
		if err == nil {
			ctx.Review = review

			// Also fetch comments for this review's job
			if review.JobID > 0 {
				responses, err := b.db.GetCommentsForJob(review.JobID)
				if err == nil {
					ctx.Responses = responses
				}
			}
		}
		// If no review found, ctx.Review stays nil

		contexts = append(contexts, ctx)
	}

	return contexts, nil
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

// BuildAddressPrompt constructs a prompt for addressing review findings
func (b *Builder) BuildAddressPrompt(repoPath string, review *storage.Review, previousAttempts []storage.Response) (string, error) {
	var sb strings.Builder

	// System prompt
	sb.WriteString(GetSystemPrompt(review.Agent, "address"))
	sb.WriteString("\n")

	// Add project-specific guidelines if configured
	if repoCfg, err := config.LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		b.writeProjectGuidelines(&sb, repoCfg.ReviewGuidelines)
	}

	// Include previous attempts to avoid repeating failed approaches
	if len(previousAttempts) > 0 {
		sb.WriteString(PreviousAttemptsHeader)
		sb.WriteString("\n")
		for _, attempt := range previousAttempts {
			fmt.Fprintf(&sb, "--- Attempt by %s at %s ---\n",
				attempt.Responder, attempt.CreatedAt.Format("2006-01-02 15:04"))
			sb.WriteString(attempt.Response)
			sb.WriteString("\n\n")
		}
	}

	// Review findings section
	fmt.Fprintf(&sb, "## Review Findings to Address (Job %d)\n\n", review.JobID)
	sb.WriteString(review.Output)
	sb.WriteString("\n\n")

	// Include the original diff for context if we have job info
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
