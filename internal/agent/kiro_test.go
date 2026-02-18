package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestKiroBuildArgs(t *testing.T) {
	a := NewKiroAgent("kiro-cli")

	// Non-agentic mode: no --trust-all-tools
	args := a.buildArgs(false)
	assertContainsArg(t, args, "chat")
	assertContainsArg(t, args, "--no-interactive")
	assertNotContainsArg(t, args, "--trust-all-tools")

	// Agentic mode: adds --trust-all-tools
	args = a.buildArgs(true)
	assertContainsArg(t, args, "chat")
	assertContainsArg(t, args, "--no-interactive")
	assertContainsArg(t, args, "--trust-all-tools")
}

func TestKiroName(t *testing.T) {
	a := NewKiroAgent("")
	if a.Name() != "kiro" {
		t.Fatalf("expected name 'kiro', got %s", a.Name())
	}
	if a.CommandName() != "kiro-cli" {
		t.Fatalf("expected command name 'kiro-cli', got %s", a.CommandName())
	}
}

func TestKiroWithAgentic(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	if a.Agentic {
		t.Fatal("expected non-agentic by default")
	}

	a2 := a.WithAgentic(true).(*KiroAgent)
	if !a2.Agentic {
		t.Fatal("expected agentic after WithAgentic(true)")
	}
	if a.Agentic {
		t.Fatal("original should be unchanged")
	}
}

func TestKiroWithReasoning(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	b := a.WithReasoning(ReasoningThorough).(*KiroAgent)
	if b.Reasoning != ReasoningThorough {
		t.Errorf("expected thorough reasoning, got %q", b.Reasoning)
	}
	if a.Reasoning != ReasoningStandard {
		t.Error("original reasoning should be unchanged")
	}
}

func TestKiroWithModelIsNoop(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	b := a.WithModel("some-model")
	// kiro-cli has no --model flag; WithModel returns self unchanged
	if b != a {
		t.Error("WithModel should return the same agent (kiro does not support model selection)")
	}
}

func TestKiroReviewSuccess(t *testing.T) {
	skipIfWindows(t)

	output := "LGTM: looks good to me"
	script := NewScriptBuilder().AddOutput(output).Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "review this commit", nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !strings.Contains(result, output) {
		t.Fatalf("expected result to contain %q, got %q", output, result)
	}
}

func TestKiroReviewFailure(t *testing.T) {
	skipIfWindows(t)

	script := NewScriptBuilder().
		AddRaw(`echo "error: auth failed" >&2`).
		AddRaw("exit 1").
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "review this commit", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "kiro failed") {
		t.Fatalf("expected 'kiro failed' in error, got %v", err)
	}
}

func TestKiroReviewEmptyOutput(t *testing.T) {
	skipIfWindows(t)

	script := NewScriptBuilder().AddRaw("exit 0").Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "review this commit", nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "No review output generated" {
		t.Fatalf("expected 'No review output generated', got %q", result)
	}
}

func TestKiroPassesPromptAsArg(t *testing.T) {
	skipIfWindows(t)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs: true,
		StdoutLines: []string{"review complete"},
	})

	a := NewKiroAgent(mock.CmdPath)
	prompt := "Review this commit for issues"
	_, err := a.Review(context.Background(), t.TempDir(), "HEAD", prompt, nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	args, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("read args capture: %v", err)
	}
	if !strings.Contains(string(args), prompt) {
		t.Errorf("expected prompt in args, got: %s", string(args))
	}
	if !strings.Contains(string(args), "--no-interactive") {
		t.Errorf("expected --no-interactive in args, got: %s", string(args))
	}
}

func TestKiroReviewAgenticMode(t *testing.T) {
	skipIfWindows(t)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs: true,
		StdoutLines: []string{"review complete"},
	})

	a := NewKiroAgent(mock.CmdPath)
	a2 := a.WithAgentic(true).(*KiroAgent)

	_, err := a2.Review(context.Background(), t.TempDir(), "HEAD", "prompt", nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	args, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("read args capture: %v", err)
	}
	if !strings.Contains(string(args), "--trust-all-tools") {
		t.Errorf("expected --trust-all-tools in args, got: %s", string(args))
	}
}

func TestKiroReviewAgenticModeFromGlobal(t *testing.T) {
	withUnsafeAgents(t, true)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs: true,
		StdoutLines: []string{"review complete"},
	})

	a := NewKiroAgent(mock.CmdPath)
	_, err := a.Review(context.Background(), t.TempDir(), "HEAD", "prompt", nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	args, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("read args capture: %v", err)
	}
	if !strings.Contains(string(args), "--trust-all-tools") {
		t.Fatalf("expected --trust-all-tools when global unsafe enabled, got: %s", strings.TrimSpace(string(args)))
	}
}

func TestKiroWithChaining(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	b := a.WithReasoning(ReasoningThorough).WithAgentic(true)
	kiro := b.(*KiroAgent)

	if kiro.Reasoning != ReasoningThorough {
		t.Errorf("expected thorough reasoning, got %q", kiro.Reasoning)
	}
	if !kiro.Agentic {
		t.Error("expected agentic true")
	}
	if kiro.Command != "kiro-cli" {
		t.Errorf("expected command 'kiro-cli', got %q", kiro.Command)
	}
}
