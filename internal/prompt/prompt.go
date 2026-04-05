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
	promptCap := b.resolveMaxPromptSize(repoPath)
	requiredPrefix := hardCapPrompt(GetSystemPrompt(agentName, promptType)+"\n", promptCap)

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

func isCodexReviewAgent(agentName string) bool {
	return strings.EqualFold(strings.TrimSpace(agentName), "codex")
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

func codexCommitInspectionFallbackVariants(sha string, pathspecArgs []string) []diffSectionView {
	view := commitInspectionFallbackView{
		SHA:         sha,
		StatCmd:     renderShellCommand(append([]string{"git", "show", "--stat", "--summary", sha, "--"}, pathspecArgs...)...),
		DiffCmd:     renderShellCommand(append([]string{"git", "show", "--format=medium", "--unified=80", sha, "--"}, pathspecArgs...)...),
		FilesCmd:    renderShellCommand(append([]string{"git", "diff-tree", "--no-commit-id", "--name-only", "-r", sha, "--"}, pathspecArgs...)...),
		ShowPathCmd: renderShellCommand(append([]string{"git", "show", sha, "--"}, pathspecArgs...)...),
	}
	names := []string{"codex_commit_fallback_full", "codex_commit_fallback_medium", "codex_commit_fallback_short", "codex_commit_fallback_shortest"}
	variants := make([]diffSectionView, 0, len(names))
	for _, name := range names {
		fallback, err := renderCommitInspectionFallback(name, view)
		if err != nil {
			continue
		}
		variants = append(variants, diffSectionView{Heading: "### Diff", Fallback: fallback})
	}
	return variants
}

func codexRangeInspectionFallbackVariants(rangeRef string, pathspecArgs []string) []diffSectionView {
	view := rangeInspectionFallbackView{
		RangeRef: rangeRef,
		LogCmd:   renderShellCommand("git", "log", "--oneline", rangeRef),
		StatCmd:  renderShellCommand(append([]string{"git", "diff", "--stat", rangeRef, "--"}, pathspecArgs...)...),
		DiffCmd:  renderShellCommand(append([]string{"git", "diff", "--unified=80", rangeRef, "--"}, pathspecArgs...)...),
		FilesCmd: renderShellCommand(append([]string{"git", "diff", "--name-only", rangeRef, "--"}, pathspecArgs...)...),
		ViewCmd:  renderShellCommand(append([]string{"git", "diff", rangeRef, "--"}, pathspecArgs...)...),
	}
	names := []string{"codex_range_fallback_full", "codex_range_fallback_medium", "codex_range_fallback_short", "codex_range_fallback_shortest"}
	variants := make([]diffSectionView, 0, len(names))
	for _, name := range names {
		fallback, err := renderRangeInspectionFallback(name, view)
		if err != nil {
			continue
		}
		variants = append(variants, diffSectionView{Heading: "### Combined Diff", Fallback: fallback})
	}
	return variants
}

func selectDiffSectionVariant(variants []diffSectionView, remaining int) (diffSectionView, error) {
	if len(variants) == 0 {
		return diffSectionView{}, nil
	}
	selected := variants[len(variants)-1]
	for _, variant := range variants {
		block, err := renderDiffBlock(variant)
		if err != nil {
			return diffSectionView{}, err
		}
		if len(block) <= remaining {
			return variant, nil
		}
	}
	return truncateDiffSectionFallbackToFit(selected, remaining)
}

func truncateDiffSectionFallbackToFit(view diffSectionView, limit int) (diffSectionView, error) {
	block, err := renderDiffBlock(view)
	if err != nil || len(block) <= limit {
		return view, err
	}
	baseBlock, err := renderDiffBlock(diffSectionView{Heading: view.Heading, Body: ""})
	if err != nil {
		return diffSectionView{}, err
	}
	view.Fallback = truncateUTF8(view.Fallback, max(0, limit-len(baseBlock)))
	return view, nil
}

type rangeMetadataLoss struct {
	RemovedEntries int
	BlankedSubject int
}

func compareRangeMetadataLoss(a, b rangeMetadataLoss) int {
	switch {
	case a.RemovedEntries != b.RemovedEntries:
		return a.RemovedEntries - b.RemovedEntries
	default:
		return a.BlankedSubject - b.BlankedSubject
	}
}

func measureRangeMetadataLoss(original, trimmed commitRangeSectionView) rangeMetadataLoss {
	loss := rangeMetadataLoss{RemovedEntries: len(original.Entries) - len(trimmed.Entries)}
	for i := range trimmed.Entries {
		if i >= len(original.Entries) {
			break
		}
		if original.Entries[i].Subject != "" && trimmed.Entries[i].Subject == "" {
			loss.BlankedSubject++
		}
	}
	return loss
}

func selectRichestRangePromptView(limit int, view rangePromptView, variants []diffSectionView) (rangePromptView, error) {
	fallback := rangePromptView{Optional: view.Optional, Current: view.Current}
	if len(variants) > 0 {
		fallback.Diff = variants[len(variants)-1]
	}
	var (
		best     rangePromptView
		bestLoss rangeMetadataLoss
		haveBest bool
	)
	for _, variant := range variants {
		candidate := rangePromptView{Optional: view.Optional, Current: view.Current, Diff: variant}
		trimmed, body, err := trimRangePromptView(limit, candidate)
		if err != nil {
			return rangePromptView{}, err
		}
		fallback = trimmed
		if len(body) > limit {
			continue
		}
		loss := measureRangeMetadataLoss(view.Current, trimmed.Current)
		if !haveBest || compareRangeMetadataLoss(loss, bestLoss) < 0 {
			best = trimmed
			bestLoss = loss
			haveBest = true
		}
	}
	if haveBest {
		return best, nil
	}
	return fallback, nil
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
	promptCap := b.resolveMaxPromptSize(repoPath)
	requiredPrefix := hardCapPrompt(GetSystemPrompt(agentName, promptType)+"\n", promptCap)

	optional := optionalSectionsView{
		ProjectGuidelines: buildProjectGuidelinesSectionView(LoadGuidelines(repoPath)),
		AdditionalContext: buildAdditionalContextSection(additionalContext),
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
		pathspecArgs := safeForMarkdown(git.FormatExcludeArgs(excludes))
		if isCodexReviewAgent(agentName) {
			variants := codexCommitInspectionFallbackVariants(sha, pathspecArgs)
			shortestBlock, err := renderDiffBlock(variants[len(variants)-1])
			if err != nil {
				return "", err
			}
			optionalPrefix, err := renderOptionalSectionsPrefix(optional)
			if err != nil {
				return "", err
			}
			softBudget := max(0, bodyLimit-len(currentRequired)-len(shortestBlock))
			softLen := len(optionalPrefix) + len(currentOverflow)
			effectiveSoftLen := min(softLen, softBudget)
			remaining := max(0, bodyLimit-len(currentRequired)-effectiveSoftLen)
			diffView, err = selectDiffSectionVariant(variants, remaining)
			if err != nil {
				return "", err
			}
		} else {
			fallback, err := renderGenericCommitFallback(renderShellCommand("git", "show", sha))
			if err != nil {
				return "", err
			}
			diffView.Fallback = fallback
		}
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
func (b *Builder) buildRangePrompt(repoPath, rangeRef string, repoID int64, contextCount int, agentName, reviewType, additionalContext string) (string, error) {
	// Start with system prompt for ranges
	promptType := "range"
	if !config.IsDefaultReviewType(reviewType) {
		promptType = reviewType
	}
	if promptType == config.ReviewTypeDesign {
		promptType = "design-review"
	}
	promptCap := b.resolveMaxPromptSize(repoPath)
	requiredPrefix := hardCapPrompt(GetSystemPrompt(agentName, promptType)+"\n", promptCap)

	optional := optionalSectionsView{
		ProjectGuidelines: buildProjectGuidelinesSectionView(LoadGuidelines(repoPath)),
		AdditionalContext: buildAdditionalContextSection(additionalContext),
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
		pathspecArgs := safeForMarkdown(git.FormatExcludeArgs(excludes))
		if isCodexReviewAgent(agentName) {
			variants := codexRangeInspectionFallbackVariants(rangeRef, pathspecArgs)
			selectedView, err := selectRichestRangePromptView(bodyLimit, rangePromptView{
				Optional: optional,
				Current:  currentView,
			}, variants)
			if err != nil {
				return "", err
			}
			optional = selectedView.Optional
			currentView = selectedView.Current
			diffView = selectedView.Diff
		} else {
			fallback, err := renderGenericRangeFallback(renderShellCommand("git", "diff", rangeRef))
			if err != nil {
				return "", err
			}
			diffView.Fallback = fallback
		}
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

// BuildSimple constructs a simpler prompt without database context
func BuildSimple(repoPath, sha, agentName string) (string, error) {
	b := &Builder{}
	return b.Build(repoPath, sha, 0, 0, agentName, "")
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
		view.PreviousAttempts = make([]addressAttemptView, 0, len(previousAttempts))
		for _, attempt := range previousAttempts {
			when := ""
			if !attempt.CreatedAt.IsZero() {
				when = attempt.CreatedAt.Format("2006-01-02 15:04")
			}
			view.PreviousAttempts = append(view.PreviousAttempts, addressAttemptView{
				Responder: attempt.Responder,
				Response:  attempt.Response,
				When:      when,
			})
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
