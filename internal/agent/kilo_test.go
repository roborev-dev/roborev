package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestKiloModelFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		model        string
		wantContains string
	}{
		{
			name:  "no model omits flag",
			model: "",
		},
		{
			name:         "explicit model includes flag",
			model:        "anthropic/claude-sonnet-4-20250514",
			wantContains: "anthropic/claude-sonnet-4-20250514",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewKiloAgent("kilo")
			a.Model = tt.model
			cl := a.CommandLine()
			if tt.wantContains != "" {
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
		wantModel string
	}{
		{
			name:  "no model omits --model from args",
			model: "",
		},
		{
			name:      "explicit model passes --model to subprocess",
			model:     "openai/gpt-4o",
			wantModel: "openai/gpt-4o",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, args, _ := runKiloMockReview(t, tt.model, "review this", nil)
			args = strings.TrimSpace(args)
			if tt.wantModel != "" {
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

func TestKiloReviewExtractsTextFromJSONL(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		kiloTextEvent("**Review:** Fix the typo."),
		kiloTextEvent("Done."),
	}

	result, _, _ := runKiloMockReview(t, "", "prompt", stdoutLines)
	assertContains(t, result, "**Review:** Fix the typo.")
	assertContains(t, result, "Done.")
}

func TestKiloReviewStripsANSIFromJSONL(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		kiloTextEvent("\x1b[31mred\x1b[0m text"),
	}

	result, _, _ := runKiloMockReview(t, "", "prompt", stdoutLines)
	assertContains(t, result, "red text")
	assertNotContains(t, result, "\x1b[")
}

func TestKiloAgenticAutoFlag(t *testing.T) {
	tests := []struct {
		name     string
		agentic  bool
		wantFlag bool
	}{
		{"enabled includes flag", true, true},
		{"disabled omits flag", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skipIfWindows(t)
			withUnsafeAgents(t, false)
			a := NewKiloAgent("kilo").WithAgentic(tt.agentic).(*KiloAgent)
			cl := a.CommandLine()
			if tt.wantFlag {
				assertContains(t, cl, "--auto")
			} else {
				assertNotContains(t, cl, "--auto")
			}
		})
	}
}

func TestKiloVariantFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		reasoning ReasoningLevel
		wantValue string
	}{
		{name: "thorough maps to high", reasoning: ReasoningThorough, wantValue: "high"},
		{name: "fast maps to minimal", reasoning: ReasoningFast, wantValue: "minimal"},
		{name: "standard omits variant", reasoning: ReasoningStandard},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewKiloAgent("kilo").WithReasoning(tt.reasoning).(*KiloAgent)
			cl := a.CommandLine()
			if tt.wantValue != "" {
				assertContains(t, cl, "--variant")
				assertContains(t, cl, tt.wantValue)
			} else {
				assertNotContains(t, cl, "--variant")
			}
		})
	}
}

func TestKiloUsesJSONFormat(t *testing.T) {
	t.Parallel()
	a := NewKiloAgent("kilo")
	cl := a.CommandLine()
	assertContains(t, cl, "--format json")
}

func TestKiloReviewDiagnostics(t *testing.T) {
	t.Parallel()
	stepEvent := `{"type":"step","part":{"type":"tool","name":"read"}}`
	tests := []struct {
		name       string
		opts       MockCLIOpts
		wantErr    string
		wantResult string
	}{
		{
			name:    "stderr on exit zero",
			opts:    MockCLIOpts{StderrLines: []string{"Error: Model not found: z-ai/glm-5:free"}, ExitCode: 0},
			wantErr: "Model not found",
		},
		{
			name:    "non-zero exit surfaces stderr",
			opts:    MockCLIOpts{StderrLines: []string{"Error: Model not found: z-ai/glm-5:free"}, ExitCode: 1},
			wantErr: "Model not found",
		},
		{
			name: "stderr strips ANSI",
			opts: MockCLIOpts{
				StderrLines: []string{"\x1b[0m\x1b[91m\x1b[0m\x1b[1mError: \x1b[0m\x1b[0mModel not found"},
				ExitCode:    1,
			},
			wantErr: "Error: Model not found",
		},
		{
			name:    "non-zero exit falls back to stdout",
			opts:    MockCLIOpts{StdoutLines: []string{"fatal: something went wrong"}, ExitCode: 1},
			wantErr: "something went wrong",
		},
		{
			name:    "exit zero non-JSON stdout",
			opts:    MockCLIOpts{StdoutLines: []string{"Error: invalid configuration"}, ExitCode: 0},
			wantErr: "invalid configuration",
		},
		{
			name:       "exit zero JSON only no text",
			opts:       MockCLIOpts{StdoutLines: []string{stepEvent}, ExitCode: 0},
			wantResult: "No review output generated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			skipIfWindows(t)
			mock := mockAgentCLI(t, tt.opts)
			a := NewKiloAgent(mock.CmdPath)
			result, err := a.Review(
				context.Background(), t.TempDir(), "HEAD", "prompt", nil,
			)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				assertContains(t, err.Error(), tt.wantErr)
				if strings.Contains(tt.name, "ANSI") {
					assertNotContains(t, err.Error(), "\x1b[")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				assertEqual(t, result, tt.wantResult)
			}
		})
	}
}

// kiloTextEvent returns a JSONL line representing a text event
// in the opencode/kilo --format json envelope.
func kiloTextEvent(text string) string {
	part := map[string]string{"type": "text", "text": text}
	partJSON, err := json.Marshal(part)
	if err != nil {
		panic(fmt.Sprintf("kiloTextEvent: %v", err))
	}
	ev := map[string]json.RawMessage{
		"type": json.RawMessage(`"text"`),
		"part": partJSON,
	}
	b, err := json.Marshal(ev)
	if err != nil {
		panic(fmt.Sprintf("kiloTextEvent: %v", err))
	}
	return string(b)
}

func runKiloMockReview(
	t *testing.T, model, prompt string, stdoutLines []string,
) (output, args, stdin string) {
	t.Helper()

	if stdoutLines == nil {
		stdoutLines = []string{kiloTextEvent("ok")}
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

	out, err := a.Review(
		context.Background(), t.TempDir(), "HEAD", prompt, nil,
	)
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
