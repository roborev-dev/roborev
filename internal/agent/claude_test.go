package agent

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

// toolsArgValue returns the value of the --allowedTools argument.
func toolsArgValue(t *testing.T, args []string) string {
	t.Helper()
	idx := slices.Index(args, "--allowedTools")
	if idx != -1 && idx+1 < len(args) {
		return args[idx+1]
	}
	t.Fatal("--allowedTools not found in args")
	return ""
}

func assertTools(t *testing.T, args []string, want []string, dontWant []string) {
	t.Helper()
	val := toolsArgValue(t, args)
	gotTools := strings.Split(val, ",")
	for i := range gotTools {
		gotTools[i] = strings.TrimSpace(gotTools[i])
	}

	// Check required
	for _, w := range want {
		if !slices.Contains(gotTools, w) {
			t.Errorf("missing tool %q in %v", w, gotTools)
		}
	}
	// Check forbidden
	for _, d := range dontWant {
		if slices.Contains(gotTools, d) {
			t.Errorf("forbidden tool %q found in %v", d, gotTools)
		}
	}
}

func TestClaudeBuildArgs(t *testing.T) {
	a := NewClaudeAgent("claude")

	t.Run("ReviewMode", func(t *testing.T) {
		// Non-agentic mode (review only): read-only tools, no dangerous flag
		args := a.buildArgs(false)
		for _, req := range []string{"--output-format", "stream-json", "--verbose", "-p", "--allowedTools"} {
			assertContainsArg(t, args, req)
		}
		assertNotContainsArg(t, args, claudeDangerousFlag)

		assertTools(t, args,
			[]string{"Read", "Glob", "Grep"},
			[]string{"Edit", "Write", "Bash"})
	})

	t.Run("AgenticMode", func(t *testing.T) {
		// Agentic mode: write tools + dangerous flag
		args := a.buildArgs(true)
		assertContainsArg(t, args, claudeDangerousFlag)
		assertContainsArg(t, args, "--allowedTools")

		assertTools(t, args,
			[]string{"Edit", "Write", "Bash"},
			nil)
	})
}

func TestClaudeDangerousFlagSupport(t *testing.T) {
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
	if err == nil || !strings.Contains(err.Error(), "does not support") {
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
			var out bytes.Buffer
			var w io.Writer
			if tt.expectOutput {
				w = &out
			}

			res, err := parseStreamJSON(strings.NewReader(tt.input), w)

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

			if tt.expectOutput && out.Len() == 0 {
				t.Error("expected output to be written")
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
	tests := []struct {
		name     string
		env      []string
		keys     []string
		expected []string
	}{
		{
			name:     "SingleKey",
			env:      []string{"PATH=/bin", "KEY=secret", "OTHER=val"},
			keys:     []string{"KEY"},
			expected: []string{"PATH=/bin", "OTHER=val"},
		},
		{
			name:     "MultipleKeys",
			env:      []string{"PATH=/bin", "KEY1=s1", "KEY2=s2", "HOME=/home"},
			keys:     []string{"KEY1", "KEY2"},
			expected: []string{"PATH=/bin", "HOME=/home"},
		},
		{
			name:     "ExactMatchOnly",
			env:      []string{"PRE_KEY=1", "KEY=1", "KEY_POST=1"},
			keys:     []string{"KEY"},
			expected: []string{"PRE_KEY=1", "KEY_POST=1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterEnv(tt.env, tt.keys...)
			if !slices.Equal(got, tt.expected) {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}
