package autotype

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultHeuristics(t *testing.T) {
	h := DefaultHeuristics()
	assert := assert.New(t)
	assert.Equal(10, h.MinDiffLines)
	assert.Equal(500, h.LargeDiffLines)
	assert.Equal(10, h.LargeFileCount)
	assert.NotEmpty(h.TriggerPaths)
	assert.NotEmpty(h.SkipPaths)
	assert.NotEmpty(h.TriggerMessagePatterns)
	assert.NotEmpty(h.SkipMessagePatterns)
	assert.Contains(h.TriggerPaths, "**/migrations/**")
	assert.Contains(h.SkipPaths, "**/*.md")
}

func TestHeuristicsValidate(t *testing.T) {
	h := DefaultHeuristics()
	require.NoError(t, h.Validate())

	bad := h
	bad.TriggerPaths = append([]string{"["}, bad.TriggerPaths...)
	require.Error(t, bad.Validate())

	bad2 := h
	bad2.TriggerMessagePatterns = []string{"["}
	require.Error(t, bad2.Validate())
}
