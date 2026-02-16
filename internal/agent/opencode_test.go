package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestFilterOpencodeToolCallLines(t *testing.T) {
	readCall := makeToolCallJSON("read", map[string]interface{}{"path": "/foo"})
	editCall := makeToolCallJSON("edit", map[string]interface{}{})

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "only tool-call lines",
			input:    readCall + "\n" + editCall,
			expected: "",
		},
		{
			name:     "only normal text",
			input:    "**Review:** No issues.\nDone.",
			expected: "**Review:** No issues.\nDone.",
		},
		{
			name:     "mixed",
			input:    makeToolCallJSON("read", map[string]interface{}{}) + "\n" + "Real text\n" + makeToolCallJSON("edit", map[string]interface{}{}),
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
			got := filterOpencodeToolCallLines(tt.input)
			if got != tt.expected {
				t.Errorf("filterOpencodeToolCallLines(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestOpenCodeModelFlag(t *testing.T) {
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
			mock := mockAgentCLI(t, MockCLIOpts{
				CaptureArgs: true,
				StdoutLines: []string{"ok"},
			})
			a := NewOpenCodeAgent(mock.CmdPath)
			a.Model = tt.model
			_, err := a.Review(
				context.Background(), t.TempDir(),
				"abc123", "review this", nil,
			)
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			raw, err := os.ReadFile(mock.ArgsFile)
			if err != nil {
				t.Fatalf("read captured args: %v", err)
			}
			args := strings.TrimSpace(string(raw))
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
	skipIfWindows(t)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines:  []string{"ok"},
	})

	a := NewOpenCodeAgent(mock.CmdPath)
	prompt := "Review this commit carefully"
	_, err := a.Review(
		context.Background(), t.TempDir(), "HEAD", prompt, nil,
	)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	stdin, err := os.ReadFile(mock.StdinFile)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	if strings.TrimSpace(string(stdin)) != prompt {
		t.Errorf("stdin = %q, want %q", string(stdin), prompt)
	}

	args, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("read args capture: %v", err)
	}
	if strings.Contains(string(args), prompt) {
		t.Errorf("prompt leaked into argv: %s", string(args))
	}
}

func TestOpenCodeReviewFiltersToolCallLines(t *testing.T) {
	skipIfWindows(t)
	script := NewScriptBuilder().
		AddToolCall("read", map[string]interface{}{"path": "/foo"}).
		AddOutput("**Review:** Fix the typo.").
		AddToolCall("edit", map[string]interface{}{}).
		AddOutput("Done.").
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewOpenCodeAgent(cmdPath)
	result, err := a.Review(context.Background(), t.TempDir(), "head", "prompt", nil)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	assertContains(t, result, "**Review:**")
	assertContains(t, result, "Done.")
	assertNotContains(t, result, `"name":"read"`)
}
