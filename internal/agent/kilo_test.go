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

func TestKiloUsesJSONFormat(t *testing.T) {
	t.Parallel()
	a := NewKiloAgent("kilo")
	cl := a.CommandLine()
	assertContains(t, cl, "--format json")
}

func TestKiloReviewStderrOnExitZero(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	// Kilo exits 0 but writes error to stderr and produces no stdout.
	// The error must be surfaced, not hidden behind "No review output".
	mock := mockAgentCLI(t, MockCLIOpts{
		StderrLines: []string{"Error: Model not found: z-ai/glm-5:free"},
		ExitCode:    0,
	})

	a := NewKiloAgent(mock.CmdPath)
	_, err := a.Review(
		context.Background(), t.TempDir(), "HEAD", "prompt", nil,
	)
	if err == nil {
		t.Fatal("expected error when kilo writes to stderr with no stdout")
	}
	assertContains(t, err.Error(), "Model not found")
}

func TestKiloReviewNonZeroExitSurfacesStderr(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	mock := mockAgentCLI(t, MockCLIOpts{
		StderrLines: []string{"Error: Model not found: z-ai/glm-5:free"},
		ExitCode:    1,
	})

	a := NewKiloAgent(mock.CmdPath)
	_, err := a.Review(
		context.Background(), t.TempDir(), "HEAD", "prompt", nil,
	)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	assertContains(t, err.Error(), "Model not found")
}

func TestKiloReviewStderrStripsANSI(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	// Kilo outputs ANSI-colored errors; verify they get stripped.
	mock := mockAgentCLI(t, MockCLIOpts{
		StderrLines: []string{
			"\x1b[0m\x1b[91m\x1b[0m\x1b[1mError: \x1b[0m\x1b[0mModel not found",
		},
		ExitCode: 1,
	})

	a := NewKiloAgent(mock.CmdPath)
	_, err := a.Review(
		context.Background(), t.TempDir(), "HEAD", "prompt", nil,
	)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	assertContains(t, err.Error(), "Error: Model not found")
	assertNotContains(t, err.Error(), "\x1b[")
}

func TestKiloReviewNonZeroExitFallsBackToStdout(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	// Some CLIs print errors to stdout instead of stderr.
	// When stderr is empty, the raw stdout should be included
	// in the error diagnostics.
	mock := mockAgentCLI(t, MockCLIOpts{
		StdoutLines: []string{"fatal: something went wrong"},
		ExitCode:    1,
	})

	a := NewKiloAgent(mock.CmdPath)
	_, err := a.Review(
		context.Background(), t.TempDir(), "HEAD", "prompt", nil,
	)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	assertContains(t, err.Error(), "something went wrong")
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
