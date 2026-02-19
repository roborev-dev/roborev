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

func TestAgentWithModelPersistence(t *testing.T) {
	tests := []struct {
		name      string
		newAgent  func() Agent
		flag      string
		model     string
		wantModel string
	}{
		{"codex", func() Agent { return NewCodexAgent("") }, "-m", "o3", "o3"},
		{"claude", func() Agent { return NewClaudeAgent("") }, "--model", "opus", "opus"},
		{"gemini", func() Agent { return NewGeminiAgent("") }, "-m", "gemini-3-pro-preview", "gemini-3-pro-preview"},
		{"copilot", func() Agent { return NewCopilotAgent("") }, "--model", "gpt-4o", "gpt-4o"},
		{"opencode", func() Agent { return NewOpenCodeAgent("") }, "--model", "anthropic/claude-sonnet-4", "anthropic/claude-sonnet-4"},
		{"cursor", func() Agent { return NewCursorAgent("") }, "--model", "claude-sonnet-4", "claude-sonnet-4"},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/WithModel sets model", func(t *testing.T) {
			a := tt.newAgent().WithModel(tt.model)
			cmdLine := a.CommandLine()
			assertArgsContain(t, cmdLine, tt.flag, tt.wantModel)
		})

		t.Run(tt.name+"/model persists through WithReasoning", func(t *testing.T) {
			a := tt.newAgent().WithModel(tt.model).WithReasoning(ReasoningThorough)
			cmdLine := a.CommandLine()
			assertArgsContain(t, cmdLine, tt.flag, tt.wantModel)
		})

		t.Run(tt.name+"/model persists through WithAgentic", func(t *testing.T) {
			a := tt.newAgent().WithModel(tt.model).WithAgentic(true)
			cmdLine := a.CommandLine()
			assertArgsContain(t, cmdLine, tt.flag, tt.wantModel)
		})

		t.Run(tt.name+"/model persists through chained calls", func(t *testing.T) {
			a := tt.newAgent().WithModel(tt.model).WithReasoning(ReasoningFast).WithAgentic(true)
			cmdLine := a.CommandLine()
			assertArgsContain(t, cmdLine, tt.flag, tt.wantModel)
		})
	}
}

func TestWithModelEmptyPreservesDefault(t *testing.T) {
	tests := []struct {
		name         string
		newAgent     func() Agent
		defaultFlag  string
		defaultModel string
	}{
		{"codex", func() Agent { return NewCodexAgent("") }, "-m", ""},                       // No flag expected
		{"claude", func() Agent { return NewClaudeAgent("") }, "--model", ""},                // No flag expected
		{"gemini", func() Agent { return NewGeminiAgent("") }, "-m", "gemini-3-pro-preview"}, // Default flag expected
		{"copilot", func() Agent { return NewCopilotAgent("") }, "--model", ""},
		{"opencode", func() Agent { return NewOpenCodeAgent("") }, "--model", ""},
		{"cursor", func() Agent { return NewCursorAgent("") }, "--model", "auto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := tt.newAgent()
			b := a.WithModel("")
			cmdLine := b.CommandLine()

			if tt.defaultModel == "" {
				// Expect flag NOT to be present (unless it's set to something else, but here we expect clean slate)
				// Check for flag followed by space to be sure we aren't matching partials
				if strings.Contains(cmdLine, tt.defaultFlag+" ") {
					t.Errorf("WithModel(\"\") produced flag %q in %q, expected none", tt.defaultFlag, cmdLine)
				}
			} else {
				expected := tt.defaultFlag + " " + tt.defaultModel
				if !strings.Contains(cmdLine, expected) {
					t.Errorf("WithModel(\"\") produced %q, expected default %q", cmdLine, expected)
				}
			}
		})

		t.Run(tt.name+"/explicit then empty preserves explicit", func(t *testing.T) {
			a := tt.newAgent().WithModel("custom-model")
			b := a.WithModel("")
			cmdLine := b.CommandLine()
			expected := tt.defaultFlag + " " + "custom-model"

			if !strings.Contains(cmdLine, expected) {
				t.Errorf("WithModel(\"\") after explicit lost model. Got %q, want %q", cmdLine, expected)
			}
		})
	}
}

