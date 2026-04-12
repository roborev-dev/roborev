package prompt

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
)

// ErrDiffTruncatedNoFile is returned when the diff is too large to
// inline and no snapshot file path was provided. Callers should write
// the diff to a file and retry with BuildWithDiffFile.
var ErrDiffTruncatedNoFile = errors.New("diff too large to inline and no snapshot file available")

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

// DiffFilePathPlaceholder is a sentinel path embedded in prebuilt
// prompts for oversized diffs. The worker replaces it with a real
// diff file path at execution time so the stored prompt remains
// reusable across retries.
const DiffFilePathPlaceholder = "/tmp/roborev diff placeholder"

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
func (b *Builder) Build(repoPath, gitRef string, repoID int64, contextCount int, agentName, reviewType, minSeverity string) (string, error) {
	return b.BuildWithAdditionalContext(repoPath, gitRef, repoID, contextCount, agentName, reviewType, minSeverity, "")
}

// BuildWithAdditionalContext constructs a review prompt with an optional
// caller-provided markdown context block inserted ahead of the current diff.
func (b *Builder) BuildWithAdditionalContext(repoPath, gitRef string, repoID int64, contextCount int, agentName, reviewType, minSeverity, additionalContext string) (string, error) {
	opts := buildOpts{
		additionalContext: additionalContext,
		minSeverity:       minSeverity,
	}
	if git.IsRange(gitRef) {
		return b.buildRangePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, opts)
	}
	return b.buildSinglePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, opts)
}

// BuildWithAdditionalContextAndDiffFile constructs a review prompt with
// caller-provided markdown context and an optional oversized-diff file
// reference for sandboxed Codex reviews.
func (b *Builder) BuildWithAdditionalContextAndDiffFile(repoPath, gitRef string, repoID int64, contextCount int, agentName, reviewType, minSeverity, additionalContext, diffFilePath string) (string, error) {
	opts := buildOpts{
		additionalContext: additionalContext,
		diffFilePath:      diffFilePath,
		requireDiffFile:   true,
		minSeverity:       minSeverity,
	}
	if git.IsRange(gitRef) {
		return b.buildRangePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, opts)
	}
	return b.buildSinglePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, opts)
}

// BuildWithDiffFile constructs a review prompt where a pre-written diff
// file is referenced for large diffs instead of git commands. This is
// used for Codex agents running in a sandboxed environment that cannot
// execute git directly.
func (b *Builder) BuildWithDiffFile(repoPath, gitRef string, repoID int64, contextCount int, agentName, reviewType, minSeverity, diffFilePath string) (string, error) {
	opts := buildOpts{
		diffFilePath:    diffFilePath,
		requireDiffFile: true,
		minSeverity:     minSeverity,
	}
	if git.IsRange(gitRef) {
		return b.buildRangePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, opts)
	}
	return b.buildSinglePrompt(repoPath, gitRef, repoID, contextCount, agentName, reviewType, opts)
}

// SnapshotResult holds a prompt and an optional cleanup function for
// a diff snapshot file that was written during prompt construction.
type SnapshotResult struct {
	Prompt  string
	Cleanup func()
}

// BuildWithSnapshot builds a review prompt, automatically writing a
// diff snapshot file when the diff is too large to inline.
func (b *Builder) BuildWithSnapshot(
	repoPath, gitRef string, repoID int64,
	contextCount int, agentName, reviewType, minSeverity string,
	excludes []string,
) (SnapshotResult, error) {
	p, err := b.BuildWithDiffFile(
		repoPath, gitRef, repoID,
		contextCount, agentName, reviewType, minSeverity, "",
	)
	if !errors.Is(err, ErrDiffTruncatedNoFile) {
		return SnapshotResult{Prompt: p}, err
	}
	diffFile, cleanup, writeErr := WriteDiffSnapshot(repoPath, gitRef, excludes)
	if writeErr != nil {
		return SnapshotResult{}, fmt.Errorf("write diff snapshot: %w", writeErr)
	}
	p, err = b.BuildWithDiffFile(
		repoPath, gitRef, repoID,
		contextCount, agentName, reviewType, minSeverity, diffFile,
	)
	if err != nil {
		cleanup()
		return SnapshotResult{}, err
	}
	return SnapshotResult{Prompt: p, Cleanup: cleanup}, nil
}

