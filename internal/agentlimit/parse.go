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

// ParseResetTime extracts an absolute reset time from messages like
// "resets at 5:42 PM" or "try again at 17:42". Interprets the parsed
// clock time in the local timezone. Returns the zero time.Time if no
// recognized phrase is present or the time is unparseable.
//
// If the parsed clock time is earlier than now-on-the-same-day, the
// returned time rolls forward by 24 hours so callers that compute
// "time until reset" never get a negative duration.
func ParseResetTime(errMsg string) time.Time {
	return parseResetTimeAt(errMsg, time.Now())
}

// parseResetTimeAt is ParseResetTime with an injectable clock for tests.
func parseResetTimeAt(errMsg string, now time.Time) time.Time {
	lower := strings.ToLower(errMsg)
	var idx int
	switch {
	case strings.Contains(lower, "resets at "):
		idx = strings.Index(lower, "resets at ") + len("resets at ")
	case strings.Contains(lower, "try again at "):
		idx = strings.Index(lower, "try again at ") + len("try again at ")
	default:
		return time.Time{}
	}
	// Consume up to the next non-time character. Allow digits, ':', and
	// AM/PM markers (with optional spaces before them).
	rest := errMsg[idx:]
	end := len(rest)
	for i, r := range rest {
		// Stop at sentence-ending punctuation or newline.
		if r == '.' || r == ',' || r == ';' || r == '\n' || r == ')' {
			end = i
			break
		}
	}
	token := strings.TrimSpace(rest[:end])

	formats := []string{
		"3:04 PM", "3:04 pm",
		"3:04PM", "3:04pm",
		"15:04",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, token); err == nil {
			candidate := time.Date(
				now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), 0, 0, now.Location(),
			)
			if candidate.Before(now) {
				candidate = candidate.Add(24 * time.Hour)
			}
			return candidate
		}
	}
	return time.Time{}
}
