package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDroidBuildArgs(t *testing.T) {
	tests := []struct {
		name      string
		agentic   bool
		reasoning ReasoningLevel
		wantArgs  []string
		dontWant  []string
	}{
		{
			name:     "Non-agentic default",
			agentic:  false,
			wantArgs: []string{"--auto", "low"},
			dontWant: []string{"medium"},
		},
		{
			name:     "Agentic mode",
			agentic:  true,
			wantArgs: []string{"--auto", "medium"},
		},
		{
			name:      "Reasoning Thorough",
			reasoning: ReasoningThorough,
			wantArgs:  []string{"--reasoning-effort", "high"},
		},
		{
			name:      "Reasoning Fast",
			reasoning: ReasoningFast,
			wantArgs:  []string{"--reasoning-effort", "low"},
		},
		{
			name:      "Reasoning Standard",
			reasoning: ReasoningStandard,
			dontWant:  []string{"--reasoning-effort"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewDroidAgent("droid")
			if tt.reasoning != "" {
				a = a.WithReasoning(tt.reasoning).(*DroidAgent)
			}

			args := a.buildArgs(tt.agentic)

			for _, want := range tt.wantArgs {
				assertContainsArg(t, args, want)
			}
			for _, dont := range tt.dontWant {
				assertNotContainsArg(t, args, dont)
			}
		})
	}
}

func TestDroidName(t *testing.T) {
	a := NewDroidAgent("")
	if a.Name() != "droid" {
		t.Fatalf("expected name 'droid', got %s", a.Name())
	}
	if a.CommandName() != "droid" {
		t.Fatalf("expected command name 'droid', got %s", a.CommandName())
	}
}

func TestDroidWithAgentic(t *testing.T) {
	a := NewDroidAgent("droid")
	if a.Agentic {
		t.Fatal("expected non-agentic by default")
	}

	a2 := a.WithAgentic(true).(*DroidAgent)
	if !a2.Agentic {
		t.Fatal("expected agentic after WithAgentic(true)")
	}
	if a.Agentic {
		t.Fatal("original should be unchanged")
	}
}

func TestDroidReviewSuccess(t *testing.T) {
	outputContent := "Review feedback from Droid"
	script := NewScriptBuilder().AddOutput(outputContent).Build()
	result, err := runReviewScenario(t, script, "review this commit")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !strings.Contains(result, outputContent) {
		t.Fatalf("expected result to contain %q, got %q", outputContent, result)
	}
}

func TestDroidReviewFailure(t *testing.T) {
	script := NewScriptBuilder().AddRaw(`echo "error: something went wrong" >&2`).AddRaw("exit 1").Build()
	_, err := runReviewScenario(t, script, "review this commit")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "droid failed") {
		t.Fatalf("expected 'droid failed' in error, got %v", err)
	}
}

func TestDroidReviewEmptyOutput(t *testing.T) {
	result, err := runReviewScenario(t, NewScriptBuilder().AddRaw("exit 0").Build(), "review this commit")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "No review output generated" {
		t.Fatalf("expected 'No review output generated', got %q", result)
	}
}

func TestDroidReviewWithProgress(t *testing.T) {
	skipIfWindows(t)
	tmpDir := t.TempDir()
	progressFile := filepath.Join(tmpDir, "progress.txt")

	mock := mockAgentCLI(t, MockCLIOpts{
		StderrLines: []string{"Processing..."},
		StdoutLines: []string{"Done"},
	})
	a := NewDroidAgent(mock.CmdPath)

	f, err := os.Create(progressFile)
	if err != nil {
		t.Fatalf("create progress file: %v", err)
	}
	defer f.Close()

	_, err = a.Review(context.Background(), tmpDir, "deadbeef", "review this commit", f)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	progress, _ := os.ReadFile(progressFile)
	if !strings.Contains(string(progress), "Processing") {
		t.Fatalf("expected progress output, got %q", string(progress))
	}
}

func TestDroidReviewPipesPromptViaStdin(t *testing.T) {
	skipIfWindows(t)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines:  []string{"ok"},
	})

	a := NewDroidAgent(mock.CmdPath)
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

func TestDroidReviewAgenticModeFromGlobal(t *testing.T) {
	withUnsafeAgents(t, true)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs: true,
		StdoutLines: []string{"result"},
	})

	a := NewDroidAgent(mock.CmdPath)
	if _, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	args, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(args), "medium") {
		t.Fatalf("expected '--auto medium' in args when global unsafe enabled, got %s", strings.TrimSpace(string(args)))
	}
}