// WriteDiffSnapshot writes the full diff for a git ref to a file in
// the repo's git dir. Returns the file path and a cleanup function.
func WriteDiffSnapshot(repoPath, gitRef string, excludes []string) (string, func(), error) {
	var (
		fullDiff string
		err      error
	)
	if git.IsRange(gitRef) {
		fullDiff, err = git.GetRangeDiff(repoPath, gitRef, excludes...)
	} else {
		fullDiff, err = git.GetDiff(repoPath, gitRef, excludes...)
	}
	if err != nil {
		return "", nil, fmt.Errorf("capture diff: %w", err)
	}
	if fullDiff == "" {
		return "", nil, fmt.Errorf("diff is empty")
	}
	gitDir, err := git.ResolveGitDir(repoPath)
	if err != nil {
		return "", nil, fmt.Errorf("resolve git dir: %w", err)
	}
	f, err := os.CreateTemp(gitDir, "roborev-snapshot-*.diff")
	if err != nil {
		return "", nil, fmt.Errorf("create snapshot: %w", err)
	}
	diffFile := f.Name()
	_, writeErr := f.WriteString(fullDiff)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(diffFile)
		if writeErr != nil {
			return "", nil, fmt.Errorf("write snapshot: %w", writeErr)
		}
		return "", nil, fmt.Errorf("close snapshot: %w", closeErr)
	}
	return diffFile, func() { os.Remove(diffFile) }, nil
}

// BuildDirtyWithSnapshot builds a dirty review prompt, writing the diff
// to a snapshot file when it's too large to inline. The caller must
// call Cleanup (if non-nil) after the prompt is no longer needed.
func (b *Builder) BuildDirtyWithSnapshot(
	repoPath, diff string, repoID int64,
	contextCount int, agentName, reviewType, minSeverity string,
) (SnapshotResult, error) {
	p, err := b.BuildDirty(repoPath, diff, repoID, contextCount, agentName, reviewType, minSeverity)
	if err != nil {
		return SnapshotResult{}, err
	}
	if strings.Contains(p, "(Diff too large to include in full)") && len(diff) > 0 {
		gitDir, dirErr := git.ResolveGitDir(repoPath)
		if dirErr != nil {
			return SnapshotResult{}, fmt.Errorf("dirty diff snapshot: %w", dirErr)
		}
		f, createErr := os.CreateTemp(gitDir, "roborev-snapshot-*.diff")
		if createErr != nil {
			return SnapshotResult{}, fmt.Errorf("dirty diff snapshot: %w", createErr)
		}
		diffFile := f.Name()
		_, writeErr := f.WriteString(diff)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			os.Remove(diffFile)
			if writeErr != nil {
				return SnapshotResult{}, fmt.Errorf("dirty diff snapshot: %w", writeErr)
			}
			return SnapshotResult{}, fmt.Errorf("dirty diff snapshot: %w", closeErr)
		}
		p += fmt.Sprintf("\nThe full diff is also available at: `%s`\n", diffFile)
		return SnapshotResult{Prompt: p, Cleanup: func() { os.Remove(diffFile) }}, nil
	}
	return SnapshotResult{Prompt: p}, nil
}

