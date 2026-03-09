package agent

import (
	"bytes"
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
)

func TestStripKiroOutput(t *testing.T) {
	assert := assert.New(t)

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
		assert.NotContains(got, "\x1b[", "result still contains ANSI escape codes")
	}
	if !strings.Contains(got, "## Summary") {
		assert.Contains(got, "## Summary", "expected '## Summary' in result, got: %q", got)
	}
	if !strings.Contains(got, "## Issues Found") {
		assert.Contains(got, "## Issues Found", "expected '## Issues Found' in result, got: %q", got)
	}
	if strings.Contains(got, "Did you know") {
		assert.NotContains(got, "Did you know", "result should not contain tip box text")
	}
	if strings.Contains(got, "Model:") {
		assert.NotContains(got, "Model:", "result should not contain model line")
	}
	if strings.Contains(got, "Time:") {
		assert.NotContains(got, "Time:", "result should not contain timing footer")
	}
	if strings.HasPrefix(got, "> ") {
		assert.NotContains(got, "> ", "result should not have leading '> ' prefix")
	}
}

func TestStripKiroOutputNoMarker(t *testing.T) {

	raw := "\x1b[1msome output without marker\x1b[0m\n"
	got := stripKiroOutput(raw)
	assert.Equal(t, "some output without marker", got, "unexpected result: %q", got)

}

func TestStripKiroOutputBareMarker(t *testing.T) {

	raw := "chrome\n>\nreview content here\n"
	got := stripKiroOutput(raw)
	if !strings.Contains(got, "review content here") {
		assert.Contains(t, got, "review content here", "expected review content, got: %q", got)
	}
	if strings.Contains(got, "chrome") {
		assert.NotContains(t, got, "chrome", "chrome should be stripped before the bare > marker")
	}
	if strings.HasPrefix(got, ">") {
		assert.NotContains(t, got, ">", "bare > marker should not appear in output")
	}
}

func TestStripKiroOutputFooterInContent(t *testing.T) {

	var lines []string
	lines = append(lines, "> ## Review")
	lines = append(lines, "some content")
	lines = append(lines, "▸ Time: 10s")
	for range 10 {
		lines = append(lines, "more content")
	}
	raw := strings.Join(lines, "\n")

	got := stripKiroOutput(raw)
	if !strings.Contains(got, "▸ Time: 10s") {
		assert.Contains(t, got, "▸ Time: 10s",
			"'▸ Time:' in content body should be preserved, got: %q",
			got,
		)
	}
}

func TestStripKiroOutputFooterWithTrailingBlanks(t *testing.T) {

	raw := "> ## Review\ncontent\n ▸ Time: 12s\n\n\n\n\n\n\n\n"
	got := stripKiroOutput(raw)
	if strings.Contains(got, "Time:") {
		assert.NotContains(t, got, "Time:", "footer should be stripped despite trailing blanks, got: %q", got)
	}
	if !strings.Contains(got, "content") {
		assert.Contains(t, got, "content", "review content should be preserved, got: %q", got)
	}
}

func TestStripKiroOutputBlockquoteNotStripped(t *testing.T) {

	var lines []string
	for range 31 {
		lines = append(lines, "chrome line")
	}
	lines = append(lines, "> this is a blockquote in review content")
	lines = append(lines, "more content")
	raw := strings.Join(lines, "\n")

	got := stripKiroOutput(raw)
	if !strings.Contains(got, "> this is a blockquote") {
		assert.Contains(t, got, "> this is a blockquote", "blockquote should be preserved in output, got: %q", got)
	}
}

func TestKiroBuildArgs(t *testing.T) {
	a := NewKiroAgent("kiro-cli")

	args := a.buildArgs(false)
	assertContainsArg(t, args, "chat")
	assertContainsArg(t, args, "--no-interactive")
	assertNotContainsArg(t, args, "--trust-all-tools")

	args = a.buildArgs(true)
	assertContainsArg(t, args, "chat")
	assertContainsArg(t, args, "--no-interactive")
	assertContainsArg(t, args, "--trust-all-tools")
}

