package agent

import (
	"bytes"
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentRegistry(t *testing.T) {

	agents := expectedAgents
	for _, name := range agents {
		a, err := Get(name)
		require.NoError(t, err, "Failed to get %s agent", name)
		assert.Equal(t, name, a.Name(), "Expected name '%s', got '%s'", name, a.Name())

	}

	_, err := Get("unknown-agent")
	require.Error(t, err, "Expected error for unknown agent")

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
			assert.Equalf(t, tt.want, got, "CanonicalName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetSupportsAgentAlias(t *testing.T) {
	a, err := Get("agent")
	require.NoError(t, err, "Get(agent) returned error")
	assert.Equal(t, "cursor", a.Name(), "Get(agent) resolved to %q, want %q", a.Name(), "cursor")

}

func TestAvailableAgents(t *testing.T) {
	agents := Available()
	if len(agents) < len(expectedAgents) {
		assert.GreaterOrEqual(t, len(agents), len(expectedAgents), "Expected at least %d agents, got %d", len(expectedAgents), len(agents))
	}

	for _, name := range expectedAgents {
		if !containsString(agents, name) {
			assert.Contains(t, agents, name, "Expected %s in available agents", name)
		}
	}
}

func TestSyncWriter(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		sw := newSyncWriter(nil)
		assert.Nil(t, sw, "expected nil for nil input")

	})

	t.Run("wraps writer correctly", func(t *testing.T) {
		var buf bytes.Buffer
		sw := newSyncWriter(&buf)
		require.NotNil(t, sw, "expected non-nil syncWriter")

		n, err := sw.Write([]byte("hello"))
		require.NoError(t, err)
		assert.Equal(t, 5, n, "expected 5 bytes written, got %d", n)
		assert.Equal(t, "hello", buf.String(), "expected 'hello', got %q", buf.String())

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
		assert.Equal(t, 100, buf.Len(), "expected 100 bytes, got %d", buf.Len())

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
		require.NoError(t, err)
		assert.Equal(t, result, buf.String(), "streamed output should match result\nstreamed: %q\nresult: %q", buf.String(), result)

	})

	t.Run("nil output writer works", func(t *testing.T) {
		agent := setup()

		result, err := agent.Review(context.Background(), "/tmp", "abc1234567", "prompt", nil)
		require.NoError(t, err)
		if result == "" {
			assert.NotEmpty(t, result, "expected non-empty result")
		}
	})

	t.Run("returns write error", func(t *testing.T) {
		agent := setup()

		errWriter := &FailingWriter{Err: errors.New("write failed")}
		_, err := agent.Review(context.Background(), "/tmp", "abc1234567", "prompt", errWriter)
		require.Error(t, err)

		if !errors.Is(err, errWriter.Err) {
			require.ErrorIs(t, err, errWriter.Err, "expected wrapped write error, got: %v", err)
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
		assert.Equal(t, tt.want, ParseReasoningLevel(tt.input), "unexpected condition")
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
		codex, ok := a.(*CodexAgent)
		require.True(t, ok, "expected CodexAgent, got %T", a)

		if got := codex.codexReasoningEffort(); got != tt.want {
			assert.Equalf(t, tt.want, got, "codexReasoningEffort(%q) = %q, want %q", tt.level, got, tt.want)
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
		assert.False(t, token == flag || strings.HasPrefix(token, flag+"="), "unexpected condition")
	}
}

func TestAgentWithModelPersistence(t *testing.T) {
	for _, tt := range agentFixtures {
		t.Run(tt.name+"/WithModel sets model", func(t *testing.T) {
			a := tt.factory("").WithModel(tt.testModel)
			cmdLine := a.CommandLine()
			assertArgsContain(t, cmdLine, tt.modelFlag, tt.testModel)
		})

		t.Run(tt.name+"/model persists through WithReasoning", func(t *testing.T) {
			a := tt.factory("").WithModel(tt.testModel).WithReasoning(ReasoningThorough)
			cmdLine := a.CommandLine()
			assertArgsContain(t, cmdLine, tt.modelFlag, tt.testModel)
		})

		t.Run(tt.name+"/model persists through WithAgentic", func(t *testing.T) {
			a := tt.factory("").WithModel(tt.testModel).WithAgentic(true)
			cmdLine := a.CommandLine()
			assertArgsContain(t, cmdLine, tt.modelFlag, tt.testModel)
		})

		t.Run(tt.name+"/model persists through chained calls", func(t *testing.T) {
			a := tt.factory("").WithModel(tt.testModel).WithReasoning(ReasoningFast).WithAgentic(true)
			cmdLine := a.CommandLine()
			assertArgsContain(t, cmdLine, tt.modelFlag, tt.testModel)
		})
	}
}

func TestWithModelEmptyPreservesDefault(t *testing.T) {
	for _, tt := range agentFixtures {
		t.Run(tt.name, func(t *testing.T) {
			a := tt.factory("")
			b := a.WithModel("")
			cmdLine := b.CommandLine()

			if tt.defaultModel == "" {
				assertArgsNotContain(t, cmdLine, tt.modelFlag)
			} else {
				assertArgsContain(t, cmdLine, tt.modelFlag, tt.defaultModel)
			}
		})

		t.Run(tt.name+"/explicit then empty preserves explicit", func(t *testing.T) {
			a := tt.factory("").WithModel("custom-model")
			b := a.WithModel("")
			cmdLine := b.CommandLine()

			assertArgsContain(t, cmdLine, tt.modelFlag, "custom-model")
		})
	}
}

func TestAgentBuildArgsWithModel(t *testing.T) {
	for _, tt := range agentFixtures {
		t.Run(tt.name+" with explicit model", func(t *testing.T) {
			agent := tt.factory("").WithModel(tt.testModel)
			cmdLine := agent.CommandLine()
			assertArgsContain(t, cmdLine, tt.modelFlag, tt.testModel)
		})

		t.Run(tt.name+" without model", func(t *testing.T) {
			agent := tt.factory("")
			cmdLine := agent.CommandLine()

			if tt.defaultModel == "" {
				assertArgsNotContain(t, cmdLine, tt.modelFlag)
			} else {
				assertArgsContain(t, cmdLine, tt.modelFlag, tt.defaultModel)
			}
		})
	}
}

func TestCodexBuildArgsModelWithReasoning(t *testing.T) {
	a := NewCodexAgent("").WithModel("o4-mini").WithReasoning(ReasoningThorough)
	cmdLine := a.CommandLine()

	if !strings.Contains(cmdLine, "-m o4-mini") {
		assert.Contains(t, cmdLine, "-m o4-mini", "expected -m o4-mini in command line %q", cmdLine)
	}
	if !strings.Contains(cmdLine, `-c model_reasoning_effort="high"`) {
		assert.Contains(t, cmdLine, `-c model_reasoning_effort="high"`, "expected reasoning effort config in command line %q", cmdLine)
	}
}

func assertArgsContain(t *testing.T, cmdLine, flag, value string) {
	t.Helper()
	tokens := strings.Fields(cmdLine)
	got := false
	for i := 0; i < len(tokens)-1; i++ {
		if tokens[i] == flag && tokens[i+1] == value {
			got = true
			break
		}
	}
	assert.True(t, got, "command line %q expected to contain flag %q followed by value %q", cmdLine, flag, value)
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
			require.NoError(t, err, "Review failed: %v")

			argsBytes, err := os.ReadFile(mock.ArgsFile)
			require.NoError(t, err, "read args file: %v")

			args := string(argsBytes)

			assertArgsContain(t, args, tt.modelFlag, tt.testModel)
		})
	}
}

