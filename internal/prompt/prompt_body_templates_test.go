package prompt

import (
	"strings"
	"testing"

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

	t.Run("single", func(t *testing.T) {
		body, err := renderSinglePromptBody(singlePromptBodyView{
			OptionalContext: rawOptional,
			CurrentRequired: rawRequired,
			CurrentOverflow: rawOverflow,
			DiffSection:     rawDiff,
		})
		require.NoError(t, err)
		assert.Equal(t, rawOptional+rawRequired+rawOverflow+rawDiff, body)
	})

	t.Run("range", func(t *testing.T) {
		body, err := renderRangePromptBody(rangePromptBodyView{
			OptionalContext: rawOptional,
			CurrentRequired: rawRequired,
			CurrentOverflow: rawOverflow,
			DiffSection:     rawDiff,
		})
		require.NoError(t, err)
		assert.Equal(t, rawOptional+rawRequired+rawOverflow+rawDiff, body)
	})

	t.Run("dirty", func(t *testing.T) {
		body, err := renderDirtyPromptBody(dirtyPromptBodyView{
			OptionalContext: rawOptional,
			CurrentRequired: rawRequired,
			DiffSection:     rawDiff,
		})
		require.NoError(t, err)
		assert.Equal(t, rawOptional+rawRequired+rawDiff, body)
	})
}

func TestFitSinglePromptBodyTrimsOptionalContextBeforeCurrentOverflow(t *testing.T) {
	view := singlePromptBodyView{
		OptionalContext: strings.Repeat("g", 128),
		CurrentRequired: "## Current Commit\n\n**Commit:** abc1234\n\n",
		CurrentOverflow: "**Subject:** large change\n**Author:** Test User\n\n",
		DiffSection:     "### Diff\n\n(Diff too large; for Codex run `git show abc1234 --` locally.)\n",
	}
	limit := len(view.CurrentRequired) + len(view.CurrentOverflow) + len(view.DiffSection)

	body, err := fitSinglePromptBody(limit, view)
	require.NoError(t, err)
	assert.Contains(t, body, "## Current Commit")
	assert.Contains(t, body, "**Subject:** large change")
	assert.NotContains(t, body, strings.Repeat("g", 128))
}
