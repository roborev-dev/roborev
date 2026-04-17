package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildClassifyPrompt_Small(t *testing.T) {
	p, err := BuildClassifyPrompt(ClassifyInput{
		Subject:  "feat: add user auth middleware",
		Body:     "Adds JWT validation to the API layer.",
		DiffStat: " 2 files changed, 40 insertions(+)",
		Diff:     "+func Auth() {}\n+func Verify() {}\n",
		MaxBytes: 20000,
	})
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Contains(p, "feat: add user auth middleware")
	assert.Contains(p, "2 files changed")
	assert.Contains(p, "+func Auth()")
	assert.Contains(p, "routing classifier")
}

func TestBuildClassifyPrompt_Truncates(t *testing.T) {
	bigDiff := strings.Repeat("+line\n", 5000)
	p, err := BuildClassifyPrompt(ClassifyInput{
		Subject:  "refactor: large",
		DiffStat: " 10 files changed, 10000 insertions(+)",
		Diff:     bigDiff,
		MaxBytes: 1024,
	})
	require.NoError(t, err)
	assert.Contains(t, p, "truncated")
	assert.LessOrEqual(t, len(p), 4096)
}
