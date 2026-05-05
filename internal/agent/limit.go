package agent

import (
	"strings"
	"time"
)

// LimitKind labels a classified agent error.
type LimitKind int

const (
	LimitKindNone      LimitKind = iota // no rate-limit signal recognized
	LimitKindTransient                  // 429-style; retry locally, no cooldown
	LimitKindQuota                      // hard quota exhaustion (Gemini/Codex today)
	LimitKindSession                    // session-level cap (e.g. Claude 5-hour)
)

// LimitClassification is the result of inspecting an agent error.
type LimitClassification struct {
	Kind        LimitKind
	Agent       string        // canonical agent name (caller resolves aliases)
	ResetAt     time.Time     // zero if not parseable from the message
	CooldownFor time.Duration // zero if not parseable; caller applies its own fallback
	Message     string        // raw error text (for logs / user display)
}

// LimitClassifier is the function shape used by callers that want to inject
// a stub in tests.
type LimitClassifier func(agent, errMsg string) LimitClassification

// limitRule is one substring → kind mapping. The Agents slice restricts
// the rule to specific canonical agent names; "*" applies to any agent.
type limitRule struct {
	Agents    []string // canonical agent names; "*" = any
	Substring string   // case-insensitive substring match on the error message
	Kind      LimitKind
}

// defaultLimitRules is the production rule table. The nine quota
// substrings are copied from the original isQuotaError set in
// internal/daemon/worker.go so detection for Gemini and Codex is
// byte-for-byte unchanged.
//
// Claude session-cap patterns are intentionally absent — once a real
// session-cap message is captured, a single LimitKindSession rule plus
// a unit-test fixture is sufficient to enable Claude detection.
var defaultLimitRules = []limitRule{
	{Agents: []string{"*"}, Substring: "resource exhausted", Kind: LimitKindQuota},
	{Agents: []string{"*"}, Substring: "quota exceeded", Kind: LimitKindQuota},
	{Agents: []string{"*"}, Substring: "quota_exceeded", Kind: LimitKindQuota},
	{Agents: []string{"*"}, Substring: "quota exhausted", Kind: LimitKindQuota},
	{Agents: []string{"*"}, Substring: "quota_exhausted", Kind: LimitKindQuota},
	{Agents: []string{"*"}, Substring: "insufficient_quota", Kind: LimitKindQuota},
	{Agents: []string{"*"}, Substring: "exhausted your capacity", Kind: LimitKindQuota},
	{Agents: []string{"*"}, Substring: "capacity exhausted", Kind: LimitKindQuota},
	{Agents: []string{"*"}, Substring: "capacity_exhausted", Kind: LimitKindQuota},
}

// ClassifyLimit inspects an agent error message and returns a
// LimitClassification describing whether (and how) the agent is
// rate-limited. The agent argument is the canonical agent name; the
// caller is responsible for resolving any aliases (e.g. "claude" →
// "claude-code") before calling.
//
// Returns Kind == LimitKindNone when no rule matches.
func ClassifyLimit(agent, errMsg string) LimitClassification {
	return classifyLimitWithRules(agent, errMsg, defaultLimitRules)
}

// classifyLimitWithRules is ClassifyLimit with an explicit rule slice.
// Unexported; used inside the package's own tests so synthetic fixtures
// (e.g. a LimitKindSession pattern) do not leak into defaultLimitRules.
func classifyLimitWithRules(agent, errMsg string, rules []limitRule) LimitClassification {
	if errMsg == "" {
		return LimitClassification{Kind: LimitKindNone, Agent: agent, Message: errMsg}
	}
	lower := strings.ToLower(errMsg)
	for _, r := range rules {
		if !limitRuleAppliesToAgent(r, agent) {
			continue
		}
		if !strings.Contains(lower, r.Substring) {
			continue
		}
		return LimitClassification{
			Kind:        r.Kind,
			Agent:       agent,
			ResetAt:     ParseResetTime(errMsg),
			CooldownFor: ParseResetDuration(errMsg),
			Message:     errMsg,
		}
	}
	return LimitClassification{Kind: LimitKindNone, Agent: agent, Message: errMsg}
}

func limitRuleAppliesToAgent(r limitRule, agent string) bool {
	for _, a := range r.Agents {
		if a == "*" || a == agent {
			return true
		}
	}
	return false
}
