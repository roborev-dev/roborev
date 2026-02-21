package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestStripKiroOutput(t *testing.T) {
	// Simulated kiro-cli stdout with ANSI codes, logo, tip box, model line, and timing footer.
	raw := "\x1b[38;5;141m⠀⠀logo⠀⠀\x1b[0m\n" +
		"\x1b[38;5;244m╭─── Did you know? ───╮\x1b[0m\n" +
		"\x1b[38;5;244m│ tip text            │\x1b[0m\n" +
		"\x1b[38;5;244m╰─────────────────────╯\x1b[0m\n" +
		"\x1b[38;5;244mModel: auto\x1b[0m\n" +
		"\n" +
		"\x1b[38;5;141m> \x1b[0m\x1b[1m## Summary\x1b[0m\n" +
		"This commit does something.\n" +
		"\n" +
		"## Issues Found\n" +
		"\n" +
		" \u25b8 Time: 21s\n"

	got := stripKiroOutput(raw)

	if strings.Contains(got, "\x1b[") {
		t.Error("result still contains ANSI escape codes")
	}
	if !strings.Contains(got, "## Summary") {
		t.Errorf("expected '## Summary' in result, got: %q", got)
	}
	if !strings.Contains(got, "## Issues Found") {
		t.Errorf("expected '## Issues Found' in result, got: %q", got)
	}
	if strings.Contains(got, "Did you know") {
		t.Error("result should not contain tip box text")
	}
	if strings.Contains(got, "Model:") {
		t.Error("result should not contain model line")
	}
	if strings.Contains(got, "Time:") {
		t.Error("result should not contain timing footer")
	}
	if strings.HasPrefix(got, "> ") {
		t.Error("result should not have leading '> ' prefix")
	}
}

func TestStripKiroOutputNoMarker(t *testing.T) {
	// If there's no "> " marker in the first 30 lines, return ANSI-stripped text as-is.
	raw := "\x1b[1msome output without marker\x1b[0m\n"
	got := stripKiroOutput(raw)
	if got != "some output without marker" {
		t.Errorf("unexpected result: %q", got)
	}
}

func TestStripKiroOutputBlockquoteNotStripped(t *testing.T) {
	// A "> " blockquote deep in review content should not be treated as the start marker.
	// Build output where "> " only appears after line 30.
	var lines []string
	for i := 0; i < 31; i++ {
		lines = append(lines, "chrome line")
	}
	lines = append(lines, "> this is a blockquote in review content")
	lines = append(lines, "more content")
	raw := strings.Join(lines, "\n")

	got := stripKiroOutput(raw)
	if !strings.Contains(got, "> this is a blockquote") {
		t.Errorf("blockquote should be preserved in output, got: %q", got)
	}
}

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