func TestKiroName(t *testing.T) {
	a := NewKiroAgent("")
	require.Equal(t, "kiro", a.Name(), "expected name 'kiro', got %s", a.Name())
	require.Equal(t, "kiro-cli", a.CommandName(), "expected command name 'kiro-cli', got %s", a.CommandName())

}

func TestKiroCommandLine(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	cl := a.CommandLine()
	if !strings.Contains(cl, "-- <prompt>") {
		assert.Contains(t, cl, "-- <prompt>",
			"CommandLine should include -- separator, got: %q", cl,
		)
	}
	if !strings.Contains(cl, "--no-interactive") {
		assert.Contains(t, cl, "--no-interactive",
			"CommandLine should include --no-interactive, got: %q", cl,
		)
	}
}

func TestKiroWithAgentic(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	require.False(t, a.Agentic, "expected non-agentic by default")

	a2 := a.WithAgentic(true).(*KiroAgent)
	require.True(t, a2.Agentic, "expected agentic after WithAgentic(true)")
	require.False(t, a.Agentic, "original should be unchanged")

}

func TestKiroWithReasoning(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	b := a.WithReasoning(ReasoningThorough).(*KiroAgent)
	assert.Equal(t, ReasoningThorough, b.Reasoning, "expected thorough reasoning, got %q", b.Reasoning)
	assert.Equal(t, ReasoningStandard, a.Reasoning, "original reasoning should be unchanged")

}

func TestKiroWithModelIsNoop(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	b := a.WithModel("some-model")
	assert.Equal(t, a, b, "WithModel should return the same agent (kiro does not support model selection)")

}

