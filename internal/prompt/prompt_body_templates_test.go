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
