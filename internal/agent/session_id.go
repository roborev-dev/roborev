package agent

import "regexp"

var resumeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:][A-Za-z0-9._:-]{0,127}$`)

// IsValidResumeSessionID reports whether a persisted session ID is safe to
// forward back to a CLI resume command.
func IsValidResumeSessionID(sessionID string) bool {
	return resumeSessionIDPattern.MatchString(sessionID)
}

func sanitizedResumeSessionID(sessionID string) string {
	if !IsValidResumeSessionID(sessionID) {
		return ""
	}
	return sessionID
}
