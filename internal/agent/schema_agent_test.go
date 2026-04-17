package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSchemaAgent struct {
	*TestAgent
	result json.RawMessage
	err    error
}

func (f *fakeSchemaAgent) ClassifyWithSchema(
	ctx context.Context, repoPath, gitRef, prompt string,
	schema json.RawMessage, out io.Writer,
) (json.RawMessage, error) {
	return f.result, f.err
}

func TestIsSchemaAgent(t *testing.T) {
	var a Agent = NewTestAgent()
	assert.False(t, IsSchemaAgent(a))

	var s Agent = &fakeSchemaAgent{TestAgent: NewTestAgent()}
	assert.True(t, IsSchemaAgent(s))
}

func TestValidateClassifyAgent_NotRegistered(t *testing.T) {
	err := ValidateClassifyAgent("no-such-agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown agent")
}

func TestValidateClassifyAgent_NotSchema(t *testing.T) {
	err := ValidateClassifyAgent("test")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "structured output")
}
