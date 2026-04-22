package autotype

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecisionZeroValue(t *testing.T) {
	var d Decision
	assert.False(t, d.Run)
	assert.Empty(t, d.Reason)
	assert.Empty(t, d.Method)
}

func TestInputZeroValue(t *testing.T) {
	var in Input
	assert.Empty(t, in.RepoPath)
	assert.Empty(t, in.GitRef)
	assert.Empty(t, in.Diff)
	assert.Empty(t, in.Message)
	assert.Nil(t, in.ChangedFiles)
}

func TestAnyMatch(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{"empty patterns", nil, "foo.go", false},
		{"exact file", []string{"foo.go"}, "foo.go", true},
		{"simple star", []string{"*.go"}, "foo.go", true},
		{"star does not cross /", []string{"*.go"}, "a/foo.go", false},
		{"doublestar crosses /", []string{"**/*.go"}, "a/b/foo.go", true},
		{"migrations anywhere", []string{"**/migrations/**"}, "internal/db/migrations/001.sql", true},
		{"migrations top-level", []string{"**/migrations/**"}, "migrations/001.sql", true},
		{"schema file", []string{"**/*.sql"}, "db/schema.sql", true},
		{"no match", []string{"**/migrations/**"}, "internal/db/001.sql", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := AnyMatch(c.patterns, c.path)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestAllMatch(t *testing.T) {
	assert := assert.New(t)
	got, err := AllMatch([]string{"**/*.md", "**/testdata/**"},
		[]string{"README.md", "internal/testdata/x.json"})
	require.NoError(t, err)
	assert.True(got)

	got, err = AllMatch([]string{"**/*.md"},
		[]string{"README.md", "foo.go"})
	require.NoError(t, err)
	assert.False(got)

	got, err = AllMatch([]string{"**/*.md"}, nil)
	require.NoError(t, err)
	assert.False(got)
}

func TestAnyMatchInvalidPattern(t *testing.T) {
	_, err := AnyMatch([]string{"["}, "foo")
	require.Error(t, err)
}
