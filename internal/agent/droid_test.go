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
	args := a.buildArgs("/repo", "/tmp/out", "prompt", false)
	if !containsString(args, "low") {
		t.Fatalf("expected --auto low in non-agentic mode, got %v", args)
	}
	if containsString(args, "medium") {
		t.Fatalf("expected no --auto medium in non-agentic mode, got %v", args)
	}

	// Test agentic mode (--auto medium)
	args = a.buildArgs("/repo", "/tmp/out", "prompt", true)
	if !containsString(args, "medium") {
		t.Fatalf("expected --auto medium in agentic mode, got %v", args)
	}
}

func TestDroidBuildArgsReasoningEffort(t *testing.T) {
	// Test thorough reasoning
	a := NewDroidAgent("droid").WithReasoning(ReasoningThorough).(*DroidAgent)
	args := a.buildArgs("/repo", "/tmp/out", "prompt", false)
	if !containsString(args, "--reasoning-effort") || !containsString(args, "high") {
		t.Fatalf("expected --reasoning-effort high for thorough, got %v", args)
	}

	// Test fast reasoning
	a = NewDroidAgent("droid").WithReasoning(ReasoningFast).(*DroidAgent)
	args = a.buildArgs("/repo", "/tmp/out", "prompt", false)
	if !containsString(args, "--reasoning-effort") || !containsString(args, "low") {
		t.Fatalf("expected --reasoning-effort low for fast, got %v", args)
	}

	// Test standard reasoning (no flag)
	a = NewDroidAgent("droid").WithReasoning(ReasoningStandard).(*DroidAgent)
	args = a.buildArgs("/repo", "/tmp/out", "prompt", false)
	if containsString(args, "--reasoning-effort") {
		t.Fatalf("expected no --reasoning-effort for standard, got %v", args)
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
	tmpDir := t.TempDir()
	outputContent := "Review feedback from Droid"

	// Create a mock droid command that writes to the output file
	cmdPath := writeTempCommand(t, `#!/bin/sh
# Find the -o flag and write to that file
while [ $# -gt 0 ]; do
    case "$1" in
        -o)
            shift
            echo "`+outputContent+`" > "$1"
            exit 0
            ;;
    esac
    shift
done
exit 1
`)

	a := NewDroidAgent(cmdPath)
	result, err := a.Review(context.Background(), tmpDir, "deadbeef", "review this commit", nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !strings.Contains(result, outputContent) {
		t.Fatalf("expected result to contain %q, got %q", outputContent, result)
	}
}

func TestDroidReviewFailure(t *testing.T) {
	tmpDir := t.TempDir()

	cmdPath := writeTempCommand(t, `#!/bin/sh
echo "error: something went wrong" >&2
exit 1
`)

	a := NewDroidAgent(cmdPath)
	_, err := a.Review(context.Background(), tmpDir, "deadbeef", "review this commit", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "droid failed") {
		t.Fatalf("expected 'droid failed' in error, got %v", err)
	}
}

func TestDroidReviewEmptyOutput(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock that creates an empty output file
	cmdPath := writeTempCommand(t, `#!/bin/sh
while [ $# -gt 0 ]; do
    case "$1" in
        -o)
            shift
            touch "$1"
            exit 0
            ;;
    esac
    shift
done
exit 1
`)

	a := NewDroidAgent(cmdPath)
	result, err := a.Review(context.Background(), tmpDir, "deadbeef", "review this commit", nil)
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

	cmdPath := writeTempCommand(t, `#!/bin/sh
echo "Processing..." >&2
while [ $# -gt 0 ]; do
    case "$1" in
        -o)
            shift
            echo "Done" > "$1"
            exit 0
            ;;
    esac
    shift
done
exit 1
`)

	// Create a writer to capture progress
	f, err := os.Create(progressFile)
	if err != nil {
		t.Fatalf("create progress file: %v", err)
	}
	defer f.Close()

	a := NewDroidAgent(cmdPath)
	_, err = a.Review(context.Background(), tmpDir, "deadbeef", "review this commit", f)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	progress, _ := os.ReadFile(progressFile)
	if !strings.Contains(string(progress), "Processing") {
		t.Fatalf("expected progress output, got %q", string(progress))
	}
}

func TestDroidReviewAgenticModeFromGlobal(t *testing.T) {
	prevAllowUnsafe := AllowUnsafeAgents()
	SetAllowUnsafeAgents(true)
	t.Cleanup(func() { SetAllowUnsafeAgents(prevAllowUnsafe) })

	tmpDir := t.TempDir()
	argsFile := filepath.Join(tmpDir, "args.txt")
	t.Setenv("ARGS_FILE", argsFile)

	cmdPath := writeTempCommand(t, `#!/bin/sh
echo "$@" > "$ARGS_FILE"
# Find the -o flag and write to that file
while [ $# -gt 0 ]; do
    case "$1" in
        -o)
            shift
            echo "result" > "$1"
            exit 0
            ;;
    esac
    shift
done
exit 1
`)

	a := NewDroidAgent(cmdPath)
	if _, err := a.Review(context.Background(), tmpDir, "deadbeef", "prompt", nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	// Should use --auto medium when global unsafe agents is enabled
	if !strings.Contains(string(args), "medium") {
		t.Fatalf("expected '--auto medium' in args when global unsafe enabled, got %s", strings.TrimSpace(string(args)))
	}
}