func TestKiroReviewSuccess(t *testing.T) {
	skipIfWindows(t)

	output := "LGTM: looks good to me"
	script := NewScriptBuilder().AddOutput(output).Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "review this commit", nil)
	require.NoError(t, err)
	if !strings.Contains(result, output) {
		require.Contains(t, result, output, "expected result to contain %q, got %q", output, result)
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
	require.NoError(t, err)
	if !strings.Contains(result, output) {
		require.Contains(t, result, output, "expected result to contain output, got: %q", result)
	}
	if !strings.Contains(buf.String(), output) {
		require.Contains(t, buf.String(), output, "expected output writer to contain %q, got %q", output, buf.String())
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
	require.Error(t, err)

	if !strings.Contains(err.Error(), "kiro failed") {
		require.Contains(t, err.Error(), "kiro failed", "unexpected error: %v", err)
	}
}

func TestKiroReviewEmptyOutput(t *testing.T) {
	skipIfWindows(t)

	script := NewScriptBuilder().AddRaw("exit 0").Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "review this commit", nil)
	require.NoError(t, err)
	require.Equal(t, "No review output generated", result, "unexpected result: %q", result)

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
	require.NoError(t, err, "Review failed")

	args, err := os.ReadFile(mock.ArgsFile)
	require.NoError(t, err, "read args capture")
	if !strings.Contains(string(args), prompt) {
		assert.Contains(t, string(args), prompt, "expected prompt in args, got: %s", string(args))
	}
	if !strings.Contains(string(args), "--no-interactive") {
		assert.Contains(t, string(args), "--no-interactive", "expected --no-interactive in args, got: %s", string(args))
	}
	if !strings.Contains(string(args), " -- ") {
		assert.Contains(t, string(args), " -- ", "expected -- separator before prompt, got: %s", string(args))
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
	require.NoError(t, err, "Review failed")

	args, err := os.ReadFile(mock.ArgsFile)
	require.NoError(t, err, "read args capture")
	if !strings.Contains(string(args), "--trust-all-tools") {
		assert.Contains(t, string(args), "--trust-all-tools", "expected --trust-all-tools in args, got: %s", string(args))
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
	require.NoError(t, err, "Review failed")

	args, err := os.ReadFile(mock.ArgsFile)
	require.NoError(t, err, "read args capture")
	if !strings.Contains(string(args), "--trust-all-tools") {
		require.Contains(t, string(args), "--trust-all-tools", "expected --trust-all-tools when global unsafe enabled, got: %s", strings.TrimSpace(string(args)))
	}
}

func TestKiroReviewStderrFallback(t *testing.T) {
	skipIfWindows(t)

	script := NewScriptBuilder().
		AddRaw(`echo "Model: auto" >&2`).
		AddRaw(`echo "> review on stderr" >&2`).
		AddRaw(`echo " ▸ Time: 5s" >&2`).
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "review", nil)
	require.NoError(t, err)
	if !strings.Contains(result, "review on stderr") {
		require.Contains(t, result, "review on stderr", "expected stderr fallback, got: %q", result)
	}
	if strings.Contains(result, "Model:") {
		assert.NotContains(t, result, "Model:", "Kiro chrome should be stripped from stderr")
	}
	if strings.Contains(result, "Time:") {
		assert.NotContains(t, result, "Time:", "timing footer should be stripped from stderr")
	}
}

func TestKiroReviewStderrFallbackMarkerOnlyStdout(t *testing.T) {
	skipIfWindows(t)

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
	require.NoError(t, err)
	if !strings.Contains(result, "review from stderr") {
		require.Contains(t, result, "review from stderr", "expected stderr when stdout is marker-only, got: %q", result)
	}
}

func TestKiroReviewStderrPreferredOverStdoutNoise(t *testing.T) {
	skipIfWindows(t)

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
	require.NoError(t, err)
	if !strings.Contains(result, "actual review content") {
		require.Contains(t, result, "actual review content", "expected stderr review over stdout noise, got: %q", result)
	}
	if strings.Contains(result, "Loading") {
		assert.NotContains(t, result, "Loading", "stdout noise should not appear in result")
	}
}

func TestKiroReviewStderrFallbackNoMarker(t *testing.T) {
	skipIfWindows(t)

	script := NewScriptBuilder().
		AddRaw(`echo "review text without marker" >&2`).
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)

	result, err := a.Review(
		context.Background(), t.TempDir(),
		"deadbeef", "review", nil,
	)
	require.NoError(t, err)
	if !strings.Contains(result, "review text without marker") {
		require.Contains(t, result, "review text without marker", "expected stderr fallback even without marker, got: %q", result)
	}
}

func TestKiroReviewStdoutPreservedOverStderrNoise(t *testing.T) {
	skipIfWindows(t)

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
	require.NoError(t, err)
	if !strings.Contains(result, "plain review on stdout") {
		require.Contains(t, result, "plain review on stdout", "expected stdout to be kept, got: %q", result)
	}
	if strings.Contains(result, "warning") {
		assert.NotContains(t, result, "warning", "stderr noise should not replace stdout content")
	}
}

func TestKiroReviewPromptTooLarge(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	bigPrompt := strings.Repeat("x", maxPromptArgLen+1)
	_, err := a.Review(context.Background(), t.TempDir(), "HEAD", bigPrompt, nil)
	require.Error(t, err)

	if !strings.Contains(err.Error(), "too large") {
		require.Contains(t, err.Error(), "too large", "unexpected error: %v", err)
	}
}

func TestKiroWithChaining(t *testing.T) {
	a := NewKiroAgent("kiro-cli")
	b := a.WithReasoning(ReasoningThorough).WithAgentic(true)
	kiro := b.(*KiroAgent)
	require.Equal(t, ReasoningThorough, kiro.Reasoning, "expected thorough reasoning, got %q", kiro.Reasoning)

	require.True(t, kiro.Agentic, "expected agentic true")
	require.Equal(t, "kiro-cli", kiro.Command, "expected command 'kiro-cli', got %q", kiro.Command)

}
