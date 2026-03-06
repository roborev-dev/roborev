package agent

import "testing"

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "claude init session_id",
			line: `{"type":"system","subtype":"init","session_id":"claude-session-123"}`,
			want: "claude-session-123",
		},
		{
			name: "camel sessionId",
			line: `{"type":"init","sessionId":"camel-session-456"}`,
			want: "camel-session-456",
		},
		{
			name: "codex thread started",
			line: `{"type":"thread.started","thread_id":"thread-789"}`,
			want: "thread-789",
		},
		{
			name: "other thread id ignored",
			line: `{"type":"turn.started","thread_id":"thread-789"}`,
			want: "",
		},
		{
			name: "plain text ignored",
			line: "not json",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractSessionID(tc.line); got != tc.want {
				t.Fatalf("ExtractSessionID(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}
