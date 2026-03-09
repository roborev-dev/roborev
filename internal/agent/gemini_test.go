package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateStderr(t *testing.T) {
	assert := assert.New(t)

	// Short string - no truncation
	short := "short stderr"
	assert.Equal(short, truncateStderr(short))

	// Exactly at limit - no truncation
	exact := strings.Repeat("x", maxStderrLen)
	assert.Equal(exact, truncateStderr(exact))

	// Over limit - should truncate
	over := strings.Repeat("x", maxStderrLen+100)
	got := truncateStderr(over)
	assert.True(strings.HasSuffix(got, "... (truncated)"), "expected truncation suffix")
	assert.Len(got, maxStderrLen+len("... (truncated)"))
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
			assert := assert.New(t)

			a := NewGeminiAgent("gemini")
			args := a.buildArgs(tc.agentic)

			// Check standalone boolean flags
			for _, flag := range tc.wantFlags {
				assert.Contains(args, flag, "missing flag %q", flag)
			}

			// Check flag-value pairs by exact next token
			for flag, val := range tc.wantArgPairs {
				assertFlagValue(t, args, flag, val)
			}

			// Check absence of specific tokens
			for _, unwanted := range tc.unwantedArgs {
				assert.NotContains(args, unwanted, "unexpected token %q", unwanted)
			}
		})
	}
}

func assertFlagValue(t *testing.T, args []string, flag, expectedVal string) {
	t.Helper()
	assert := assert.New(t)
	require := require.New(t)

	count := 0
	for _, a := range args {
		if a == flag {
			count++
		}
	}
	require.Equal(1, count, "flag %q count in args %v", flag, args)

	idx := slices.Index(args, flag)
	require.Less(idx+1, len(args), "flag %q is last arg", flag)
	assert.Equal(expectedVal, args[idx+1], "flag %q value", flag)
}

