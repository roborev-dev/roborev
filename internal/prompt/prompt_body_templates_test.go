package prompt

import (
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
