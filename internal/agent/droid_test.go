package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDroidBuildArgsAgenticMode(t *testing.T) {
	a := NewDroidAgent("droid")

	// Test non-agentic mode (--auto low)
	args := a.buildArgs(false)
	assertContainsArg(t, args, "low")
	assertNotContainsArg(t, args, "medium")

	// Test agentic mode (--auto medium)
	args = a.buildArgs(true)
	assertContainsArg(t, args, "medium")
}

func TestDroidBuildArgsReasoningEffort(t *testing.T) {
	// Test thorough reasoning
	a := NewDroidAgent("droid").WithReasoning(ReasoningThorough).(*DroidAgent)
	args := a.buildArgs(false)
	assertContainsArg(t, args, "--reasoning-effort")
	assertContainsArg(t, args, "high")

	// Test fast reasoning
	a = NewDroidAgent("droid").WithReasoning(ReasoningFast).(*DroidAgent)
	args = a.buildArgs(false)
	assertContainsArg(t, args, "--reasoning-effort")
	assertContainsArg(t, args, "low")

	// Test standard reasoning (no flag)
	a = NewDroidAgent("droid").WithReasoning(ReasoningStandard).(*DroidAgent)
	args = a.buildArgs(false)
	assertNotContainsArg(t, args, "--reasoning-effort")
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
	tmpDir := t.TempDir()
	progressFile := filepath.Join(tmpDir, "progress.txt")

	a := newMockDroidAgent(t, "#!/bin/sh\necho \"Processing...\" >&2\necho \"Done\"\n")

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

func TestDroidReviewAgenticModeFromGlobal(t *testing.T) {
	withUnsafeAgents(t, true)

	tmpDir := t.TempDir()
	argsFile := filepath.Join(tmpDir, "args.txt")
	t.Setenv("ARGS_FILE", argsFile)

	a := newMockDroidAgent(t, "#!/bin/sh\necho \"$@\" > \"$ARGS_FILE\"\necho \"result\"\n")
	if _, err := a.Review(context.Background(), tmpDir, "deadbeef", "prompt", nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(args), "medium") {
		t.Fatalf("expected '--auto medium' in args when global unsafe enabled, got %s", strings.TrimSpace(string(args)))
	}
}
