package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentRegistry(t *testing.T) {
	// Check that all agents are registered
	agents := expectedAgents
	for _, name := range agents {
		a, err := Get(name)
		if err != nil {
			t.Fatalf("Failed to get %s agent: %v", name, err)
		}
		if a.Name() != name {
			t.Errorf("Expected name '%s', got '%s'", name, a.Name())
		}
	}

	// Check unknown agent
	_, err := Get("unknown-agent")
	if err == nil {
		t.Error("Expected error for unknown agent")
	}
}

func TestCanonicalNameAliases(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "claude", want: "claude-code"},
		{input: "agent", want: "cursor"},
		{input: "cursor", want: "cursor"},
	}

	for _, tt := range tests {
		if got := CanonicalName(tt.input); got != tt.want {
			t.Errorf("CanonicalName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetSupportsAgentAlias(t *testing.T) {
	a, err := Get("agent")
	if err != nil {
		t.Fatalf("Get(agent) returned error: %v", err)
	}
	if a.Name() != "cursor" {
		t.Fatalf("Get(agent) resolved to %q, want %q", a.Name(), "cursor")
	}
}

func TestAvailableAgents(t *testing.T) {
	agents := Available()
	if len(agents) < len(expectedAgents) {
		t.Errorf("Expected at least %d agents, got %d: %v", len(expectedAgents), len(agents), agents)
	}

	for _, name := range expectedAgents {
		if !containsString(agents, name) {
			t.Errorf("Expected %s in available agents", name)
		}
	}
}

func TestSyncWriter(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		sw := newSyncWriter(nil)
		if sw != nil {
			t.Error("expected nil for nil input")
		}
	})

	t.Run("wraps writer correctly", func(t *testing.T) {
		var buf bytes.Buffer
		sw := newSyncWriter(&buf)
		if sw == nil {
			t.Fatal("expected non-nil syncWriter")
		}

		n, err := sw.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 5 {
			t.Errorf("expected 5 bytes written, got %d", n)
		}
		if buf.String() != "hello" {
			t.Errorf("expected 'hello', got %q", buf.String())
		}
	})

	t.Run("concurrent writes are safe", func(t *testing.T) {
		var buf bytes.Buffer
		sw := newSyncWriter(&buf)

		var wg sync.WaitGroup
		for i := range 100 {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				sw.Write([]byte("x"))
			}(i)
		}
		wg.Wait()

		if buf.Len() != 100 {
			t.Errorf("expected 100 bytes, got %d", buf.Len())
		}
	})
}

