package autotype

import (
	"fmt"
	"regexp"
)

// MatchMessage returns the first matching substring from msg for the first
// pattern in patterns that matches. Returns "" (no error) when no pattern
// matches. Returns an error if a pattern is an invalid regex.
//
// Only the subject line (first line) of msg is considered.
func MatchMessage(patterns []string, msg string) (string, error) {
	subject := msg
	for i, c := range msg {
		if c == '\n' {
			subject = msg[:i]
			break
		}
	}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return "", fmt.Errorf("invalid regex %q: %w", p, err)
		}
		if m := re.FindString(subject); m != "" {
			return m, nil
		}
	}
	return "", nil
}
