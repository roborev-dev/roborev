// Package agentlimit classifies agent CLI error messages as rate-limit,
// quota, or session-cap signals so the daemon worker and the foreground
// roborev fix loop can react consistently. Pure logic — no I/O.
package agentlimit

import "time"

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