func TestAgentBuildArgsWithModel(t *testing.T) {
	type agentFactory func(model string) Agent

	tests := []struct {
		name    string
		factory agentFactory
		flag    string
		model   string
		want    bool
	}{
		{
			name:    "codex with model",
			factory: func(m string) Agent { return NewCodexAgent("").WithModel(m) },
			flag:    "-m",
			model:   "o3",
			want:    true,
		},
		{
			name:    "codex without model",
			factory: func(m string) Agent { return NewCodexAgent("").WithModel(m) },
			flag:    "-m",
			model:   "",
			want:    false,
		},
		{
			name:    "claude with model",
			factory: func(m string) Agent { return NewClaudeAgent("").WithModel(m) },
			flag:    "--model",
			model:   "opus",
			want:    true,
		},
		{
			name:    "claude without model",
			factory: func(m string) Agent { return NewClaudeAgent("").WithModel(m) },
			flag:    "--model",
			model:   "",
			want:    false,
		},
		{
			name:    "gemini with model",
			factory: func(m string) Agent { return NewGeminiAgent("").WithModel(m) },
			flag:    "-m",
			model:   "gemini-3-pro-preview",
			want:    true,
		},
		{
			name:    "gemini default",
			factory: func(m string) Agent { return NewGeminiAgent("") },
			flag:    "-m",
			model:   "gemini-3-pro-preview",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.factory(tt.model)
			cmdLine := agent.CommandLine()
			hasFlag := strings.Contains(cmdLine, tt.flag)

			if tt.want {
				if !hasFlag {
					t.Errorf("expected flag %q in command line %q", tt.flag, cmdLine)
				}
				assertArgsContain(t, cmdLine, tt.flag, tt.model)
			} else {
				if hasFlag {
					t.Errorf("expected no flag %q in command line %q", tt.flag, cmdLine)
				}
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

	tests := []struct {
		name       string
		createFunc func(cmdPath string) Agent
		helpOutput string
		jsonOutput string
		flag       string
		value      string
	}{
		{
			name:       "codex",
			createFunc: func(p string) Agent { return NewCodexAgent(p).WithModel("o3") },
			helpOutput: "--full-auto",
			jsonOutput: `{"type": "item.completed", "item": {"type": "agent_message", "text": "review result"}}`,
			flag:       "-m",
			value:      "o3",
		},
		{
			name:       "claude",
			createFunc: func(p string) Agent { return NewClaudeAgent(p).WithModel("opus") },
			helpOutput: "", // Non-agentic mode doesn't check help
			jsonOutput: `{"type": "result", "result": "review result"}`,
			flag:       "--model",
			value:      "opus",
		},
		{
			name:       "gemini",
			createFunc: func(p string) Agent { return NewGeminiAgent(p).WithModel("gemini-1.5-pro") },
			helpOutput: "",
			jsonOutput: `{"type": "result", "result": "review result"}`,
			flag:       "-m",
			value:      "gemini-1.5-pro",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := MockCLIOpts{
				HelpOutput:  tt.helpOutput,
				CaptureArgs: true,
				StdoutLines: []string{tt.jsonOutput},
			}
			mock := mockAgentCLI(t, opts)

			agent := tt.createFunc(mock.CmdPath)
			_, err := agent.Review(context.Background(), t.TempDir(), "head", "prompt", nil)
			if err != nil {
				t.Fatalf("Review failed: %v", err)
			}

			argsBytes, err := os.ReadFile(mock.ArgsFile)
			if err != nil {
				t.Fatalf("read args file: %v", err)
			}
			args := string(argsBytes)

			assertArgsContain(t, args, tt.flag, tt.value)
		})
	}
}

func TestAgentReviewPassesModelFlag(t *testing.T) {
	tests := []struct {
		name       string
		createFunc func(cmdPath string) Agent
		flag       string
		value      string
	}{
		{"opencode", func(p string) Agent { return NewOpenCodeAgent(p).WithModel("anthropic/claude-sonnet-4") }, "--model", "anthropic/claude-sonnet-4"},
		{"copilot", func(p string) Agent { return NewCopilotAgent(p).WithModel("gpt-4o") }, "--model", "gpt-4o"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifyAgentPassesFlag(t, tt.createFunc, tt.flag, tt.value)
		})
	}
}
