package agentlimit

import (
	"strings"
	"time"
)

// minCooldown / maxCooldown clamp parsed durations to a sane range so a
// pathological message ("reset after 1ms" or "reset after 100h") does not
// produce a useless cooldown.
const (
	minCooldown = 1 * time.Minute
	maxCooldown = 24 * time.Hour
)

// ParseResetDuration extracts a Go-format duration from a "reset after
// <dur>" substring in errMsg (case-insensitive). Returns 0 if no such
// substring is present or the duration is unparseable. Clamps positive
// values to [minCooldown, maxCooldown].
func ParseResetDuration(errMsg string) time.Duration {
	lower := strings.ToLower(errMsg)
	idx := strings.Index(lower, "reset after ")
	if idx < 0 {
		return 0
	}
	rest := errMsg[idx+len("reset after "):]
	token := rest
	if sp := strings.IndexAny(rest, " \t\n,;)"); sp > 0 {
		token = rest[:sp]
	}
	token = strings.TrimRight(token, ".,;:)]}")
	d, err := time.ParseDuration(token)
	if err != nil || d <= 0 {
		return 0
	}
	if d < minCooldown {
		return minCooldown
	}
	if d > maxCooldown {
		return maxCooldown
	}
	return d
}
