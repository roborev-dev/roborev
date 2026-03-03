package agent

import (
	"bytes"
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

func TestStripKiroOutputBareMarker(t *testing.T) {
	// A bare ">" (no trailing space) followed by content on the next line.
	raw := "chrome\n>\nreview content here\n"
	got := stripKiroOutput(raw)
	if !strings.Contains(got, "review content here") {
		t.Errorf("expected review content, got: %q", got)
	}
	if strings.Contains(got, "chrome") {
		t.Error("chrome should be stripped before the bare > marker")
	}
	if strings.HasPrefix(got, ">") {
		t.Error("bare > marker should not appear in output")
	}
}

func TestStripKiroOutputFooterInContent(t *testing.T) {
	// "▸ Time:" appearing in review content (not in the last 5 lines)
	// should not be treated as a footer.
	var lines []string
	lines = append(lines, "> ## Review")
	lines = append(lines, "some content")
	lines = append(lines, "▸ Time: 10s") // looks like footer but is in content
	for range 10 {
		lines = append(lines, "more content")
	}
	raw := strings.Join(lines, "\n")

	got := stripKiroOutput(raw)
	if !strings.Contains(got, "▸ Time: 10s") {
		t.Errorf(
			"'▸ Time:' in content body should be preserved, got: %q",
			got,
		)
	}
}

func TestStripKiroOutputFooterWithTrailingBlanks(t *testing.T) {
	// Footer followed by trailing blank lines should still be stripped.
	raw := "> ## Review\ncontent\n ▸ Time: 12s\n\n\n\n\n\n\n\n"
	got := stripKiroOutput(raw)
	if strings.Contains(got, "Time:") {
		t.Errorf("footer should be stripped despite trailing blanks, got: %q", got)
	}
	if !strings.Contains(got, "content") {
		t.Errorf("review content should be preserved, got: %q", got)
	}
}

func TestStripKiroOutputBlockquoteNotStripped(t *testing.T) {
	// A "> " blockquote deep in review content should not be treated as the start marker.
	// Build output where "> " only appears after line 30.
	var lines []string
	for range 31 {
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

func TestKiroCommandLine(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	cl := a.CommandLine()
	if !strings.Contains(cl, "-- <prompt>") {
		t.Errorf(
			"CommandLine should include -- separator, got: %q", cl,
		)
	}
	if !strings.Contains(cl, "--no-interactive") {
		t.Errorf(
			"CommandLine should include --no-interactive, got: %q", cl,
		)
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

func TestKiroReviewWritesOutputWriter(t *testing.T) {
	skipIfWindows(t)

	output := "review findings here"
	script := NewScriptBuilder().AddOutput(output).Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	var buf bytes.Buffer
	result, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "review", &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, output) {
		t.Fatalf("result missing output: %q", result)
	}
	if !strings.Contains(buf.String(), output) {
		t.Fatalf("output writer missing content: %q", buf.String())
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
	if !strings.Contains(string(args), " -- ") {
		t.Errorf("expected -- separator before prompt, got: %s",
			string(args))
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

func TestKiroReviewStderrFallback(t *testing.T) {
	skipIfWindows(t)

	// kiro-cli exits 0 with Kiro chrome + review text on stderr,
	// nothing on stdout. Validates that stripKiroOutput (not just
	// stripTerminalControls) is applied to stderr.
	script := NewScriptBuilder().
		AddRaw(`echo "Model: auto" >&2`).
		AddRaw(`echo "> review on stderr" >&2`).
		AddRaw(`echo " ▸ Time: 5s" >&2`).
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "review", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "review on stderr") {
		t.Fatalf("expected stderr fallback, got: %q", result)
	}
	if strings.Contains(result, "Model:") {
		t.Error("Kiro chrome should be stripped from stderr")
	}
	if strings.Contains(result, "Time:") {
		t.Error("timing footer should be stripped from stderr")
	}
}

func TestKiroReviewStderrFallbackMarkerOnlyStdout(t *testing.T) {
	skipIfWindows(t)

	// stdout has only a bare ">" marker with no review body;
	// stderr has the actual review. stderr should be used.
	script := NewScriptBuilder().
		AddRaw(`echo ">"`).
		AddRaw(`echo "> review from stderr" >&2`).
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(
		context.Background(), t.TempDir(),
		"deadbeef", "review", nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "review from stderr") {
		t.Fatalf(
			"expected stderr when stdout is marker-only, got: %q",
			result,
		)
	}
}

func TestKiroReviewStderrPreferredOverStdoutNoise(t *testing.T) {
	skipIfWindows(t)

	// stdout has status noise without a "> " marker;
	// stderr has the actual review with a marker.
	// The fallback should prefer stderr.
	script := NewScriptBuilder().
		AddRaw(`echo "Model: auto"`).
		AddRaw(`echo "Loading..."`).
		AddRaw(`echo "> actual review content" >&2`).
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(
		context.Background(), t.TempDir(),
		"deadbeef", "review", nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "actual review content") {
		t.Fatalf(
			"expected stderr review over stdout noise, got: %q",
			result,
		)
	}
	if strings.Contains(result, "Loading") {
		t.Error("stdout noise should not appear in result")
	}
}

func TestKiroReviewStderrFallbackNoMarker(t *testing.T) {
	skipIfWindows(t)

	// stdout is empty; stderr has plain review text without
	// a marker. stderr should be used as fallback.
	script := NewScriptBuilder().
		AddRaw(`echo "review text without marker" >&2`).
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(
		context.Background(), t.TempDir(),
		"deadbeef", "review", nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "review text without marker") {
		t.Fatalf(
			"expected stderr fallback even without marker, got: %q",
			result,
		)
	}
}

func TestKiroReviewStdoutPreservedOverStderrNoise(t *testing.T) {
	skipIfWindows(t)

	// Both streams have content, neither has a marker.
	// Stdout (primary stream) should be kept.
	script := NewScriptBuilder().
		AddRaw(`echo "plain review on stdout"`).
		AddRaw(`echo "warning: something" >&2`).
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(
		context.Background(), t.TempDir(),
		"deadbeef", "review", nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "plain review on stdout") {
		t.Fatalf(
			"expected stdout to be kept, got: %q", result,
		)
	}
	if strings.Contains(result, "warning") {
		t.Error("stderr noise should not replace stdout content")
	}
}

func TestKiroReviewPromptTooLarge(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	bigPrompt := strings.Repeat("x", maxPromptArgLen+1)
	_, err := a.Review(context.Background(), t.TempDir(), "HEAD", bigPrompt, nil)
	if err == nil {
		t.Fatal("expected error for oversized prompt")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("unexpected error: %v", err)
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