// BuildDirty constructs a review prompt for uncommitted (dirty) changes.
// The diff is provided directly since it was captured at enqueue time.
// reviewType selects the system prompt variant (e.g., "security"); any default alias (see config.IsDefaultReviewType) uses the standard prompt.
func (b *Builder) BuildDirty(repoPath, diff string, repoID int64, contextCount int, agentName, reviewType, minSeverity string) (string, error) {
	// Start with system prompt for dirty changes
	promptType := "dirty"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == config.ReviewTypeDesign {
		promptType = "design-review"
	}
	promptCap := b.resolveMaxPromptSize(repoPath)
	requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"
	if inst := config.SeverityInstruction(minSeverity); inst != "" {
		requiredPrefix += inst + "\n"
	}
	requiredPrefix = hardCapPrompt(requiredPrefix, promptCap)

	optional := optionalSectionsView{}

	// Add project-specific guidelines if configured
	if repoCfg, err := config.LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		optional.ProjectGuidelines = buildProjectGuidelinesSectionView(repoCfg.ReviewGuidelines)
	}

	// Get previous reviews for context (use HEAD as reference point)
	if contextCount > 0 && b.db != nil {
		headSHA, err := git.ResolveSHA(repoPath, "HEAD")
		if err == nil {
			contexts, err := b.getPreviousReviewContexts(repoPath, headSHA, contextCount)
			if err == nil && len(contexts) > 0 {
				optional.PreviousReviews = orderedPreviousReviewViews(contexts)
			}
		}
	}

	bodyLimit := max(0, promptCap-len(requiredPrefix))
	inlineDiff, err := renderInlineDiff(diff)
	if err != nil {
		return "", err
	}
	view := dirtyPromptView{
		Optional: optional,
		Current: dirtyChangesSectionView{
			Description: "The following changes have not yet been committed.",
		},
		Diff: diffSectionView{
			Heading: "### Diff",
			Body:    inlineDiff,
		},
	}

	currentSection, err := renderDirtyChangesSection(view.Current)
	if err != nil {
		return "", err
	}
	fullDiffBlock, err := renderDiffBlock(view.Diff)
	if err != nil {
		return "", err
	}
	if len(currentSection)+len(fullDiffBlock) > bodyLimit {
		fallbackOnly, err := renderDirtyTruncatedDiffFallback("")
		if err != nil {
			return "", err
		}
		fallbackBlock, err := renderDiffBlock(diffSectionView{Heading: "### Diff", Fallback: fallbackOnly})
		if err != nil {
			return "", err
		}
		maxDiffLen := bodyLimit - len(currentSection) - len(fallbackBlock)
		view.Diff.Body = ""
		sizingView := view
		sizingBody, err := renderDirtyPrompt(sizingView)
		if err != nil {
			return "", err
		}
		for len(sizingBody) > bodyLimit && trimOptionalSections(&sizingView.Optional) {
			sizingBody, err = renderDirtyPrompt(sizingView)
			if err != nil {
				return "", err
			}
		}
		if maxDiffLen > 1000 {
			emptyFallbackOptional := sizingView.Optional
			sampleBody := "X\n"
			sampleFallback, err := renderDirtyTruncatedDiffFallback(sampleBody)
			if err != nil {
				return "", err
			}
			wrapperOverhead := len(sampleFallback) - len(fallbackOnly) - len(sampleBody)
			truncationSuffix := "\n... (truncated)\n"
			availableContentLen := maxDiffLen - wrapperOverhead - len(truncationSuffix)
			if availableContentLen > 0 {
				truncatedContent := truncateUTF8(diff, availableContentLen)
				for truncatedContent != "" {
					truncatedBody := truncatedContent
					if !strings.HasSuffix(truncatedBody, "\n") {
						truncatedBody += "\n"
					}
					truncatedBody += "... (truncated)\n"
					view.Diff.Fallback, err = renderDirtyTruncatedDiffFallback(truncatedBody)
					if err != nil {
						return "", err
					}
					sizingView.Diff = view.Diff
					rendered, err := renderDirtyPrompt(sizingView)
					if err != nil {
						return "", err
					}
					if len(rendered) <= bodyLimit {
						view.Optional = sizingView.Optional
						break
					}
					if trimOptionalSections(&sizingView.Optional) {
						continue
					}
					overflow := len(rendered) - bodyLimit
					next := truncateUTF8(truncatedContent, max(0, len(truncatedContent)-overflow))
					if next == truncatedContent {
						next = truncateUTF8(truncatedContent, max(0, len(truncatedContent)-1))
					}
					truncatedContent = next
				}
				if truncatedContent == "" {
					view.Diff.Fallback = fallbackOnly
					view.Optional = emptyFallbackOptional
				}
			} else {
				view.Diff.Fallback = fallbackOnly
			}
		} else {
			view.Diff.Fallback = fallbackOnly
		}
	}

	body, err := fitDirtyPrompt(bodyLimit, view)
	if err != nil {
		return "", err
	}
	return requiredPrefix + hardCapPrompt(body, bodyLimit), nil
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

