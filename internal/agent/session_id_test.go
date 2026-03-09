package agent

import "testing"

func TestIsValidResumeSessionID(t *testing.T) {
	tests := []struct {
		sessionID string
		want      bool
	}{
		{sessionID: "session-123", want: true},
		{sessionID: "thread:abc.def", want: true},
		{sessionID: "", want: false},
		{sessionID: "-session-123", want: false},
		{sessionID: "session 123", want: false},
		{sessionID: "session\t123", want: false},
		{sessionID: "session\n123", want: false},
	}

	for _, tt := range tests {
		if got := IsValidResumeSessionID(tt.sessionID); got != tt.want {
			t.Fatalf("IsValidResumeSessionID(%q) = %v, want %v", tt.sessionID, got, tt.want)
		}
	}
}
