package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
		assert.Equal(t, tt.want, IsValidResumeSessionID(tt.sessionID), "IsValidResumeSessionID(%q)", tt.sessionID)
	}
}