func TestTestAgentStreaming(t *testing.T) {
	setup := func() *TestAgent {
		return &TestAgent{
			Delay:  1 * time.Millisecond,
			Output: "test output",
		}
	}

	t.Run("streams output to writer", func(t *testing.T) {
		agent := setup()

		var buf bytes.Buffer
		result, err := agent.Review(context.Background(), "/tmp", "abc1234567", "prompt", &buf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if buf.String() != result {
			t.Errorf("streamed output should match result\nstreamed: %q\nresult: %q", buf.String(), result)
		}
	})

	t.Run("nil output writer works", func(t *testing.T) {
		agent := setup()

		result, err := agent.Review(context.Background(), "/tmp", "abc1234567", "prompt", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == "" {
			t.Error("expected non-empty result")
		}
	})

	t.Run("returns write error", func(t *testing.T) {
		agent := setup()

		errWriter := &FailingWriter{Err: errors.New("write failed")}
		_, err := agent.Review(context.Background(), "/tmp", "abc1234567", "prompt", errWriter)
		if err == nil {
			t.Fatal("expected error from failing writer")
		}
		if !errors.Is(err, errWriter.Err) {
			t.Errorf("expected wrapped write error, got: %v", err)
		}
	})
}

func TestParseReasoningLevel(t *testing.T) {
	tests := []struct {
		input string
		want  ReasoningLevel
	}{
		{"thorough", ReasoningThorough},
		{"high", ReasoningThorough},
		{"fast", ReasoningFast},
		{"low", ReasoningFast},
		{"standard", ReasoningStandard},
		{"medium", ReasoningStandard},
		{"", ReasoningStandard},
		{"unknown", ReasoningStandard},
	}

	for _, tt := range tests {
		if got := ParseReasoningLevel(tt.input); got != tt.want {
			t.Errorf("ParseReasoningLevel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCodexReasoningEffortMapping(t *testing.T) {
	tests := []struct {
		level ReasoningLevel
		want  string
	}{
		{ReasoningThorough, "high"},
		{ReasoningFast, "low"},
		{ReasoningStandard, ""},
	}

	for _, tt := range tests {
		a := NewCodexAgent("").WithReasoning(tt.level)
		cmdLine := a.CommandLine()
		expectedConfig := `model_reasoning_effort="` + tt.want + `"`

		if tt.want != "" && !strings.Contains(cmdLine, expectedConfig) {
			t.Errorf("expected command line %q to contain %q", cmdLine, expectedConfig)
		} else if tt.want == "" && strings.Contains(cmdLine, "model_reasoning_effort") {
			t.Errorf("expected command line %q to omit reasoning effort", cmdLine)
		}
	}
}

type agentTestDef struct {
	name                  string
	factory               func(string) Agent
	modelFlag             string
	defaultModel          string
	testModel             string
	supportsSmartReview   bool
	supportsPlainFlagEcho bool
}

var agentFixtures = []agentTestDef{
	{"codex", func(s string) Agent { return NewCodexAgent(s) }, "-m", "", "o3", true, false},
	{"claude", func(s string) Agent { return NewClaudeAgent(s) }, "--model", "", "opus", true, false},
	{"gemini", func(s string) Agent { return NewGeminiAgent(s) }, "-m", "gemini-3.1-pro-preview", "gemini-1.5-pro", true, false},
	{"copilot", func(s string) Agent { return NewCopilotAgent(s) }, "--model", "", "gpt-4o", false, true},
	{"opencode", func(s string) Agent { return NewOpenCodeAgent(s) }, "--model", "", "anthropic/claude-sonnet-4", false, false},
	{"cursor", func(s string) Agent { return NewCursorAgent(s) }, "--model", "auto", "claude-sonnet-4", false, false},
	{"kilo", func(s string) Agent { return NewKiloAgent(s) }, "--model", "", "anthropic/claude-sonnet-4-20250514", false, false},
}

func assertArgsNotContain(t *testing.T, cmdLine, flag string) {
	t.Helper()
	for token := range strings.FieldsSeq(cmdLine) {
		if token == flag || strings.HasPrefix(token, flag+"=") {
			t.Errorf("command line %q unexpectedly contained flag %q", cmdLine, flag)
		}
	}
}

func TestAgentModelConfiguration(t *testing.T) {
	for _, tt := range agentFixtures {
		t.Run(tt.name, func(t *testing.T) {
			t.Run("explicit model sets flag", func(t *testing.T) {
				a := tt.factory("").WithModel(tt.testModel)
				assertArgsContain(t, a.CommandLine(), tt.modelFlag, tt.testModel)
			})

			t.Run("empty model preserves default", func(t *testing.T) {
				a := tt.factory("").WithModel("")

				if tt.defaultModel == "" {
					assertArgsNotContain(t, a.CommandLine(), tt.modelFlag)
				} else {
					assertArgsContain(t, a.CommandLine(), tt.modelFlag, tt.defaultModel)
				}
			})

			t.Run("explicit then empty preserves explicit", func(t *testing.T) {
				a := tt.factory("").WithModel("custom-model").WithModel("")
				assertArgsContain(t, a.CommandLine(), tt.modelFlag, "custom-model")
			})

			t.Run("persists through chained calls", func(t *testing.T) {
				a := tt.factory("").WithModel(tt.testModel).WithReasoning(ReasoningFast).WithAgentic(true)
				assertArgsContain(t, a.CommandLine(), tt.modelFlag, tt.testModel)
			})
		})
	}
}

func TestCodexBuildArgsModelWithReasoning(t *testing.T) {
	a := NewCodexAgent("").WithModel("o4-mini").WithReasoning(ReasoningThorough)
	cmdLine := a.CommandLine()

	if !strings.Contains(cmdLine, "-m o4-mini") {
		t.Errorf("expected -m o4-mini in command line %q", cmdLine)
	}
	if !strings.Contains(cmdLine, `-c model_reasoning_effort="high"`) {
		t.Errorf("expected reasoning effort config in command line %q", cmdLine)
	}
}

func assertArgsContain(t *testing.T, cmdLine, flag, value string) {
	t.Helper()
	tokens := strings.Fields(cmdLine)
	for i, token := range tokens {
		if token == flag && i+1 < len(tokens) && tokens[i+1] == value {
			return
		}
		if token == flag+"="+value {
			return
		}
	}
	t.Errorf("command line %q expected to contain flag %q with value %q", cmdLine, flag, value)
}

func TestSmartAgentReviewPassesModelFlag(t *testing.T) {
	skipIfWindows(t)

	for _, tt := range agentFixtures {
		if !tt.supportsSmartReview {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			helpOutput := ""
			jsonOutput := `{"type": "result", "result": "review result"}`
			if tt.name == "codex" {
				helpOutput = "--full-auto"
				jsonOutput = `{"type": "item.completed", "item": {"type": "agent_message", "text": "review result"}}`
			}

			opts := MockCLIOpts{
				HelpOutput:  helpOutput,
				CaptureArgs: true,
				StdoutLines: []string{jsonOutput},
			}
			mock := mockAgentCLI(t, opts)

			agent := tt.factory(mock.CmdPath).WithModel(tt.testModel)
			_, err := agent.Review(context.Background(), t.TempDir(), "head", "prompt", nil)
			if err != nil {
				t.Fatalf("Review failed: %v", err)
			}

			argsBytes, err := os.ReadFile(mock.ArgsFile)
			if err != nil {
				t.Fatalf("read args file: %v", err)
			}
			args := string(argsBytes)

			assertArgsContain(t, args, tt.modelFlag, tt.testModel)
		})
	}
}

func TestAgentReviewPassesModelFlag(t *testing.T) {
	for _, tt := range agentFixtures {
		// opencode uses JSON streaming so verifyAgentPassesFlag (plain text echo)
		// doesn't work; model flag is verified in TestOpenCodeReviewModelFlag.
		if !tt.supportsPlainFlagEcho {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			verifyAgentPassesFlag(t, func(cmdPath string) Agent {
				return tt.factory(cmdPath).WithModel(tt.testModel)
			}, tt.modelFlag, tt.testModel)
		})
	}
}

func TestGetAvailableRejectsUnknownAgent(t *testing.T) {
	_, err := GetAvailable("typo-agent")
	if err == nil {
		t.Fatal("Expected error for unknown agent name")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("Expected 'unknown agent' error, got: %v", err)
	}
}

func TestGetAvailableFallsBackForKnownUnavailable(t *testing.T) {
	// Isolate registry: "codex" has a missing binary, "claude-code"
	// has its binary on PATH. Request "codex" and verify fallback
	// returns "claude-code" without an "unknown agent" error.
	fakeBin := t.TempDir()
	binName := "claude"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	claudeBin := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake claude binary: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	testRegistry := map[string]Agent{
		"codex":       NewCodexAgent("definitely-not-on-path"),
		"claude-code": NewClaudeAgent(""),
	}

	resolved, err := GetAvailableFromRegistry(testRegistry, "codex")
	if err != nil {
		if strings.Contains(err.Error(), "unknown agent") {
			t.Fatalf("Known agent should not produce unknown agent error: %v", err)
		}
		t.Fatalf("Expected fallback, got error: %v", err)
	}
	if resolved.Name() != "claude-code" {
		t.Fatalf("Expected fallback to 'claude-code', got %q", resolved.Name())
	}
}

func TestGetAvailableTriesBackupBeforeChain(t *testing.T) {
	// Setup: codex unavailable, gemini available, claude-code available.
	// Without backup: GetAvailable("codex") → claude-code (first in chain).
	// With backup "gemini": GetAvailable("codex", "gemini") → gemini.
	fakeBin := t.TempDir()
	for _, bin := range []string{"gemini", "claude"} {
		name := bin
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		p := filepath.Join(fakeBin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("create fake %s binary: %v", bin, err)
		}
	}
	t.Setenv("PATH", fakeBin)

	originalRegistry := registry
	registry = map[string]Agent{
		"codex":       NewCodexAgent("definitely-not-on-path"),
		"gemini":      NewGeminiAgent(""),
		"claude-code": NewClaudeAgent(""),
	}
	t.Cleanup(func() { registry = originalRegistry })

	// With backup, should pick gemini (not claude-code from chain)
	resolved, err := GetAvailable("codex", "gemini")
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	if resolved.Name() != "gemini" {
		t.Fatalf("expected backup agent 'gemini', got %q", resolved.Name())
	}

	// Without backup, should still fall through to claude-code
	resolved, err = GetAvailable("codex")
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	if resolved.Name() != "claude-code" {
		t.Fatalf("expected chain fallback 'claude-code', got %q", resolved.Name())
	}
}

func TestGetAvailableBackupSkipsDuplicateAndEmpty(t *testing.T) {
	// Backup that matches preferred or is empty should be skipped.
	fakeBin := t.TempDir()
	binName := "claude"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	p := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("create fake binary: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	originalRegistry := registry
	registry = map[string]Agent{
		"codex":       NewCodexAgent("definitely-not-on-path"),
		"claude-code": NewClaudeAgent(""),
	}
	t.Cleanup(func() { registry = originalRegistry })

	// Backup same as preferred → skipped, falls through to chain
	resolved, err := GetAvailable("codex", "codex", "")
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	if resolved.Name() != "claude-code" {
		t.Fatalf("expected chain fallback 'claude-code', got %q", resolved.Name())
	}
}

func TestGetAvailableBackupResolvesAliases(t *testing.T) {
	// Backup "claude" should resolve to "claude-code" via alias.
	fakeBin := t.TempDir()
	binName := "claude"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	p := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("create fake binary: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	originalRegistry := registry
	registry = map[string]Agent{
		"codex":       NewCodexAgent("definitely-not-on-path"),
		"claude-code": NewClaudeAgent(""),
	}
	t.Cleanup(func() { registry = originalRegistry })

	resolved, err := GetAvailable("codex", "claude")
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	if resolved.Name() != "claude-code" {
		t.Fatalf("expected alias-resolved 'claude-code', got %q", resolved.Name())
	}
}

func TestGetAvailableBackupUnavailableFallsToChain(t *testing.T) {
	// Backup agent that is unavailable should be skipped, chain used.
	fakeBin := t.TempDir()
	binName := "claude"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	p := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("create fake binary: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	originalRegistry := registry
	registry = map[string]Agent{
		"codex":       NewCodexAgent("definitely-not-on-path"),
		"gemini":      NewGeminiAgent("also-not-on-path"),
		"claude-code": NewClaudeAgent(""),
	}
	t.Cleanup(func() { registry = originalRegistry })

	// Backup gemini is registered but unavailable → falls to chain
	resolved, err := GetAvailable("codex", "gemini")
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	if resolved.Name() != "claude-code" {
		t.Fatalf("expected chain fallback 'claude-code', got %q", resolved.Name())
	}
}
