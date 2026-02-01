package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTruncateStderr(t *testing.T) {
	// Short string - no truncation
	short := "short stderr"
	if got := truncateStderr(short); got != short {
		t.Errorf("expected no truncation for short string, got %q", got)
	}

	// Exactly at limit - no truncation
	exact := strings.Repeat("x", maxStderrLen)
	if got := truncateStderr(exact); got != exact {
		t.Errorf("expected no truncation at exact limit, got len=%d", len(got))
	}

	// Over limit - should truncate
	over := strings.Repeat("x", maxStderrLen+100)
	got := truncateStderr(over)
	if !strings.HasSuffix(got, "... (truncated)") {
		t.Errorf("expected truncation suffix, got %q", got)
	}
	if len(got) != maxStderrLen+len("... (truncated)") {
		t.Errorf("expected truncated length %d, got %d", maxStderrLen+len("... (truncated)"), len(got))
	}
}

func TestGeminiBuildArgs(t *testing.T) {
	a := NewGeminiAgent("gemini")

	// Non-agentic mode (review only): read-only tools, no yolo
	args := a.buildArgs(false)
	argsStr := strings.Join(args, " ")
	if containsString(args, "--yolo") {
		t.Fatalf("expected no --yolo in review mode, got %v", args)
	}
	if !containsString(args, "--output-format") || !containsString(args, "stream-json") {
		t.Fatalf("expected --output-format stream-json, got %v", args)
	}
	if !containsString(args, "--allowed-tools") {
		t.Fatalf("expected --allowed-tools, got %v", args)
	}
	// Review mode should have read-only tools (no Edit, Write, Bash, or Shell)
	if strings.Contains(argsStr, "Edit") || strings.Contains(argsStr, "Write") ||
		strings.Contains(argsStr, "Bash") || strings.Contains(argsStr, "Shell") {
		t.Fatalf("expected read-only tools in review mode (no Edit/Write/Bash/Shell), got %v", args)
	}

	// Agentic mode: write tools + yolo
	args = a.buildArgs(true)
	argsStr = strings.Join(args, " ")
	if !containsString(args, "--yolo") {
		t.Fatalf("expected --yolo in agentic mode, got %v", args)
	}
	if !containsString(args, "--allowed-tools") {
		t.Fatalf("expected --allowed-tools in agentic mode, got %v", args)
	}
	// Agentic mode should have write tools
	if !strings.Contains(argsStr, "Edit") || !strings.Contains(argsStr, "Write") {
		t.Fatalf("expected write tools in agentic mode, got %v", args)
	}
}

func TestGeminiParseStreamJSON(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantResult   string
		wantContains []string // if set, check result contains each substring instead of exact match
		wantErr      error    // if non-nil, expect errors.Is match
		wantOutput   bool     // if true, pass a writer and check it received data
	}{
		{
			name: "ResultEvent",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"Working on it..."}}
{"type":"result","result":"Done! Review complete."}
`,
			wantResult: "Done! Review complete.",
		},
		{
			name: "GeminiMessageFormat",
			input: `{"type":"message","timestamp":"2026-01-19T17:49:13.445Z","role":"assistant","content":"Changes:\n- Created file.ts","delta":true}
{"type":"message","timestamp":"2026-01-19T17:49:13.447Z","role":"assistant","content":" with filtering logic.","delta":true}
{"type":"result","timestamp":"2026-01-19T17:49:13.519Z","status":"success","stats":{"total_tokens":1000}}
`,
			wantContains: []string{"Changes:", "filtering logic"},
		},
		{
			name: "AssistantFallback",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"First message"}}
{"type":"assistant","message":{"content":"Second message"}}
`,
			wantResult: "First message\nSecond message",
		},
		{
			name: "NoValidEvents",
			input: `not json at all
still not json
`,
			wantErr: errNoStreamJSON,
		},
		{
			name: "StreamsToOutput",
			input: `{"type":"system","subtype":"init"}
{"type":"result","result":"Done"}
`,
			wantResult: "Done",
			wantOutput: true,
		},
		{
			name: "EmptyResult",
			input: `{"type":"system","subtype":"init"}
{"type":"tool","name":"Read"}
`,
			wantResult: "",
		},
		{
			name: "PlainTextError",
			input: `This is a plain text review.
No issues found in the code.
`,
			wantErr: errNoStreamJSON,
		},
	}

	a := NewGeminiAgent("gemini")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sw *syncWriter
			var output bytes.Buffer
			if tc.wantOutput {
				sw = newSyncWriter(&output)
			}

			parsed, err := a.parseStreamJSON(strings.NewReader(tc.input), sw)

			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.wantContains != nil {
				for _, s := range tc.wantContains {
					if !strings.Contains(parsed.result, s) {
						t.Errorf("expected result to contain %q, got %q", s, parsed.result)
					}
				}
			} else if parsed.result != tc.wantResult {
				t.Fatalf("expected result %q, got %q", tc.wantResult, parsed.result)
			}

			if tc.wantOutput && output.Len() == 0 {
				t.Fatal("expected output to be written")
			}
		})
	}
}

