package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLimitClassificationZeroValue(t *testing.T) {
	var c LimitClassification
	assert := assert.New(t)
	assert.Equal(LimitKindNone, c.Kind)
	assert.Empty(c.Agent)
	assert.True(c.ResetAt.IsZero())
	assert.Equal(time.Duration(0), c.CooldownFor)
	assert.Empty(c.Message)
}

func TestClassifyLimitProductionPatterns(t *testing.T) {
	// All nine substrings from the original isQuotaError set must
	// produce LimitKindQuota. This is the byte-for-byte regression
	// test for current Gemini and Codex detection.
	patterns := []string{
		"resource exhausted",
		"quota exceeded",
		"quota_exceeded",
		"quota exhausted",
		"quota_exhausted",
		"insufficient_quota",
		"exhausted your capacity",
		"capacity exhausted",
		"capacity_exhausted",
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			cls := ClassifyLimit("gemini", "agent failed: "+p+", retrying...")
			assert := assert.New(t)
			assert.Equal(LimitKindQuota, cls.Kind, "expected LimitKindQuota for %q", p)
			assert.Equal("gemini", cls.Agent)
			assert.Contains(cls.Message, p)
		})
	}
}

func TestClassifyLimitExtractsCooldownDuration(t *testing.T) {
	cls := ClassifyLimit(
		"gemini",
		"You have exhausted your capacity on this model. Your quota will reset after 48m20s.",
	)
	assert := assert.New(t)
	assert.Equal(LimitKindQuota, cls.Kind)
	assert.Equal(48*time.Minute+20*time.Second, cls.CooldownFor)
	assert.True(cls.ResetAt.IsZero(), "no absolute time in this message")
}

func TestClassifyLimitNegativeCases(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"empty", ""},
		{"unrelated error", "exit status 1: file not found"},
		{"benign mention of limit", "limit set to 100 in config"},
		{"benign rate limit (transient, no rule produces it day 1)", "429 rate limit, retrying"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := ClassifyLimit("gemini", tc.msg)
			assert.Equal(t, LimitKindNone, cls.Kind, "expected LimitKindNone for %q", tc.msg)
		})
	}
}

func TestClassifyLimitWithRulesIsolatesSyntheticPattern(t *testing.T) {
	// Synthetic rule used only inside this test — does not pollute
	// defaultLimitRules.
	syntheticRules := []limitRule{
		{Agents: []string{"*"}, Substring: "test-claude session limit", Kind: LimitKindSession},
	}
	cls := classifyLimitWithRules(
		"claude-code",
		"5-hour test-claude session limit reached",
		syntheticRules,
	)
	assert := assert.New(t)
	assert.Equal(LimitKindSession, cls.Kind)
	assert.Equal("claude-code", cls.Agent)

	// Same message via the production ClassifyLimit must not match —
	// the synthetic rule is not in defaultLimitRules.
	cls2 := ClassifyLimit("claude-code", "5-hour test-claude session limit reached")
	assert.Equal(LimitKindNone, cls2.Kind, "synthetic rule must not leak into defaultLimitRules")
}
