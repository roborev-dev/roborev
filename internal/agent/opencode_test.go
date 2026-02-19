package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestFilterOpencodeToolCallLines(t *testing.T) {
	t.Parallel()
	readCall := makeToolCallJSON("read", map[string]any{"path": "/foo"})
	editCall := makeToolCallJSON("edit", map[string]any{})

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "only tool-call lines",
			input:    strings.Join([]string{readCall, editCall}, "\n"),
			expected: "",
		},
		{
			name:     "only normal text",
			input:    "**Review:** No issues.\nDone.",
			expected: "**Review:** No issues.\nDone.",
		},
		{
			name:     "mixed",
			input:    strings.Join([]string{makeToolCallJSON("read", map[string]any{}), "Real text", makeToolCallJSON("edit", map[string]any{})}, "\n"),
			expected: "Real text",
		},
		{
			name:     "empty",
			input:    "",
			expected: "",
		},
		{
			name:     "only newlines",
			input:    "\n\n",
			expected: "",
		},
		{
			name:     "JSON without arguments",
			input:    `{"name":"foo"}`,
			expected: `{"name":"foo"}`,
		},
		{
			name:     "JSON without name",
			input:    `{"arguments":{}}`,
			expected: `{"arguments":{}}`,
		},
		{
			name:     "JSON with name and arguments plus extra keys preserved",
			input:    `{"name":"example","arguments":{"foo":"bar"},"description":"This is a JSON example"}`,
			expected: `{"name":"example","arguments":{"foo":"bar"},"description":"This is a JSON example"}`,
		},
		{
			name:     "leading indentation preserved",
			input:    "  indented line\n    more indented",
			expected: "  indented line\n    more indented",
		},
		{
			name:     "code block with JSON example preserved",
			input:    "Here's an example:\n```json\n{\"name\":\"test\",\"arguments\":{},\"extra\":true}\n```",
			expected: "Here's an example:\n```json\n{\"name\":\"test\",\"arguments\":{},\"extra\":true}\n```",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := filterOpencodeToolCallLines(tt.input)
			if got != tt.expected {
				t.Errorf("filterOpencodeToolCallLines(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestOpenCodeModelFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		model        string
		wantModel    bool // true = --model should appear
		wantContains string
	}{
		{
			name:      "no model omits flag",
			model:     "",
			wantModel: false,
		},
		{
			name:         "explicit model includes flag",
			model:        "anthropic/claude-sonnet-4-20250514",
			wantModel:    true,
			wantContains: "anthropic/claude-sonnet-4-20250514",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewOpenCodeAgent("opencode")
			a.Model = tt.model
			cl := a.CommandLine()
			if tt.wantModel {
				assertContains(t, cl, "--model")
				assertContains(t, cl, tt.wantContains)
			} else {
				assertNotContains(t, cl, "--model")
			}
		})
	}
}

func TestOpenCodeReviewModelFlag(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)
	tests := []struct {
		name      string
		model     string
		wantFlag  bool
		wantModel string
	}{
		{
			name:     "no model omits --model from args",
			model:    "",
			wantFlag: false,
		},
		{
			name:      "explicit model passes --model to subprocess",
			model:     "openai/gpt-4o",
			wantFlag:  true,
			wantModel: "openai/gpt-4o",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, args, _ := runMockReview(t, tt.model, "review this", nil)
			args = strings.TrimSpace(args)

			if tt.wantFlag {
				assertContains(t, args, "--model")
				assertContains(t, args, tt.wantModel)
			} else {
				assertNotContains(t, args, "--model")
			}
		})
	}
}

func TestOpenCodeReviewPipesPromptViaStdin(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	prompt := "Review this commit carefully"
	_, args, stdin := runMockReview(t, "", prompt, nil)

	// Prompt must be in stdin
	if strings.TrimSpace(stdin) != prompt {
		t.Errorf("stdin content = %q, want %q", stdin, prompt)
	}

	// Prompt must not be in argv
	assertNotContains(t, args, prompt)
}

func TestOpenCodeReviewFiltersToolCallLines(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeToolCallJSON("read", map[string]any{"path": "/foo"}),
		"**Review:** Fix the typo.",
		makeToolCallJSON("edit", map[string]any{}),
		"Done.",
	}

	result, _, _ := runMockReview(t, "", "prompt", stdoutLines)

	assertContains(t, result, "**Review:**")
	assertContains(t, result, "Done.")
	assertNotContains(t, result, `"name":"read"`)
}

func runMockReview(t *testing.T, model, prompt string, stdoutLines []string) (output, args, stdin string) {
	t.Helper()

	if stdoutLines == nil {
		stdoutLines = []string{"ok"}
	}

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines:  stdoutLines,
	})

	a := NewOpenCodeAgent(mock.CmdPath)
	if model != "" {
		a.Model = model
	}

	out, err := a.Review(context.Background(), t.TempDir(), "HEAD", prompt, nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	argsBytes := readFileOrFatal(t, mock.ArgsFile)
	stdinBytes := readFileOrFatal(t, mock.StdinFile)

	return out, string(argsBytes), string(stdinBytes)
}

func readFileOrFatal(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file %s: %v", path, err)
	}
	return data
}
