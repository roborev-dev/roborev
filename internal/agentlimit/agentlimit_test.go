package agentlimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClassificationZeroValue(t *testing.T) {
	var c Classification
	assert := assert.New(t)
	assert.Equal(KindNone, c.Kind)
	assert.Empty(c.Agent)
	assert.True(c.ResetAt.IsZero())
	assert.Equal(time.Duration(0), c.CooldownFor)
	assert.Empty(c.Message)
}

func TestClassifyProductionPatterns(t *testing.T) {
	// All nine substrings from the original isQuotaError set must
	// produce KindQuota. This is the byte-for-byte regression test
	// for current Gemini and Codex detection.
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
			cls := Classify("gemini", "agent failed: "+p+", retrying...")
			assert := assert.New(t)
			assert.Equal(KindQuota, cls.Kind, "expected KindQuota for %q", p)
			assert.Equal("gemini", cls.Agent)
			assert.Contains(cls.Message, p)
		})
	}
}

func TestClassifyExtractsCooldownDuration(t *testing.T) {
	cls := Classify(
		"gemini",
		"You have exhausted your capacity on this model. Your quota will reset after 48m20s.",
	)
	assert := assert.New(t)
	assert.Equal(KindQuota, cls.Kind)
	assert.Equal(48*time.Minute+20*time.Second, cls.CooldownFor)
	assert.True(cls.ResetAt.IsZero(), "no absolute time in this message")
}

func TestClassifyNegativeCases(t *testing.T) {
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
			cls := Classify("gemini", tc.msg)
			assert.Equal(t, KindNone, cls.Kind, "expected KindNone for %q", tc.msg)
		})
	}
}

func TestClassifyWithRulesIsolatesSyntheticPattern(t *testing.T) {
	// Synthetic rule used only inside this test — does not pollute defaultRules.
	syntheticRules := []rule{
		{Agents: []string{"*"}, Substring: "test-claude session limit", Kind: KindSession},
	}
	cls := classifyWithRules(
		"claude-code",
		"5-hour test-claude session limit reached",
		syntheticRules,
	)
	assert := assert.New(t)
	assert.Equal(KindSession, cls.Kind)
	assert.Equal("claude-code", cls.Agent)

	// Same message via the production Classify must not match —
	// the synthetic rule is not in defaultRules.
	cls2 := Classify("claude-code", "5-hour test-claude session limit reached")
	assert.Equal(KindNone, cls2.Kind, "synthetic rule must not leak into defaultRules")
}