func TestGeminiParseStreamJSON(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		assertResult func(t *testing.T, res string)
		wantErr      error // if non-nil, expect errors.Is match
		wantOutput   bool  // if true, pass a writer and check it received data
	}{
		{
			name: "ResultEvent",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"Working on it..."}}
{"type":"result","result":"Done! Review complete."}
`,
			assertResult: func(t *testing.T, res string) {
				assert.Equal(t, "Done! Review complete.", res)
			},
		},
		{
			name: "GeminiMessageFormat",
			input: `{"type":"message","timestamp":"2026-01-19T17:49:13.445Z","role":"assistant","content":"Changes:\n- Created file.ts","delta":true}
{"type":"message","timestamp":"2026-01-19T17:49:13.447Z","role":"assistant","content":" with filtering logic.","delta":true}
{"type":"result","timestamp":"2026-01-19T17:49:13.519Z","status":"success","stats":{"total_tokens":1000}}
`,
			assertResult: func(t *testing.T, res string) {
				for _, s := range []string{"Changes:", "filtering logic"} {
					assert.Contains(t, res, s)
				}
			},
		},
		{
			name: "AssistantFallback",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"First message"}}
{"type":"assistant","message":{"content":"Second message"}}
`,
			assertResult: func(t *testing.T, res string) {
				want := "First message\nSecond message"
				assert.Equal(t, want, res)
			},
		},
		{
			name: "AssistantFallbackDropsNarrationBeforeToolUse",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"I am checking the relevant files first."}}
{"type":"tool","name":"Read"}
{"type":"assistant","message":{"content":"## Review Findings\n- **Severity**: Low; **Problem**: Final finding."}}
`,
			assertResult: func(t *testing.T, res string) {
				want := "## Review Findings\n- **Severity**: Low; **Problem**: Final finding."
				assert.Equal(t, want, res)
			},
		},
		{
			name: "AssistantFallbackPrefersFinalPostToolSegment",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"## Review Findings\n- **Severity**: Low; **Problem**: Earlier provisional finding."}}
{"type":"tool","name":"Read"}
{"type":"assistant","message":{"content":"## Review Findings\n- **Severity**: Medium; **Problem**: Final persisted finding."}}
`,
			assertResult: func(t *testing.T, res string) {
				want := "## Review Findings\n- **Severity**: Medium; **Problem**: Final persisted finding."
				assert.Equal(t, want, res)
			},
		},
		{
			name: "AssistantFallbackDropsNarrationBeforeToolUse",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"I am checking the relevant files first."}}
{"type":"tool","name":"Read"}
{"type":"assistant","message":{"content":"## Review Findings\n- **Severity**: Low; **Problem**: Final finding."}}
`,
			assertResult: func(t *testing.T, res string) {
				want := "## Review Findings\n- **Severity**: Low; **Problem**: Final finding."
				assert.Equal(t, want, res)
			},
		},
		{
			name: "AssistantFallbackPrefersFinalPostToolSegment",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"## Review Findings\n- **Severity**: Low; **Problem**: Earlier provisional finding."}}
{"type":"tool","name":"Read"}
{"type":"assistant","message":{"content":"## Review Findings\n- **Severity**: Medium; **Problem**: Final persisted finding."}}
`,
			assertResult: func(t *testing.T, res string) {
				want := "## Review Findings\n- **Severity**: Medium; **Problem**: Final persisted finding."
				assert.Equal(t, want, res)
			},
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
			assertResult: func(t *testing.T, res string) {
				assert.Equal(t, "Done", res)
			},
			wantOutput: true,
		},
		{
			name: "EmptyResult",
			input: `{"type":"system","subtype":"init"}
{"type":"tool","name":"Read"}
`,
			assertResult: func(t *testing.T, res string) {
				assert.Empty(t, res)
			},
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
			assert := assert.New(t)
			require := require.New(t)

			var sw *syncWriter
			var output bytes.Buffer
			if tc.wantOutput {
				sw = newSyncWriter(&output)
			}

			parsed, err := a.parseStreamJSON(strings.NewReader(tc.input), sw)

			if tc.wantErr != nil {
				require.Error(err)
				require.ErrorIs(err, tc.wantErr)
				return
			}
			require.NoError(err)

			if tc.assertResult != nil {
				tc.assertResult(t, parsed.result)
			}

			if tc.wantOutput {
				assert.NotZero(output.Len(), "expected output to be written")
			}
		})
	}
}

func TestGemini_Review_Integration(t *testing.T) {
	skipIfWindows(t)

	tests := []struct {
		name       string
		script     string
		wantResult string
		checkErr   func(t *testing.T, err error)
	}{
		{
			name: "PlainTextError",
			script: `#!/bin/sh
echo "Plain text review output"
echo "No issues found."
`,
			checkErr: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "stream-json")
				require.ErrorIs(t, err, errNoStreamJSON)
			},
		},
		{
			name: "PlainTextErrorWithStderr",
			script: `#!/bin/sh
echo "Plain text review output"
echo "Some stderr message" >&2
`,
			checkErr: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "Some stderr message")
			},
		},
		{
			name: "LargeStderrTruncation",
			script: `#!/bin/sh
echo "Plain text"
yes "This is a long stderr line that will contribute to the total size" | head -n 200 >&2
`,
			checkErr: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "... (truncated)")
			},
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
			checkErr: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "gemini failed")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)

			scriptPath := writeTempCommand(t, tc.script)
			a := NewGeminiAgent(scriptPath)
			var output bytes.Buffer

			res, err := a.Review(context.Background(), t.TempDir(), "sha", "prompt", &output)

			if tc.checkErr != nil {
				tc.checkErr(t, err)
				return
			}
			require.NoError(err)
			assert.Equal(tc.wantResult, res)
		})
	}
}

func TestGeminiReview_PromptDeliveredViaStdin(t *testing.T) {
	assert := assert.New(t)

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
	require.NoError(t, err, "Review failed")

	// Verify the prompt was received
	receivedPrompt, err := os.ReadFile(stdinFile)
	require.NoError(t, err, "failed to read prompt file")
	assert.Equal(expectedPrompt, string(receivedPrompt), "prompt not delivered correctly")
}
