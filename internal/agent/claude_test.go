package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// toolsArgValue returns the value of the --allowedTools argument.
func toolsArgValue(t *testing.T, args []string) string {
	t.Helper()
	for i, a := range args {
		if a == "--allowedTools" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatal("--allowedTools not found in args")
	return ""
}

func TestClaudeBuildArgs(t *testing.T) {
	a := NewClaudeAgent("claude")

	// Non-agentic mode (review only): read-only tools, no dangerous flag
	args := a.buildArgs(false)
	for _, req := range []string{"--output-format", "stream-json", "--verbose", "-p", "--allowedTools"} {
		assertContainsArg(t, args, req)
	}
	assertNotContainsArg(t, args, claudeDangerousFlag)
	tools := toolsArgValue(t, args)
	toolList := strings.Split(tools, ",")
	for _, forb := range []string{"Edit", "Write", "Bash"} {
		for _, tool := range toolList {
			if strings.TrimSpace(tool) == forb {
				t.Errorf("non-agentic tools should not contain %q, got %q", forb, tools)
			}
		}
	}

	// Agentic mode: write tools + dangerous flag
	args = a.buildArgs(true)
	assertContainsArg(t, args, claudeDangerousFlag)
	assertContainsArg(t, args, "--allowedTools")
	tools = toolsArgValue(t, args)
	toolList = strings.Split(tools, ",")
	for _, req := range []string{"Edit", "Write"} {
		found := false
		for _, tool := range toolList {
			if strings.TrimSpace(tool) == req {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("agentic tools should contain %q, got %q", req, tools)
		}
	}
}

func TestClaudeSupportsDangerousFlagAllowsNonZeroHelp(t *testing.T) {
	cmdPath := writeTempCommand(t, "#!/bin/sh\necho \"usage "+claudeDangerousFlag+"\"; exit 1\n")

	supported, err := claudeSupportsDangerousFlag(context.Background(), cmdPath)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !supported {
		t.Fatalf("expected dangerous flag support, got false")
	}
}

func TestClaudeReviewUnsafeMissingFlagErrors(t *testing.T) {
	withUnsafeAgents(t, true)

	cmdPath := writeTempCommand(t, "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then echo \"usage\"; exit 0; fi\nexit 0\n")

	a := NewClaudeAgent(cmdPath)
	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("expected unsupported flag error, got %v", err)
	}
}

func TestParseStreamJSON(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectedResult string
		expectedErr    string // substring match
		expectOutput   bool   // verify output buffer was written to
	}{
		{
			name: "ResultEvent",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"Working on it..."}}
{"type":"result","result":"Done! Created the file."}
`,
			expectedResult: "Done! Created the file.",
		},
		{
			name: "AssistantFallback",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"First message"}}
{"type":"assistant","message":{"content":"Second message"}}
`,
			expectedResult: "First message\nSecond message",
		},
		{
			name: "MalformedLines",
			input: `{"type":"system","subtype":"init"}
not valid json
{"type":"result","result":"Success"}
also not json
`,
			expectedResult: "Success",
		},
		{
			name: "NoValidEvents",
			input: `not json at all
still not json
`,
			expectedErr: "no valid stream-json events",
		},
		{
			name:        "EmptyInput",
			input:       "",
			expectedErr: "no valid stream-json events",
		},
		{
			name: "StreamsToOutput",
			input: `{"type":"system","subtype":"init"}
{"type":"result","result":"Done"}
`,
			expectedResult: "Done",
			expectOutput:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewClaudeAgent("claude")

			if tt.expectOutput {
				var out bytes.Buffer
				res, err := a.parseStreamJSON(strings.NewReader(tt.input), &out)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if res != tt.expectedResult {
					t.Errorf("expected result %q, got %q", tt.expectedResult, res)
				}
				if out.Len() == 0 {
					t.Error("expected output to be written")
				}
				return
			}

			res, err := a.parseStreamJSON(strings.NewReader(tt.input), nil)

			if tt.expectedErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.expectedErr) {
					t.Errorf("expected error containing %q, got %v", tt.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res != tt.expectedResult {
				t.Errorf("expected result %q, got %q", tt.expectedResult, res)
			}
		})
	}
}

func TestAnthropicAPIKey(t *testing.T) {
	// Clear any existing key
	SetAnthropicAPIKey("")
	t.Cleanup(func() { SetAnthropicAPIKey("") })

	// Initially should be empty
	if key := AnthropicAPIKey(); key != "" {
		t.Fatalf("expected empty API key, got %q", key)
	}

	// Set a key
	SetAnthropicAPIKey("test-api-key")
	if key := AnthropicAPIKey(); key != "test-api-key" {
		t.Fatalf("expected 'test-api-key', got %q", key)
	}

	// Clear the key
	SetAnthropicAPIKey("")
	if key := AnthropicAPIKey(); key != "" {
		t.Fatalf("expected empty API key after clear, got %q", key)
	}
}

func TestFilterEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/home/test",
		"ANTHROPIC_API_KEY=secret-key",
		"OTHER_VAR=value",
	}

	filtered := filterEnv(env, "ANTHROPIC_API_KEY")

	// Should have 3 items (ANTHROPIC_API_KEY removed)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 env vars, got %d: %v", len(filtered), filtered)
	}

	// Verify ANTHROPIC_API_KEY is not present
	for _, e := range filtered {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY=") {
			t.Fatalf("ANTHROPIC_API_KEY should be filtered out, got %v", filtered)
		}
	}

	// Verify other vars are present
	found := make(map[string]bool)
	for _, e := range filtered {
		if strings.HasPrefix(e, "PATH=") {
			found["PATH"] = true
		}
		if strings.HasPrefix(e, "HOME=") {
			found["HOME"] = true
		}
		if strings.HasPrefix(e, "OTHER_VAR=") {
			found["OTHER_VAR"] = true
		}
	}
	if !found["PATH"] || !found["HOME"] || !found["OTHER_VAR"] {
		t.Fatalf("missing expected env vars in filtered result: %v", filtered)
	}
}

func TestFilterEnvMultipleKeys(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"ANTHROPIC_API_KEY=secret",
		"CLAUDECODE=1",
		"HOME=/home/test",
	}

	filtered := filterEnv(env, "ANTHROPIC_API_KEY", "CLAUDECODE")

	if len(filtered) != 2 {
		t.Fatalf("expected 2 env vars, got %d: %v", len(filtered), filtered)
	}
	for _, e := range filtered {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY=") {
			t.Fatal("ANTHROPIC_API_KEY should be stripped")
		}
		if strings.HasPrefix(e, "CLAUDECODE=") {
			t.Fatal("CLAUDECODE should be stripped")
		}
	}
}

func TestFilterEnvExactMatchOnly(t *testing.T) {
	env := []string{
		"CLAUDE=1",
		"CLAUDECODE=1",
		"CLAUDE_NO_SOUND=1",
		"PATH=/usr/bin",
	}

	filtered := filterEnv(env, "CLAUDE")

	if len(filtered) != 3 {
		t.Fatalf("expected 3 env vars, got %d: %v", len(filtered), filtered)
	}
	for _, e := range filtered {
		k, _, _ := strings.Cut(e, "=")
		if k == "CLAUDE" {
			t.Fatal("CLAUDE should be stripped")
		}
	}
}
