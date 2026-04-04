package prompt

import (
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderSinglePromptBody(t *testing.T) {
	body, err := renderSinglePromptBody(singlePromptBodyView{
		OptionalContext: "optional\n",
		CurrentRequired: "required\n",
		CurrentOverflow: "overflow\n",
		DiffSection:     "diff\n",
	})
	require.NoError(t, err)
	assert.Equal(t, "optional\nrequired\noverflow\ndiff\n", body)
}

func TestRenderRangePromptBody(t *testing.T) {
	body, err := renderRangePromptBody(rangePromptBodyView{
		OptionalContext: "optional\n",
		CurrentRequired: "required\n",
		CurrentOverflow: "overflow\n",
		DiffSection:     "diff\n",
	})
	require.NoError(t, err)
	assert.Equal(t, "optional\nrequired\noverflow\ndiff\n", body)
}

func TestRenderDirtyPromptBody(t *testing.T) {
	body, err := renderDirtyPromptBody(dirtyPromptBodyView{
		OptionalContext: "optional\n",
		CurrentRequired: "required\n",
		DiffSection:     "diff\n",
	})
	require.NoError(t, err)
	assert.Equal(t, "optional\nrequired\ndiff\n", body)
}

func TestRenderPromptBodiesPreserveRawText(t *testing.T) {
	const rawOptional = "context with <tag> & symbols > keep\n"
	const rawRequired = "required <input> && output > all\n"
	const rawOverflow = "overflow uses <, >, and &\n"
	const rawDiff = "diff --git a/file b/file\n+ use <raw> & keep > unchanged\n"

	body, err := renderSinglePromptBody(singlePromptBodyView{
		OptionalContext: rawOptional,
		CurrentRequired: rawRequired,
		CurrentOverflow: rawOverflow,
		DiffSection:     rawDiff,
	})
	require.NoError(t, err)
	assert.Equal(t, rawOptional+rawRequired+rawOverflow+rawDiff, body)
}

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

func TestRenderSinglePromptFromSections(t *testing.T) {
	body, err := renderSinglePromptFromSections(
		"## Pull Request Discussion\n\nNewest comment first.\n\n",
		currentCommitSectionView{
			Commit:  "abc1234",
			Subject: "template prompt rendering",
			Author:  "Test User",
			Message: "body text",
		},
		diffSectionView{
			Heading: "### Diff",
			Body:    "```diff\n+line\n```\n",
		},
	)
	require.NoError(t, err)
	assert.Contains(t, body, "## Pull Request Discussion")
	assert.Contains(t, body, "## Current Commit")
	assert.Contains(t, body, "**Subject:** template prompt rendering")
	assert.Contains(t, body, "**Message:**")
	assert.Contains(t, body, "### Diff")
}

func TestRenderRangePromptUsesNestedSections(t *testing.T) {
	view := rangePromptView{
		Optional: optionalSectionsView{
			AdditionalContext: "## Pull Request Discussion\n\nNewest comment first.\n\n",
		},
		Current: commitRangeSectionView{
			Entries: []commitRangeEntryView{{Commit: "abc1234", Subject: "first"}, {Commit: "def5678", Subject: "second"}},
		},
		Diff: diffSectionView{
			Heading: "### Combined Diff",
			Body:    "```diff\n+line\n```\n",
		},
	}

	body, err := renderRangePrompt(view)
	require.NoError(t, err)

	assert.Contains(t, body, "## Pull Request Discussion")
	assert.Contains(t, body, "## Commit Range")
	assert.Contains(t, body, "- abc1234 first")
	assert.Contains(t, body, "- def5678 second")
	assert.Contains(t, body, "### Combined Diff")
}

func TestRenderRangePromptFromSections(t *testing.T) {
	body, err := renderRangePromptFromSections(
		"## Pull Request Discussion\n\nNewest comment first.\n\n",
		commitRangeSectionView{
			Entries: []commitRangeEntryView{{Commit: "abc1234", Subject: "first"}, {Commit: "def5678", Subject: "second"}},
		},
		diffSectionView{
			Heading: "### Combined Diff",
			Body:    "```diff\n+line\n```\n",
		},
	)
	require.NoError(t, err)
	assert.Contains(t, body, "## Pull Request Discussion")
	assert.Contains(t, body, "## Commit Range")
	assert.Contains(t, body, "- abc1234 first")
	assert.Contains(t, body, "- def5678 second")
	assert.Contains(t, body, "### Combined Diff")
}
func TestRenderDirtyPromptUsesNestedSections(t *testing.T) {
	view := dirtyPromptView{
		Optional: optionalSectionsView{
			AdditionalContext: "## Pull Request Discussion\n\nNewest comment first.\n\n",
		},
		Current: dirtyChangesSectionView{
			Description: "The following changes have not yet been committed.",
		},
		Diff: diffSectionView{
			Heading: "### Diff",
			Body:    "```diff\n+line\n```\n",
		},
	}

	body, err := renderDirtyPrompt(view)
	require.NoError(t, err)

	assert.Contains(t, body, "## Pull Request Discussion")
	assert.Contains(t, body, "## Uncommitted Changes")
	assert.Contains(t, body, "The following changes have not yet been committed.")
	assert.Contains(t, body, "### Diff")
}

func TestRenderAddressPromptUsesNestedSections(t *testing.T) {
	view := addressPromptView{
		ProjectGuidelines: &markdownSectionView{
			Heading: "## Project Guidelines",
			Body:    "Keep it simple.",
		},
		PreviousAttempts: []addressAttemptView{{Responder: "developer", Response: "Tried a narrow fix", When: "2026-04-04 12:00"}},
		SeverityFilter:   "Only address medium and higher findings.\n\n",
		ReviewFindings:   "- medium: do the thing",
		OriginalDiff:     "diff --git a/a b/a\n+line\n",
		JobID:            42,
	}

	body, err := renderAddressPrompt(view)
	require.NoError(t, err)

	assert.Contains(t, body, "## Project Guidelines")
	assert.Contains(t, body, "## Previous Addressing Attempts")
	assert.Contains(t, body, "## Review Findings to Address (Job 42)")
	assert.Contains(t, body, "## Original Commit Diff (for context)")
}

func TestRenderSystemPromptUsesTemplateData(t *testing.T) {
	body, err := renderSystemPrompt("default_review.tmpl", systemPromptView{
		NoSkillsInstruction: noSkillsInstruction,
		CurrentDate:         "2030-06-15",
	})
	require.NoError(t, err)

	assert.Contains(t, body, "You are a code reviewer")
	assert.Contains(t, body, "Do NOT use any external skills")
	assert.Contains(t, body, "Current date: 2030-06-15 (UTC)")
}

func TestRenderSinglePromptPreservesRawText(t *testing.T) {
	view := singlePromptView{
		Optional: optionalSectionsView{
			ProjectGuidelines: &markdownSectionView{
				Heading: "## Project Guidelines",
				Body:    "context with <tag> & symbols > keep",
			},
		},
		Current: currentCommitSectionView{
			Commit:  "abc1234",
			Subject: "required <input> && output > all",
			Author:  "overflow uses <, >, and &",
		},
		Diff: diffSectionView{
			Heading: "### Diff",
			Body:    "```diff\n+ use <raw> & keep > unchanged\n```\n",
		},
	}

	body, err := renderSinglePrompt(view)
	require.NoError(t, err)
	assert.Contains(t, body, "context with <tag> & symbols > keep")
	assert.Contains(t, body, "required <input> && output > all")
	assert.Contains(t, body, "overflow uses <, >, and &")
	assert.Contains(t, body, "+ use <raw> & keep > unchanged")
}

func TestRenderSinglePromptUsesFallbackOverBody(t *testing.T) {
	body, err := renderSinglePrompt(singlePromptView{
		Current: currentCommitSectionView{Commit: "abc1234", Subject: "subject", Author: "author"},
		Diff:    diffSectionView{Heading: "### Diff", Body: "body", Fallback: "fallback"},
	})
	require.NoError(t, err)
	assert.Contains(t, body, "fallback")
	assert.NotContains(t, body, "body")
}

func TestRenderOptionalSectionsFromTypedData(t *testing.T) {
	body, err := renderOptionalSectionsFromView(optionalSectionsView{
		ProjectGuidelines: &markdownSectionView{
			Heading: "## Project Guidelines",
			Body:    "Prefer composition over inheritance.",
		},
		AdditionalContext: "## Pull Request Discussion\n\nNewest comment first.\n\n",
		PreviousReviews: []previousReviewView{{
			Commit:    "abc1234",
			Available: true,
			Output:    "Found a bug",
			Comments:  []reviewCommentView{{Responder: "alice", Response: "Known issue"}},
		}},
		PreviousAttempts: []reviewAttemptView{{
			Label:    "Review Attempt 1",
			Agent:    "test",
			When:     "2026-04-05 10:00",
			Output:   "Still failing",
			Comments: []reviewCommentView{{Responder: "bob", Response: "Will fix"}},
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, body, "## Project Guidelines")
	assert.Contains(t, body, "## Pull Request Discussion")
	assert.Contains(t, body, "## Previous Reviews")
	assert.Contains(t, body, "Found a bug")
	assert.Contains(t, body, "Known issue")
	assert.Contains(t, body, "## Previous Review Attempts")
	assert.Contains(t, body, "Review Attempt 1")
	assert.Contains(t, body, "Will fix")
}

func TestRenderOptionalSectionsOmitsEmptySections(t *testing.T) {
	body, err := renderOptionalSectionsFromView(optionalSectionsView{})
	require.NoError(t, err)
	assert.Empty(t, body)
}

func TestRenderAdditionalContextBlockTrimsAndFormats(t *testing.T) {
	body, err := renderAdditionalContextBlock("\n## Pull Request Discussion\n\nNewest comment first.\n")
	require.NoError(t, err)
	assert.Equal(t, "## Pull Request Discussion\n\nNewest comment first.\n\n", body)
}

func TestRenderProjectGuidelinesBlockTrimsAndFormats(t *testing.T) {
	body, err := renderProjectGuidelinesBlock("\nPrefer composition over inheritance.\n")
	require.NoError(t, err)
	assert.Equal(t, "## Project Guidelines\n\nPrefer composition over inheritance.\n\n", body)
}

func TestPreviousReviewViewsPreserveChronologicalOrder(t *testing.T) {
	views := previousReviewViews([]ReviewContext{
		{SHA: "bbbbbbb", Review: &storage.Review{Output: "second"}},
		{SHA: "aaaaaaa", Review: &storage.Review{Output: "first"}},
	})
	require.Len(t, views, 2)
	assert.Equal(t, "bbbbbbb", views[0].Commit)
	assert.Equal(t, "aaaaaaa", views[1].Commit)
}

func TestRenderPreviousReviewsFromContexts(t *testing.T) {
	body, err := renderPreviousReviewsFromContexts([]ReviewContext{
		{
			SHA:    "abc1234",
			Review: &storage.Review{Output: "Found a bug"},
			Responses: []storage.Response{{
				Responder: "alice",
				Response:  "Known issue",
			}},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, body, "## Previous Reviews")
	assert.Contains(t, body, "Found a bug")
	assert.Contains(t, body, "Known issue")
}

func TestReviewAttemptViewsPreserveOrderAndMetadata(t *testing.T) {
	views := reviewAttemptViews([]storage.Review{{
		Agent:     "test",
		Output:    "first",
		CreatedAt: mustParsePromptTestTime(t, "2026-04-05 10:00"),
	}})
	require.Len(t, views, 1)
	assert.Equal(t, "Review Attempt 1", views[0].Label)
	assert.Equal(t, "test", views[0].Agent)
	assert.Equal(t, "2026-04-05 10:00", views[0].When)
	assert.Equal(t, "first", views[0].Output)
}

func TestRenderPreviousAttemptsFromReviews(t *testing.T) {
	body, err := renderPreviousAttemptsFromReviews([]storage.Review{{
		Agent:     "test",
		Output:    "first",
		CreatedAt: mustParsePromptTestTime(t, "2026-04-05 10:00"),
	}})
	require.NoError(t, err)
	assert.Contains(t, body, "## Previous Review Attempts")
	assert.Contains(t, body, "Review Attempt 1")
	assert.Contains(t, body, "first")
}

func mustParsePromptTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04", value)
	require.NoError(t, err)
	return parsed
}
func TestRenderAddressPromptOmitsOptionalSectionsWhenEmpty(t *testing.T) {
	body, err := renderAddressPrompt(addressPromptView{ReviewFindings: "finding", JobID: 1})
	require.NoError(t, err)
	assert.NotContains(t, body, "## Previous Addressing Attempts")
	assert.NotContains(t, body, "## Original Commit Diff")
}

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
