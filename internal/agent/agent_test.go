package agent

import (
	"bytes"
	"context"
	"errors"
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
		for i := 0; i < 100; i++ {
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
	t.Run("streams output to writer", func(t *testing.T) {
		agent := &TestAgent{
			Delay:  1 * time.Millisecond,
			Output: "test output",
		}

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
		agent := &TestAgent{
			Delay:  1 * time.Millisecond,
			Output: "test output",
		}

		result, err := agent.Review(context.Background(), "/tmp", "abc1234567", "prompt", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == "" {
			t.Error("expected non-empty result")
		}
	})

	t.Run("returns write error", func(t *testing.T) {
		agent := &TestAgent{
			Delay:  1 * time.Millisecond,
			Output: "test output",
		}

		errWriter := &failingWriter{err: errors.New("write failed")}
		_, err := agent.Review(context.Background(), "/tmp", "abc1234567", "prompt", errWriter)
		if err == nil {
			t.Fatal("expected error from failing writer")
		}
		if !errors.Is(err, errWriter.err) {
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

// failingWriter always returns an error on Write
type failingWriter struct {
	err error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	return 0, w.err
}

// getAgentModel extracts the model from any agent type
func getAgentModel(a Agent) string {
	switch v := a.(type) {
	case *CodexAgent:
		return v.Model
	case *ClaudeAgent:
		return v.Model
	case *GeminiAgent:
		return v.Model
	case *CopilotAgent:
		return v.Model
	case *OpenCodeAgent:
		return v.Model
	case *CursorAgent:
		return v.Model
	default:
		return ""
	}
}

func TestAgentWithModelPersistence(t *testing.T) {
	tests := []struct {
		name      string
		newAgent  func() Agent
		model     string
		wantModel string
	}{
		{"codex", func() Agent { return NewCodexAgent("") }, "o3", "o3"},
		{"claude", func() Agent { return NewClaudeAgent("") }, "opus", "opus"},
		{"gemini", func() Agent { return NewGeminiAgent("") }, "gemini-3-pro-preview", "gemini-3-pro-preview"},
		{"copilot", func() Agent { return NewCopilotAgent("") }, "gpt-4o", "gpt-4o"},
		{"opencode", func() Agent { return NewOpenCodeAgent("") }, "anthropic/claude-sonnet-4", "anthropic/claude-sonnet-4"},
		{"cursor", func() Agent { return NewCursorAgent("") }, "claude-sonnet-4", "claude-sonnet-4"},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/WithModel sets model", func(t *testing.T) {
			a := tt.newAgent().WithModel(tt.model)
			if got := getAgentModel(a); got != tt.wantModel {
				t.Errorf("got model %q, want %q", got, tt.wantModel)
			}
		})

		t.Run(tt.name+"/model persists through WithReasoning", func(t *testing.T) {
			a := tt.newAgent().WithModel(tt.model).WithReasoning(ReasoningThorough)
			if got := getAgentModel(a); got != tt.wantModel {
				t.Errorf("after WithReasoning: got model %q, want %q", got, tt.wantModel)
			}
		})

		t.Run(tt.name+"/model persists through WithAgentic", func(t *testing.T) {
			a := tt.newAgent().WithModel(tt.model).WithAgentic(true)
			if got := getAgentModel(a); got != tt.wantModel {
				t.Errorf("after WithAgentic: got model %q, want %q", got, tt.wantModel)
			}
		})

		t.Run(tt.name+"/model persists through chained calls", func(t *testing.T) {
			a := tt.newAgent().WithModel(tt.model).WithReasoning(ReasoningFast).WithAgentic(true)
			if got := getAgentModel(a); got != tt.wantModel {
				t.Errorf("after chained calls: got model %q, want %q", got, tt.wantModel)
			}
		})
	}
}

func TestWithModelEmptyPreservesDefault(t *testing.T) {
	tests := []struct {
		name         string
		newAgent     func() Agent
		defaultModel string
	}{
		{"codex", func() Agent { return NewCodexAgent("") }, ""},
		{"claude", func() Agent { return NewClaudeAgent("") }, ""},
		{"gemini", func() Agent { return NewGeminiAgent("") }, "gemini-3-pro-preview"},
		{"copilot", func() Agent { return NewCopilotAgent("") }, ""},
		{"opencode", func() Agent { return NewOpenCodeAgent("") }, ""},
		{"cursor", func() Agent { return NewCursorAgent("") }, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := tt.newAgent()
			b := a.WithModel("")
			if got := getAgentModel(b); got != tt.defaultModel {
				t.Errorf("WithModel(\"\") changed model from %q to %q", tt.defaultModel, got)
			}
		})

		t.Run(tt.name+"/explicit then empty preserves explicit", func(t *testing.T) {
			a := tt.newAgent().WithModel("custom-model")
			b := a.WithModel("")
			if got := getAgentModel(b); got != "custom-model" {
				t.Errorf("WithModel(\"\") after WithModel(\"custom-model\"): got %q, want %q", got, "custom-model")
			}
		})
	}
}

// containsSequence checks if args contains needle1 immediately followed by needle2
func containsSequence(args []string, needle1, needle2 string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == needle1 && args[i+1] == needle2 {
			return true
		}
	}
	return false
}

func TestAgentBuildArgsWithModel(t *testing.T) {
	tests := []struct {
		name     string
		buildFn  func(model string) []string
		flag     string // e.g. "-m" or "--model"
		model    string
		wantFlag bool
	}{
		{
			name: "codex with model",
			buildFn: func(model string) []string {
				return (&CodexAgent{Model: model}).buildArgs("/tmp", "/tmp/out.txt", false, true)
			},
			flag: "-m", model: "o3", wantFlag: true,
		},
		{
			name: "codex without model",
			buildFn: func(model string) []string {
				return (&CodexAgent{Model: model}).buildArgs("/tmp", "/tmp/out.txt", false, true)
			},
			flag: "-m", model: "", wantFlag: false,
		},
		{
			name: "claude with model",
			buildFn: func(model string) []string {
				return (&ClaudeAgent{Model: model}).buildArgs(false)
			},
			flag: "--model", model: "opus", wantFlag: true,
		},
		{
			name: "claude without model",
			buildFn: func(model string) []string {
				return (&ClaudeAgent{Model: model}).buildArgs(false)
			},
			flag: "--model", model: "", wantFlag: false,
		},
		{
			name: "gemini with model",
			buildFn: func(model string) []string {
				return (&GeminiAgent{Model: model}).buildArgs(false)
			},
			flag: "-m", model: "gemini-3-pro-preview", wantFlag: true,
		},
		{
			name: "gemini without model",
			buildFn: func(model string) []string {
				return (&GeminiAgent{Model: model}).buildArgs(false)
			},
			flag: "-m", model: "", wantFlag: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.buildFn(tt.model)
			hasFlag := containsString(args, tt.flag)
			if tt.wantFlag {
				if !hasFlag {
					t.Errorf("expected flag %q in args %v", tt.flag, args)
				}
				if !containsSequence(args, tt.flag, tt.model) {
					t.Errorf("expected %q %q sequence in args %v", tt.flag, tt.model, args)
				}
			} else {
				if hasFlag {
					t.Errorf("expected no flag %q in args %v", tt.flag, args)
				}
			}
		})
	}
}

func TestCodexBuildArgsModelWithReasoning(t *testing.T) {
	a := &CodexAgent{Model: "o4-mini", Reasoning: ReasoningThorough}
	args := a.buildArgs("/tmp", "/tmp/out.txt", false, true)

	if !containsSequence(args, "-m", "o4-mini") {
		t.Errorf("expected -m o4-mini in args %v", args)
	}
	if !containsSequence(args, "-c", `model_reasoning_effort="high"`) {
		t.Errorf("expected reasoning effort config in args %v", args)
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
