package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCursorBuildArgs(t *testing.T) {
	a := NewCursorAgent("agent")

	// Non-agentic mode (review): --mode plan, no --force, default model "auto"
	args := a.buildArgs(false)
	assertContainsArg(t, args, "-p")
	assertContainsArg(t, args, "--output-format")
	assertContainsArg(t, args, "stream-json")
	assertContainsArg(t, args, "--model")
	assertContainsArg(t, args, "auto")
	assertContainsArg(t, args, "--mode")
	assertContainsArg(t, args, "plan")
	assertNotContainsArg(t, args, "--force")

	// Agentic mode: --force, no --mode plan
	args = a.buildArgs(true)
	assertContainsArg(t, args, "--force")
	assertNotContainsArg(t, args, "--mode")
	assertNotContainsArg(t, args, "plan")
}

func TestCursorBuildArgsWithModel(t *testing.T) {
	a := NewCursorAgent("agent")
	a = a.WithModel("gpt-5.2-codex-high").(*CursorAgent)

	args := a.buildArgs(false)
	assertContainsArg(t, args, "--model")
	assertContainsArg(t, args, "gpt-5.2-codex-high")
}

func TestCursorReviewPassesModelFlag(t *testing.T) {
	skipIfWindows(t)

	// Create a script that echoes args as a stream-json result
	script := `#!/bin/sh
printf '{"type":"result","result":"args: %s"}\n' "$*"
`
	cmdPath := writeTempCommand(t, script)
	a := NewCursorAgent(cmdPath)
	a = a.WithModel("test-model").(*CursorAgent)

	result, err := a.Review(context.Background(), t.TempDir(), "head", "test prompt", nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	if !strings.Contains(result, "--model") {
		t.Errorf("expected --model in args, got: %q", result)
	}
	if !strings.Contains(result, "test-model") {
		t.Errorf("expected test-model in args, got: %q", result)
	}
}

func TestCursorParseStreamJSON(t *testing.T) {
	// Cursor uses the same stream-json format as Claude Code.
	// Verify that output parsing works via a mock script.
	tests := []struct {
		name           string
		input          string
		expectedResult string
	}{
		{
			name: "ResultEvent",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"Reviewing..."}}
{"type":"result","result":"LGTM"}
`,
			expectedResult: "LGTM",
		},
		{
			name: "AssistantFallback",
			input: `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"First"}}
{"type":"assistant","message":{"content":"Second"}}
`,
			expectedResult: "First\nSecond",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claude := &ClaudeAgent{}
			res, err := claude.parseStreamJSON(strings.NewReader(tt.input), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res != tt.expectedResult {
				t.Errorf("expected %q, got %q", tt.expectedResult, res)
			}
		})
	}
}

func TestCursorWithChaining(t *testing.T) {
	a := NewCursorAgent("agent")

	// Chain WithModel, WithReasoning, WithAgentic
	b := a.WithModel("m1").WithReasoning(ReasoningThorough).WithAgentic(true)
	cursor := b.(*CursorAgent)

	if cursor.Model != "m1" {
		t.Errorf("expected model m1, got %q", cursor.Model)
	}
	if cursor.Reasoning != ReasoningThorough {
		t.Errorf("expected thorough reasoning, got %q", cursor.Reasoning)
	}
	if !cursor.Agentic {
		t.Error("expected agentic true")
	}
	if cursor.Command != "agent" {
		t.Errorf("expected command 'agent', got %q", cursor.Command)
	}
}

func TestCursorReviewPipesPromptViaStdin(t *testing.T) {
	skipIfWindows(t)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines: []string{
			`{"type":"result","result":"ok"}`,
		},
	})

	a := NewCursorAgent(mock.CmdPath)
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

func TestCursorName(t *testing.T) {
	a := NewCursorAgent("")
	if a.Name() != "cursor" {
		t.Errorf("expected 'cursor', got %q", a.Name())
	}
	if a.CommandName() != "agent" {
		t.Errorf("expected 'agent', got %q", a.CommandName())
	}
}
