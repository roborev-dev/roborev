package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestKiloModelFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		model        string
		wantModel    bool
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
			a := NewKiloAgent("kilo")
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

func TestKiloReviewModelFlag(t *testing.T) {
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
			_, args, _ := runKiloMockReview(t, tt.model, "review this", nil)
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

func TestKiloReviewPipesPromptViaStdin(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	prompt := "Review this commit carefully"
	_, args, stdin := runKiloMockReview(t, "", prompt, nil)

	if strings.TrimSpace(stdin) != prompt {
		t.Errorf("stdin content = %q, want %q", stdin, prompt)
	}
	assertNotContains(t, args, prompt)
}

func TestKiloReviewFiltersToolCallLines(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeToolCallJSON("read", map[string]any{"path": "/foo"}),
		"**Review:** Fix the typo.",
		makeToolCallJSON("edit", map[string]any{}),
		"Done.",
	}

	result, _, _ := runKiloMockReview(t, "", "prompt", stdoutLines)
	assertContains(t, result, "**Review:**")
	assertContains(t, result, "Done.")
	assertNotContains(t, result, `"name":"read"`)
}

func TestKiloAgenticAutoFlag(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	withUnsafeAgents(t, false)
	a := NewKiloAgent("kilo").WithAgentic(true).(*KiloAgent)
	cl := a.CommandLine()
	assertContains(t, cl, "--auto")

	b := NewKiloAgent("kilo").WithAgentic(false).(*KiloAgent)
	cl2 := b.CommandLine()
	assertNotContains(t, cl2, "--auto")
}

func TestKiloVariantFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		reasoning   ReasoningLevel
		wantVariant bool
		wantValue   string
	}{
		{name: "thorough maps to high", reasoning: ReasoningThorough, wantVariant: true, wantValue: "high"},
		{name: "fast maps to minimal", reasoning: ReasoningFast, wantVariant: true, wantValue: "minimal"},
		{name: "standard omits variant", reasoning: ReasoningStandard, wantVariant: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewKiloAgent("kilo").WithReasoning(tt.reasoning).(*KiloAgent)
			cl := a.CommandLine()
			if tt.wantVariant {
				assertContains(t, cl, "--variant")
				assertContains(t, cl, tt.wantValue)
			} else {
				assertNotContains(t, cl, "--variant")
			}
		})
	}
}

func runKiloMockReview(t *testing.T, model, prompt string, stdoutLines []string) (output, args, stdin string) {
	t.Helper()

	if stdoutLines == nil {
		stdoutLines = []string{"ok"}
	}

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines:  stdoutLines,
	})

	a := NewKiloAgent(mock.CmdPath)
	if model != "" {
		a.Model = model
	}

	out, err := a.Review(context.Background(), t.TempDir(), "HEAD", prompt, nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	argsBytes, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("failed to read args file: %v", err)
	}
	stdinBytes, err := os.ReadFile(mock.StdinFile)
	if err != nil {
		t.Fatalf("failed to read stdin file: %v", err)
	}

	return out, string(argsBytes), string(stdinBytes)
}
