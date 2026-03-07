package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTruncateStderr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"Short", "short stderr", "short stderr"},
		{"ExactLimit", strings.Repeat("x", maxStderrLen), strings.Repeat("x", maxStderrLen)},
		{"OverLimit", strings.Repeat("x", maxStderrLen+100), strings.Repeat("x", maxStderrLen) + "... (truncated)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateStderr(tc.input); got != tc.want {
				t.Errorf("truncateStderr() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGeminiBuildArgs(t *testing.T) {
	tests := []struct {
		name         string
		agentic      bool
		wantFlags    []string          // Standalone boolean flags
		wantArgPairs map[string]string // Flag -> exact next token
		unwantedArgs []string          // Tokens expected NOT in args
	}{
		{
			name:    "ReviewMode",
			agentic: false,
			wantArgPairs: map[string]string{
				"--output-format": "stream-json",
				"--allowed-tools": "Read,Glob,Grep",
			},
			unwantedArgs: []string{
				"--yolo",
				"Edit", "Write", "Bash", "Shell",
			},
		},
		{
			name:      "AgenticMode",
			agentic:   true,
			wantFlags: []string{"--yolo"},
			wantArgPairs: map[string]string{
				"--output-format": "stream-json",
				"--allowed-tools": "Edit,Write,Read,Glob,Grep,Bash,Shell",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := NewGeminiAgent("gemini")
			args := a.buildArgs(tc.agentic)

			// Check standalone boolean flags
			for _, flag := range tc.wantFlags {
				if !slices.Contains(args, flag) {
					t.Errorf("missing flag %q in args %v", flag, args)
				}
			}

			// Check flag-value pairs by exact next token
			for flag, val := range tc.wantArgPairs {
				assertFlagValue(t, args, flag, val)
			}

			// Check absence of specific tokens
			for _, unwanted := range tc.unwantedArgs {
				if slices.Contains(args, unwanted) {
					t.Errorf("unexpected token %q in args %v", unwanted, args)
				}
			}
		})
	}
}

func assertFlagValue(t *testing.T, args []string, flag, expectedVal string) {
	t.Helper()
	count := 0
	for _, a := range args {
		if a == flag {
			count++
		}
	}
	if count != 1 {
		t.Errorf(
			"flag %q appears %d times, want exactly 1 in args %v",
			flag, count, args,
		)
		return
	}

	idx := slices.Index(args, flag)
	if idx+1 >= len(args) {
		t.Errorf("flag %q is last arg, expected value %q", flag, expectedVal)
		return
	}
	if args[idx+1] != expectedVal {
		t.Errorf("flag %q: got value %q, want %q", flag, args[idx+1], expectedVal)
	}
}

func TestGeminiParseStreamJSON(t *testing.T) {
	tests := []struct {
		name               string
		input              string
		wantResult         string
		wantResultContains []string
		wantErr            error // if non-nil, expect errors.Is match
		wantOutput         bool  // if true, pass a writer and check it received data
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
			wantResultContains: []string{"Changes:", "filtering logic"},
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

			if tc.wantResultContains != nil {
				for _, s := range tc.wantResultContains {
					if !strings.Contains(parsed.result, s) {
						t.Errorf("expected result to contain %q, got %q", s, parsed.result)
					}
				}
			} else if parsed.result != tc.wantResult {
				t.Errorf("expected result %q, got %q", tc.wantResult, parsed.result)
			}

			if tc.wantOutput && output.Len() == 0 {
				t.Fatal("expected output to be written")
			}
		})
	}
}

func TestGemini_Review_Integration(t *testing.T) {
	skipIfWindows(t)

	tests := []struct {
		name            string
		script          string
		wantResult      string
		wantErrIs       error
		wantErrContains string
	}{
		{
			name: "PlainTextError",
			script: `#!/bin/sh
echo "Plain text review output"
echo "No issues found."
`,
			wantErrIs:       errNoStreamJSON,
			wantErrContains: "stream-json",
		},
		{
			name: "PlainTextErrorWithStderr",
			script: `#!/bin/sh
echo "Plain text review output"
echo "Some stderr message" >&2
`,
			wantErrContains: "Some stderr message",
		},
		{
			name: "LargeStderrTruncation",
			script: `#!/bin/sh
echo "Plain text"
yes "This is a long stderr line that will contribute to the total size" | head -n 200 >&2
`,
			wantErrContains: "... (truncated)",
		},
		{
			name: "StreamJSON_Success",
			script: `#!/bin/sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"result","result":"Review complete. All good!"}'
`,
			wantResult: "Review complete. All good!",
		},
		{
			name: "StreamJSONNoResult",
			script: `#!/bin/sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"tool","name":"Read","input":{"path":"foo.go"}}'
echo '{"type":"tool_result","content":"file contents here"}'
`,
			wantResult: "No review output generated",
		},
		{
			name: "IOError",
			script: `#!/bin/sh
echo "Error message" >&2
exit 1
`,
			wantErrContains: "gemini failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scriptPath := writeTempCommand(t, tc.script)
			a := NewGeminiAgent(scriptPath)
			var output bytes.Buffer

			res, err := a.Review(context.Background(), t.TempDir(), "sha", "prompt", &output)

			if tc.wantErrIs != nil || tc.wantErrContains != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("expected error type %v, got %v", tc.wantErrIs, err)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("expected error to contain %q, got %q", tc.wantErrContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res != tc.wantResult {
				t.Errorf("expected result %q, got %q", tc.wantResult, res)
			}
		})
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