func TestGeminiReview_PlainTextError(t *testing.T) {
	skipIfWindows(t)
	// End-to-end test: create a temp script that emits plain text (no stream-json)
	// should return an error since we require stream-json
	scriptPath := writeTempCommand(t, `#!/bin/sh
echo "Plain text review output"
echo "No issues found."
`)

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	_, err := a.Review(context.Background(), t.TempDir(), "abc123", "Review this code", &output)
	if err == nil {
		t.Fatal("expected error for plain text output, got nil")
	}
	// Should return actionable error message
	if !strings.Contains(err.Error(), "stream-json") {
		t.Errorf("expected error to mention stream-json, got %v", err)
	}
	// Should preserve sentinel for errors.Is
	if !errors.Is(err, errNoStreamJSON) {
		t.Errorf("expected errors.Is(err, errNoStreamJSON) to be true, got false")
	}
}

func TestGeminiReview_PlainTextErrorWithStderr(t *testing.T) {
	scriptPath := writeTempCommand(t, `#!/bin/sh
echo "Plain text review output"
echo "Some stderr message" >&2
`)

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	_, err := a.Review(context.Background(), t.TempDir(), "abc123", "Review this code", &output)
	if err == nil {
		t.Fatal("expected error for plain text output, got nil")
	}
	// Should include stderr in error message
	if !strings.Contains(err.Error(), "Some stderr message") {
		t.Errorf("expected error to include stderr, got %v", err)
	}
}

func TestGeminiReview_LargeStderrTruncation(t *testing.T) {
	scriptPath := writeTempCommand(t, `#!/bin/sh
echo "Plain text"
for i in $(seq 1 200); do
	echo "This is a long stderr line number $i that will contribute to the total size" >&2
done
`)

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	_, err := a.Review(context.Background(), t.TempDir(), "abc123", "Review this code", &output)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Should be truncated
	if !strings.Contains(err.Error(), "... (truncated)") {
		t.Errorf("expected error to indicate truncation, got %v", err)
	}
}

func TestGeminiReview_StreamJSON(t *testing.T) {
	scriptPath := writeTempCommand(t, `#!/bin/sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"result","result":"Review complete. All good!"}'
`)

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	result, err := a.Review(context.Background(), t.TempDir(), "abc123", "Review this code", &output)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Should return the parsed result
	if result != "Review complete. All good!" {
		t.Errorf("expected parsed result, got %q", result)
	}
}

func TestGeminiReview_StreamJSONNoResult(t *testing.T) {
	scriptPath := writeTempCommand(t, `#!/bin/sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"tool","name":"Read","input":{"path":"foo.go"}}'
echo '{"type":"tool_result","content":"file contents here"}'
`)

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	result, err := a.Review(context.Background(), t.TempDir(), "abc123", "Review this code", &output)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Should return "No review output generated" since no result/assistant content
	if result != "No review output generated" {
		t.Errorf("expected 'No review output generated', got %q", result)
	}
}

func TestGeminiReview_IOError(t *testing.T) {
	scriptPath := writeTempCommand(t, `#!/bin/sh
echo "Error message" >&2
exit 1
`)

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	_, err := a.Review(context.Background(), t.TempDir(), "abc123", "Review this code", &output)
	if err == nil {
		t.Fatal("expected error for failed command, got nil")
	}
	// Should contain information about the failure
	if !strings.Contains(err.Error(), "gemini failed") {
		t.Errorf("expected error to mention 'gemini failed', got %v", err)
	}
}

func TestGeminiReview_PromptDeliveredViaStdin(t *testing.T) {
	skipIfWindows(t)

	stdinFile := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv("MOCK_STDIN_FILE", stdinFile)

	scriptPath := writeTempCommand(t, `#!/bin/sh
cat > "$MOCK_STDIN_FILE"
echo '{"type":"result","result":"Done"}'
`)

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	expectedPrompt := "Please review this code for security issues"
	_, err := a.Review(context.Background(), t.TempDir(), "abc123", expectedPrompt, &output)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Verify the prompt was received
	receivedPrompt, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("failed to read prompt file: %v", err)
	}
	if string(receivedPrompt) != expectedPrompt {
		t.Errorf("prompt not delivered correctly: expected %q, got %q", expectedPrompt, string(receivedPrompt))
	}
}
