package autotype

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchMessage_Triggers(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		msg      string
		want     string
	}{
		{"no patterns", nil, "anything", ""},
		{"refactor word", []string{`\b(refactor|redesign)\b`}, "refactor: rework auth layer", "refactor"},
		{"breaking", []string{`\b(refactor|redesign|rewrite|architect|breaking)\b`}, "feat!: breaking change to API", "breaking"},
		{"no match", []string{`\brefactor\b`}, "fix: null deref", ""},
		{"case sensitive by default", []string{`\brefactor\b`}, "Refactor: ...", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := MatchMessage(c.patterns, c.msg)
			require.NoError(t, err)
			if c.want == "" {
				assert.Empty(t, m)
			} else {
				assert.True(t, strings.Contains(m, c.want), "got match %q, want contains %q", m, c.want)
			}
		})
	}
}

func TestMatchMessage_ConventionalSkip(t *testing.T) {
	assert := assert.New(t)
	patterns := []string{`^(docs|test|style|chore)(\(.+\))?:`}

	for _, msg := range []string{
		"docs: update README",
		"docs(readme): tweak wording",
		"test: add coverage",
		"test(auth): ...",
		"style: gofmt",
		"chore: bump deps",
	} {
		m, err := MatchMessage(patterns, msg)
		assert.NoError(err)
		assert.NotEmpty(m, "expected match for %q", msg)
	}

	for _, msg := range []string{
		"feat: add X",
		"fix: null deref",
		"docs: fix and docs: too",
	} {
		m, err := MatchMessage(patterns, msg)
		assert.NoError(err)
		if msg == "docs: fix and docs: too" {
			assert.NotEmpty(m)
		} else {
			assert.Empty(m, "expected no match for %q", msg)
		}
	}
}

func TestMatchMessage_InvalidRegex(t *testing.T) {
	_, err := MatchMessage([]string{"["}, "anything")
	assert.Error(t, err)
}
