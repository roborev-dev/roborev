package agent

import (
	"context"
	"strings"
	"testing"
)

func TestCursorBuildArgs(t *testing.T) {
	tests := []struct {
		name        string
		agentic     bool
		model       string
		wantArgs    []string
		excludeArgs []string
	}{
		{
			name:    "Review Mode (Default)",
			agentic: false,
			wantArgs: []string{
				"-p",
				"--output-format", "stream-json",
				"--model", "auto",
				"--mode", "plan",
			},
			excludeArgs: []string{"--force"},
		},
		{
			name:    "Agentic Mode",
			agentic: true,
			wantArgs: []string{
				"--force",
			},
			excludeArgs: []string{"--mode", "plan"},
		},
		{
			name:     "Custom Model",
			agentic:  false,
			model:    "gpt-5.2-codex-high",
			wantArgs: []string{"--model", "gpt-5.2-codex-high"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var agent Agent = NewCursorAgent("agent")
			if tt.model != "" {
				agent = agent.WithModel(tt.model)
			}
			cursor := agent.(*CursorAgent)

			args := cursor.buildArgs(tt.agentic)

			for _, want := range tt.wantArgs {
				assertContainsArg(t, args, want)
			}
			for _, exclude := range tt.excludeArgs {
				assertNotContainsArg(t, args, exclude)
			}
		})
	}
}
func setupMockCursorAgent(t *testing.T, opts MockCLIOpts) (*CursorAgent, *MockCLIResult) {
	t.Helper()
	skipIfWindows(t)
	mock := mockAgentCLI(t, opts)
	return NewCursorAgent(mock.CmdPath), mock
}

func TestCursorReviewPassesModelFlag(t *testing.T) {
	a, mock := setupMockCursorAgent(t, MockCLIOpts{
		CaptureArgs: true,
		StdoutLines: []string{`{"type":"result","result":"ok"}`},
	})

	b := a.WithModel("test-model")
	cursor, ok := b.(*CursorAgent)
	if !ok {
		t.Fatalf("expected *CursorAgent, got %T", b)
	}

	_, err := cursor.Review(context.Background(), t.TempDir(), "head", "test prompt", nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	args := readMockArgs(t, mock.ArgsFile)
	assertContainsArg(t, args, "--model")
	assertContainsArg(t, args, "test-model")
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
			cursor := NewCursorAgent("dummy")
			res, err := cursor.parseStreamJSON(strings.NewReader(tt.input), nil)
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
	a := NewCursorAgent("agent").
		WithModel("m1").
		WithReasoning(ReasoningThorough).
		WithAgentic(true)

	cursor := a.(*CursorAgent)

	if cursor.Command != "agent" {
		t.Errorf("expected Command 'agent', got %q", cursor.Command)
	}
	if cursor.Reasoning != ReasoningThorough {
		t.Errorf("expected Reasoning %v, got %v", ReasoningThorough, cursor.Reasoning)
	}

	args := cursor.buildArgs(cursor.Agentic)

	assertContainsArg(t, args, "--model")
	assertContainsArg(t, args, "m1")
	assertContainsArg(t, args, "--force") // Assumes Agentic maps to --force
}

func TestCursorReviewPipesPromptViaStdin(t *testing.T) {
	a, mock := setupMockCursorAgent(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines: []string{
			`{"type":"result","result":"ok"}`,
		},
	})

	prompt := "Review this commit carefully"
	_, err := a.Review(
		context.Background(), t.TempDir(), "HEAD", prompt, nil,
	)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Prompt must be in stdin
	assertFileContent(t, mock.StdinFile, prompt)

	// Prompt must not be in argv
	assertFileNotContains(t, mock.ArgsFile, prompt)
}

func TestCursorReviewEmptyOutput(t *testing.T) {
	a, _ := setupMockCursorAgent(t, MockCLIOpts{
		StdoutLines: []string{`{"type":"system","subtype":"init"}`},
	})

	result, err := a.Review(
		context.Background(), t.TempDir(), "abc123", "review this", nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "No review output generated" {
		t.Errorf("expected %q, got %q", "No review output generated", result)
	}
}

func TestCursorReviewErrorResult(t *testing.T) {
	a, _ := setupMockCursorAgent(t, MockCLIOpts{
		StdoutLines: []string{
			`{"type":"system","subtype":"init"}`,
			`{"type":"result","is_error":true,"result":"","error":{"message":"Invalid API key"}}`,
		},
	})

	_, err := a.Review(
		context.Background(), t.TempDir(), "abc123", "review this", nil,
	)
	assertErrorContains(t, err, "Invalid API key")
}
func TestCursorReviewErrorResultNonZeroExit(t *testing.T) {
	a, _ := setupMockCursorAgent(t, MockCLIOpts{
		StdoutLines: []string{
			`{"type":"system","subtype":"init"}`,
			`{"type":"result","is_error":true,"result":"","error":{"message":"quota exceeded"}}`,
		},
		ExitCode: 1,
	})

	_, err := a.Review(
		context.Background(), t.TempDir(), "abc123", "review this", nil,
	)
	assertErrorContains(t, err, "quota exceeded")
	assertErrorContains(t, err, "failed")
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
