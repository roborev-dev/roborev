package agent

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestStripKiroOutput(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantContains []string
		wantMissing  []string
		exactMatch   string
	}{
		{
			name: "standard kiro output",
			raw: "\x1b[38;5;141m⠀⠀logo⠀⠀\x1b[0m\n" +
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
				" \u25b8 Time: 21s\n",
			wantContains: []string{"## Summary", "## Issues Found"},
			wantMissing:  []string{"\x1b[", "Did you know", "Model:", "Time:"},
		},
		{
			name:       "no marker",
			raw:        "\x1b[1msome output without marker\x1b[0m\n",
			exactMatch: "some output without marker",
		},
		{
			name:         "bare marker",
			raw:          "chrome\n>\nreview content here\n",
			wantContains: []string{"review content here"},
			wantMissing:  []string{"chrome"},
		},
		{
			name: "footer in content",
			raw: func() string {
				var lines []string
				lines = append(lines, "> ## Review", "some content", "▸ Time: 10s")
				for range 10 {
					lines = append(lines, "more content")
				}
				return strings.Join(lines, "\n")
			}(),
			wantContains: []string{"▸ Time: 10s"},
		},
		{
			name:         "footer with trailing blanks",
			raw:          "> ## Review\ncontent\n ▸ Time: 12s\n\n\n\n\n\n\n\n",
			wantContains: []string{"content"},
			wantMissing:  []string{"Time:"},
		},
		{
			name: "blockquote not stripped",
			raw: func() string {
				var lines []string
				for range 31 {
					lines = append(lines, "chrome line")
				}
				lines = append(lines, "> this is a blockquote in review content", "more content")
				return strings.Join(lines, "\n")
			}(),
			wantContains: []string{"> this is a blockquote"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripKiroOutput(tt.raw)
			if tt.exactMatch != "" && got != tt.exactMatch {
				t.Errorf("got %q, want %q", got, tt.exactMatch)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q to contain %q", got, want)
				}
			}
			for _, missing := range tt.wantMissing {
				if strings.Contains(got, missing) {
					t.Errorf("expected %q to not contain %q", got, missing)
				}
			}
			if tt.name == "standard kiro output" && strings.HasPrefix(got, "> ") {
				t.Error("result should not have leading '> ' prefix")
			}
			if tt.name == "bare marker" && strings.HasPrefix(got, ">") {
				t.Error("bare > marker should not appear in output")
			}
		})
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

func runMockKiroReview(t *testing.T, script string) (string, error) {
	t.Helper()
	cmdPath := writeTempCommand(t, script)
	a := NewKiroAgent(cmdPath)
	return a.Review(context.Background(), t.TempDir(), "deadbeef", "review", nil)
}

func TestKiroReviewFallbackScenarios(t *testing.T) {
	skipIfWindows(t)
	tests := []struct {
		name         string
		script       string
		wantErr      bool
		exactContent string
		wantContains []string
		wantMissing  []string
	}{
		{
			name:         "empty output",
			script:       NewScriptBuilder().AddRaw("exit 0").Build(),
			exactContent: "No review output generated",
		},
		{
			name: "stderr fallback",
			script: NewScriptBuilder().
				AddRaw(`echo "Model: auto" >&2`).
				AddRaw(`echo "> review on stderr" >&2`).
				AddRaw(`echo " ▸ Time: 5s" >&2`).
				Build(),
			wantContains: []string{"review on stderr"},
			wantMissing:  []string{"Model:", "Time:"},
		},
		{
			name: "stderr fallback marker only stdout",
			script: NewScriptBuilder().
				AddRaw(`echo ">"`).
				AddRaw(`echo "> review from stderr" >&2`).
				Build(),
			wantContains: []string{"review from stderr"},
		},
		{
			name: "stderr preferred over stdout noise",
			script: NewScriptBuilder().
				AddRaw(`echo "Model: auto"`).
				AddRaw(`echo "Loading..."`).
				AddRaw(`echo "> actual review content" >&2`).
				Build(),
			wantContains: []string{"actual review content"},
			wantMissing:  []string{"Loading"},
		},
		{
			name: "stderr fallback no marker",
			script: NewScriptBuilder().
				AddRaw(`echo "review text without marker" >&2`).
				Build(),
			wantContains: []string{"review text without marker"},
		},
		{
			name: "stdout preserved over stderr noise",
			script: NewScriptBuilder().
				AddRaw(`echo "plain review on stdout"`).
				AddRaw(`echo "warning: something" >&2`).
				Build(),
			wantContains: []string{"plain review on stdout"},
			wantMissing:  []string{"warning"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runMockKiroReview(t, tt.script)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr %v, got err %v", tt.wantErr, err)
			}
			if tt.exactContent != "" && got != tt.exactContent {
				t.Errorf("got %q, want %q", got, tt.exactContent)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("got %q, want it to contain %q", got, want)
				}
			}
			for _, missing := range tt.wantMissing {
				if strings.Contains(got, missing) {
					t.Errorf("got %q, want it to not contain %q", got, missing)
				}
			}
		})
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
