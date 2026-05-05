// Package agentlimit classifies agent CLI error messages as rate-limit,
// quota, or session-cap signals so the daemon worker and the foreground
// roborev fix loop can react consistently. Pure logic — no I/O.
package agentlimit

import (
	"strings"
	"time"
)

// Kind labels a classified agent error.
type Kind int

const (
	KindNone      Kind = iota // no rate-limit signal recognized
	KindTransient             // 429-style; retry locally, no cooldown
	KindQuota                 // hard quota exhaustion (Gemini/Codex today)
	KindSession               // session-level cap (e.g. Claude 5-hour)
)

// Classification is the result of inspecting an agent error.
type Classification struct {
	Kind        Kind
	Agent       string        // canonical agent name (caller resolves aliases)
	ResetAt     time.Time     // zero if not parseable from the message
	CooldownFor time.Duration // zero if not parseable; caller applies its own fallback
	Message     string        // raw error text (for logs / user display)
}

// ClassifyFunc is the function shape used by callers that want to inject
// a stub in tests.
type ClassifyFunc func(agent, errMsg string) Classification

// rule is one substring → kind mapping. The Agents slice restricts the
// rule to specific canonical agent names; "*" applies to any agent.
type rule struct {
	Agents    []string // canonical agent names; "*" = any
	Substring string   // case-insensitive substring match on the error message
	Kind      Kind
}

// defaultRules is the production rule table. The nine quota substrings
// are copied from the original isQuotaError set in
// internal/daemon/worker.go so detection for Gemini and Codex is
// byte-for-byte unchanged.
//
// Claude session-cap patterns are intentionally absent — see the design
// doc's "Detection without a captured Claude message" section. Once a
// real session-cap message is captured, a single KindSession rule plus
// a unit-test fixture is sufficient to enable Claude detection.
var defaultRules = []rule{
	{Agents: []string{"*"}, Substring: "resource exhausted", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "quota exceeded", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "quota_exceeded", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "quota exhausted", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "quota_exhausted", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "insufficient_quota", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "exhausted your capacity", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "capacity exhausted", Kind: KindQuota},
	{Agents: []string{"*"}, Substring: "capacity_exhausted", Kind: KindQuota},
}

// Classify inspects an agent error message and returns a Classification
// describing whether (and how) the agent is rate-limited. The agent
// argument is the canonical agent name; the caller is responsible for
// resolving any aliases (e.g. "claude" → "claude-code") before calling.
//
// Returns Kind == KindNone when no rule matches.
func Classify(agent, errMsg string) Classification {
	return classifyWithRules(agent, errMsg, defaultRules)
}

// classifyWithRules is Classify with an explicit rule slice. Unexported;
// used inside the package's own tests so synthetic fixtures (e.g. a
// KindSession pattern) do not leak into defaultRules.
func classifyWithRules(agent, errMsg string, rules []rule) Classification {
	if errMsg == "" {
		return Classification{Kind: KindNone, Agent: agent, Message: errMsg}
	}
	lower := strings.ToLower(errMsg)
	for _, r := range rules {
		if !ruleAppliesToAgent(r, agent) {
			continue
		}
		if !strings.Contains(lower, r.Substring) {
			continue
		}
		return Classification{
			Kind:        r.Kind,
			Agent:       agent,
			ResetAt:     ParseResetTime(errMsg),
			CooldownFor: ParseResetDuration(errMsg),
			Message:     errMsg,
		}
	}
	return Classification{Kind: KindNone, Agent: agent, Message: errMsg}
}

func ruleAppliesToAgent(r rule, agent string) bool {
	for _, a := range r.Agents {
		if a == "*" || a == agent {
			return true
		}
	}
	return false
}