// buildOpts groups optional parameters for buildSinglePrompt and
// buildRangePrompt to keep the positional parameter count manageable.
type buildOpts struct {
	additionalContext string
	// diffFilePath, when non-empty, is a file containing the full
	// diff that the prompt can reference for oversized diffs.
	diffFilePath string
	// requireDiffFile makes truncation an error when no file path
	// is available. Set by BuildWithDiffFile so the worker can
	// detect when a snapshot is needed.
	requireDiffFile bool
	// minSeverity, when non-empty, injects a severity filter
	// instruction into the system prompt.
	minSeverity string
}

// diffFileFallbackVariants returns progressively shorter prompt
// variants for oversized diffs. When filePath is non-empty, the
// variants reference the file; otherwise they just note truncation.
func diffFileFallbackVariants(heading, filePath string) []string {
	if filePath == "" {
		return []string{heading + "\n\n(Diff too large to include inline)\n"}
	}
	return []string{
		fmt.Sprintf("%s\n\n(Diff too large to include inline)\n\nThe full diff has been written to a file for review.\nRead the diff from: `%s`\n\nReview the actual diff before writing findings.\n", heading, filePath),
		fmt.Sprintf("%s\n\n(Diff too large to include inline)\nRead the diff from: `%s`\n", heading, filePath),
	}
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

func buildPromptPreservingCurrentSection(requiredPrefix, optionalContext, currentRequired, currentOverflow string, limit int, variants ...string) string {
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

// buildSinglePrompt constructs a prompt for a single commit
func (b *Builder) buildSinglePrompt(repoPath, sha string, repoID int64, contextCount int, agentName, reviewType string, opts buildOpts) (string, error) {
	// Start with system prompt
	promptType := "review"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == config.ReviewTypeDesign {
		promptType = "design-review"
	}
	promptCap := b.resolveMaxPromptSize(repoPath)
	requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"
	if inst := config.SeverityInstruction(opts.minSeverity); inst != "" {
		requiredPrefix += inst + "\n"
	}
	requiredPrefix = hardCapPrompt(requiredPrefix, promptCap)

	optional := optionalSectionsView{
		ProjectGuidelines: buildProjectGuidelinesSectionView(LoadGuidelines(repoPath)),
		AdditionalContext: buildAdditionalContextSection(opts.additionalContext),
	}

	// Get previous reviews if requested
	if contextCount > 0 && b.db != nil {
		contexts, err := b.getPreviousReviewContexts(repoPath, sha, contextCount)
		if err == nil && len(contexts) > 0 {
			optional.PreviousReviews = orderedPreviousReviewViews(contexts)
		}
	}

	// Include previous review attempts for this same commit (for re-reviews)
	optional.PreviousAttempts = previousAttemptViewsFromContexts(b.previousAttemptContexts(sha))

	// Current commit section
	shortSHA := git.ShortSHA(sha)

	// Get commit info
	info, err := git.GetCommitInfo(repoPath, sha)
	if err != nil {
		return "", fmt.Errorf("get commit info: %w", err)
	}

	currentView := currentCommitSectionView{
		Commit:  shortSHA,
		Subject: info.Subject,
		Author:  info.Author,
		Message: info.Body,
	}
	currentRequired, err := renderCurrentCommitRequired(currentView)
	if err != nil {
		return "", err
	}
	currentOverflow, err := renderCurrentCommitOverflow(currentView)
	if err != nil {
		return "", err
	}
	emptyInlineDiff, err := renderInlineDiff("")
	if err != nil {
		return "", err
	}
	emptyDiffBlock, err := renderDiffBlock(diffSectionView{Heading: "### Diff", Body: emptyInlineDiff})
	if err != nil {
		return "", err
	}

	excludes := b.resolveExcludes(repoPath, reviewType)
	bodyLimit := max(0, promptCap-len(requiredPrefix))
	diffLimit := max(0, bodyLimit-len(currentRequired)-len(currentOverflow)-len(emptyDiffBlock))
	diff, truncated, err := git.GetDiffLimited(repoPath, sha, diffLimit, excludes...)
	if err != nil {
		return "", fmt.Errorf("get diff: %w", err)
	}

	diffView := diffSectionView{Heading: "### Diff"}
	if truncated {
		if opts.diffFilePath == "" && opts.requireDiffFile {
			return "", ErrDiffTruncatedNoFile
		}
		optionalPrefix, err := renderOptionalSectionsPrefix(optional)
		if err != nil {
			return "", err
		}
		return buildPromptPreservingCurrentSection(
			requiredPrefix,
			optionalPrefix,
			currentRequired,
			currentOverflow,
			promptCap,
			diffFileFallbackVariants("### Diff", opts.diffFilePath)...,
		), nil
	} else {
		inlineDiff, err := renderInlineDiff(diff)
		if err != nil {
			return "", err
		}
		diffView.Body = inlineDiff
	}

	body, err := fitSinglePrompt(
		bodyLimit,
		singlePromptView{
			Optional: optional,
			Current:  currentView,
			Diff:     diffView,
		},
	)
	if err != nil {
		return "", err
	}
	return requiredPrefix + body, nil
}

// buildRangePrompt constructs a prompt for a commit range
func (b *Builder) buildRangePrompt(repoPath, rangeRef string, repoID int64, contextCount int, agentName, reviewType string, opts buildOpts) (string, error) {
	// Start with system prompt for ranges
	promptType := "range"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == config.ReviewTypeDesign {
		promptType = "design-review"
	}
	promptCap := b.resolveMaxPromptSize(repoPath)
	requiredPrefix := GetSystemPrompt(agentName, promptType) + "\n"
	if inst := config.SeverityInstruction(opts.minSeverity); inst != "" {
		requiredPrefix += inst + "\n"
	}
	requiredPrefix = hardCapPrompt(requiredPrefix, promptCap)

	optional := optionalSectionsView{
		ProjectGuidelines: buildProjectGuidelinesSectionView(LoadGuidelines(repoPath)),
		AdditionalContext: buildAdditionalContextSection(opts.additionalContext),
	}

	// Get previous reviews from before the range start
	if contextCount > 0 && b.db != nil {
		startSHA, err := git.GetRangeStart(repoPath, rangeRef)
		if err == nil {
			contexts, err := b.getPreviousReviewContexts(repoPath, startSHA, contextCount)
			if err == nil && len(contexts) > 0 {
				optional.PreviousReviews = orderedPreviousReviewViews(contexts)
			}
		}
	}

	// Include previous review attempts for this same range (for re-reviews)
	optional.PreviousAttempts = previousAttemptViewsFromContexts(b.previousAttemptContexts(rangeRef))

	// Get commits in range
	commits, err := git.GetRangeCommits(repoPath, rangeRef)
	if err != nil {
		return "", fmt.Errorf("get range commits: %w", err)
	}

	optional.InRangeReviews = inRangeReviewViews(b.lookupReviewContexts(commits, true))

	entries := make([]commitRangeEntryView, 0, len(commits))
	for _, commitSHA := range commits {
		short := git.ShortSHA(commitSHA)
		info, err := git.GetCommitInfo(repoPath, commitSHA)
		if err == nil {
			entries = append(entries, commitRangeEntryView{Commit: short, Subject: info.Subject})
			continue
		}
		entries = append(entries, commitRangeEntryView{Commit: short})
	}
	currentView := commitRangeSectionView{Count: len(commits), Entries: entries}
	currentRequiredText, err := renderCommitRangeRequired(currentView)
	if err != nil {
		return "", err
	}
	currentOverflowText, err := renderCommitRangeOverflow(currentView)
	if err != nil {
		return "", err
	}
	emptyInlineDiff, err := renderInlineDiff("")
	if err != nil {
		return "", err
	}
	emptyDiffBlock, err := renderDiffBlock(diffSectionView{Heading: "### Combined Diff", Body: emptyInlineDiff})
	if err != nil {
		return "", err
	}

	excludes := b.resolveExcludes(repoPath, reviewType)
	bodyLimit := max(0, promptCap-len(requiredPrefix))
	diffLimit := max(0, bodyLimit-len(currentRequiredText)-len(currentOverflowText)-len(emptyDiffBlock))
	diff, truncated, err := git.GetRangeDiffLimited(repoPath, rangeRef, diffLimit, excludes...)
	if err != nil {
		return "", fmt.Errorf("get range diff: %w", err)
	}

	diffView := diffSectionView{Heading: "### Combined Diff"}
	if truncated {
		if opts.diffFilePath == "" && opts.requireDiffFile {
			return "", ErrDiffTruncatedNoFile
		}
		optionalPrefix, err := renderOptionalSectionsPrefix(optional)
		if err != nil {
			return "", err
		}
		return buildPromptPreservingCurrentSection(
			requiredPrefix,
			optionalPrefix,
			currentRequiredText,
			currentOverflowText,
			promptCap,
			diffFileFallbackVariants("### Combined Diff", opts.diffFilePath)...,
		), nil
	} else {
		inlineDiff, err := renderInlineDiff(diff)
		if err != nil {
			return "", err
		}
		diffView.Body = inlineDiff
	}

	body, err := fitRangePrompt(
		bodyLimit,
		rangePromptView{
			Optional: optional,
			Current:  currentView,
			Diff:     diffView,
		},
	)
	if err != nil {
		return "", err
	}
	return requiredPrefix + body, nil
}

func buildProjectGuidelinesSectionView(guidelines string) *markdownSectionView {
	trimmed := strings.TrimSpace(guidelines)
	if trimmed == "" {
		return nil
	}
	return &markdownSectionView{
		Heading: "## Project Guidelines",
		Body:    trimmed,
	}
}

func buildAdditionalContextSection(additionalContext string) string {
	trimmed := strings.TrimSpace(additionalContext)
	if trimmed == "" {
		return ""
	}
	return trimmed + "\n\n"
}

func orderedPreviousReviewViews(contexts []ReviewContext) []previousReviewView {
	ordered := make([]ReviewContext, 0, len(contexts))
	for i := len(contexts) - 1; i >= 0; i-- {
		ordered = append(ordered, contexts[i])
	}
	return previousReviewViews(ordered)
}

// LoadGuidelines loads review guidelines from the repo's default
// branch, falling back to filesystem config when the default branch
// has no .roborev.toml.
func (b *Builder) writeProjectGuidelines(sb *strings.Builder, guidelines string) {
	body, err := renderOptionalSectionsPrefix(optionalSectionsView{ProjectGuidelines: buildProjectGuidelinesSectionView(guidelines)})
	if err == nil {
		sb.WriteString(body)
	}
}

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

func (b *Builder) previousAttemptContexts(gitRef string) []reviewAttemptContext {
	if b.db == nil {
		return nil
	}

	reviews, err := b.db.GetAllReviewsForGitRef(gitRef)
	if err != nil || len(reviews) == 0 {
		return nil
	}

	attempts := make([]reviewAttemptContext, 0, len(reviews))
	for _, review := range reviews {
		attempt := reviewAttemptContext{Review: review}
		if review.JobID > 0 {
			responses, err := b.db.GetCommentsForJob(review.JobID)
			if err == nil {
				attempt.Responses = responses
			}
		}
		attempts = append(attempts, attempt)
	}
	return attempts
}

func (b *Builder) lookupReviewContexts(shas []string, skipMissing bool) []ReviewContext {
	if b.db == nil {
		return nil
	}
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

// BuildSimple constructs a simpler prompt without database context
func BuildSimple(repoPath, sha, agentName string) (string, error) {
	b := &Builder{}
	return b.Build(repoPath, sha, 0, 0, agentName, "", "")
}

// PreviousAttemptsHeader introduces previous addressing attempts section.
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

// FormatToolAttempts renders automated tool responses into a prompt section.
func FormatToolAttempts(attempts []storage.Response) string {
	if len(attempts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(PreviousAttemptsHeader)
	sb.WriteString("\n")
	for _, attempt := range attempts {
		fmt.Fprintf(&sb, "--- Attempt by %s at %s ---\n", attempt.Responder, attempt.CreatedAt.Format("2006-01-02 15:04"))
		sb.WriteString(attempt.Response)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// FormatUserComments renders user-authored comments into a prompt section.
func FormatUserComments(comments []storage.Response) string {
	if len(comments) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(UserCommentsHeader)
	for _, c := range comments {
		fmt.Fprintf(&sb, "**%s** (%s):\n%s\n\n", c.Responder, c.CreatedAt.Format("2006-01-02 15:04"), c.Response)
	}
	return sb.String()
}

// BuildAddressPrompt constructs a prompt for addressing review findings.
// When minSeverity is non-empty, a severity filtering instruction is
// injected before the findings section.
func (b *Builder) BuildAddressPrompt(repoPath string, review *storage.Review, previousAttempts []storage.Response, minSeverity string) (string, error) {
	view := addressPromptView{
		SeverityFilter: config.SeverityInstruction(minSeverity),
		ReviewFindings: review.Output,
		JobID:          review.JobID,
	}

	if repoCfg, err := config.LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		view.ProjectGuidelines = buildProjectGuidelinesSectionView(repoCfg.ReviewGuidelines)
	}

	if len(previousAttempts) > 0 {
		toolAttempts, userComments := SplitResponses(previousAttempts)
		if len(toolAttempts) > 0 {
			view.ToolAttempts = make([]addressAttemptView, 0, len(toolAttempts))
			for _, attempt := range toolAttempts {
				when := ""
				if !attempt.CreatedAt.IsZero() {
					when = attempt.CreatedAt.Format("2006-01-02 15:04")
				}
				view.ToolAttempts = append(view.ToolAttempts, addressAttemptView{Responder: attempt.Responder, Response: attempt.Response, When: when})
			}
		}
		if len(userComments) > 0 {
			view.UserComments = make([]addressAttemptView, 0, len(userComments))
			for _, comment := range userComments {
				when := ""
				if !comment.CreatedAt.IsZero() {
					when = comment.CreatedAt.Format("2006-01-02 15:04")
				}
				view.UserComments = append(view.UserComments, addressAttemptView{Responder: comment.Responder, Response: comment.Response, When: when})
			}
		}
	}

	if review.Job != nil && review.Job.GitRef != "" && review.Job.GitRef != "dirty" {
		diff, err := git.GetDiff(repoPath, review.Job.GitRef)
		if err == nil && len(diff) > 0 && len(diff) < MaxPromptSize/2 {
			view.OriginalDiff = diff
			if !strings.HasSuffix(view.OriginalDiff, "\n") {
				view.OriginalDiff += "\n"
			}
		}
	}

	body, err := renderAddressPromptFromSections(view)
	if err != nil {
		return "", err
	}
	return GetSystemPrompt(review.Agent, "address") + "\n" + body, nil
}