func TestAgentReviewPassesModelFlag(t *testing.T) {
	for _, tt := range agentFixtures {

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
	require.Error(t, err, "Expected error for unknown agent name")

	if !strings.Contains(err.Error(), "unknown agent") {
		require.Failf(t, "unexpected error", "Expected 'unknown agent' error, got: %v", err)
	}
}

func TestGetAvailableFallsBackForKnownUnavailable(t *testing.T) {

	fakeBin := t.TempDir()
	binName := "claude"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	claudeBin := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		require.NoError(t, err, "failed to create fake claude binary: %v")
	}
	t.Setenv("PATH", fakeBin)

	originalRegistry := registry
	registry = map[string]Agent{
		"codex":       NewCodexAgent("definitely-not-on-path"),
		"claude-code": NewClaudeAgent(""),
	}
	t.Cleanup(func() { registry = originalRegistry })

	resolved, err := GetAvailable("codex")
	if err != nil {
		if strings.Contains(err.Error(), "unknown agent") {
			require.NoError(t, err, "Known agent should not produce unknown agent error")
		}
		require.NoError(t, err, "Expected fallback")
	}
	require.Equal(t, "claude-code", resolved.Name(), "Expected fallback to 'claude-code', got %q", resolved.Name())

}

func TestGetAvailableTriesBackupBeforeChain(t *testing.T) {

	fakeBin := t.TempDir()
	for _, bin := range []string{"gemini", "claude"} {
		name := bin
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		p := filepath.Join(fakeBin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			require.Errorf(t, err, "create fake %s binary: %v", bin, err)
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

	resolved, err := GetAvailable("codex", "gemini")
	require.NoError(t, err, "expected fallback")
	require.Equal(t, "gemini", resolved.Name(), "expected backup agent 'gemini', got %q", resolved.Name())

	resolved, err = GetAvailable("codex")
	require.NoError(t, err, "expected fallback")
	require.Equal(t, "claude-code", resolved.Name(), "expected chain fallback 'claude-code', got %q", resolved.Name())

}

func TestGetAvailableBackupSkipsDuplicateAndEmpty(t *testing.T) {

	fakeBin := t.TempDir()
	binName := "claude"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	p := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		require.NoError(t, err, "create fake binary: %v")
	}
	t.Setenv("PATH", fakeBin)

	originalRegistry := registry
	registry = map[string]Agent{
		"codex":       NewCodexAgent("definitely-not-on-path"),
		"claude-code": NewClaudeAgent(""),
	}
	t.Cleanup(func() { registry = originalRegistry })

	resolved, err := GetAvailable("codex", "codex", "")
	require.NoError(t, err, "expected fallback")
	require.Equal(t, "claude-code", resolved.Name(), "expected chain fallback 'claude-code', got %q", resolved.Name())

}

func TestGetAvailableBackupResolvesAliases(t *testing.T) {

	fakeBin := t.TempDir()
	binName := "claude"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	p := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		require.NoError(t, err, "create fake binary: %v")
	}
	t.Setenv("PATH", fakeBin)

	originalRegistry := registry
	registry = map[string]Agent{
		"codex":       NewCodexAgent("definitely-not-on-path"),
		"claude-code": NewClaudeAgent(""),
	}
	t.Cleanup(func() { registry = originalRegistry })

	resolved, err := GetAvailable("codex", "claude")
	require.NoError(t, err, "expected fallback")
	require.Equal(t, "claude-code", resolved.Name(), "expected alias-resolved 'claude-code', got %q", resolved.Name())

}

func TestGetAvailableBackupUnavailableFallsToChain(t *testing.T) {

	fakeBin := t.TempDir()
	binName := "claude"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	p := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		require.NoError(t, err, "create fake binary: %v")
	}
	t.Setenv("PATH", fakeBin)

	originalRegistry := registry
	registry = map[string]Agent{
		"codex":       NewCodexAgent("definitely-not-on-path"),
		"gemini":      NewGeminiAgent("also-not-on-path"),
		"claude-code": NewClaudeAgent(""),
	}
	t.Cleanup(func() { registry = originalRegistry })

	resolved, err := GetAvailable("codex", "gemini")
	require.NoError(t, err, "expected fallback")
	require.Equal(t, "claude-code", resolved.Name(), "expected chain fallback 'claude-code', got %q", resolved.Name())

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
