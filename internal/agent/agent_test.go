package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
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
		codex, ok := a.(*CodexAgent)
		if !ok {
			t.Fatalf("expected CodexAgent, got %T", a)
		}
		if got := codex.codexReasoningEffort(); got != tt.want {
			t.Errorf("codexReasoningEffort(%q) = %q, want %q", tt.level, got, tt.want)
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
	{"gemini", func(s string) Agent { return NewGeminiAgent(s) }, "-m", "gemini-3-pro-preview", "gemini-1.5-pro", true, false},
	{"copilot", func(s string) Agent { return NewCopilotAgent(s) }, "--model", "", "gpt-4o", false, true},
	{"opencode", func(s string) Agent { return NewOpenCodeAgent(s) }, "--model", "", "anthropic/claude-sonnet-4", false, false},
	{"cursor", func(s string) Agent { return NewCursorAgent(s) }, "--model", "auto", "claude-sonnet-4", false, false},
}

func assertArgsNotContain(t *testing.T, cmdLine, flag string) {
	t.Helper()
	for token := range strings.FieldsSeq(cmdLine) {
		if token == flag || strings.HasPrefix(token, flag+"=") {
			t.Errorf("command line %q unexpectedly contained flag %q", cmdLine, flag)
		}
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
		t.Errorf("expected -m o4-mini in command line %q", cmdLine)
	}
	if !strings.Contains(cmdLine, `-c model_reasoning_effort="high"`) {
		t.Errorf("expected reasoning effort config in command line %q", cmdLine)
	}
}

func assertArgsContain(t *testing.T, cmdLine, flag, value string) {
	t.Helper()
	tokens := strings.Fields(cmdLine)
	for i := 0; i < len(tokens)-1; i++ {
		if tokens[i] == flag && tokens[i+1] == value {
			return
		}
	}
	t.Errorf("command line %q expected to contain flag %q followed by value %q", cmdLine, flag, value)
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
