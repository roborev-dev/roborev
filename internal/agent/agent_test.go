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
	agents := []string{"codex", "claude-code", "gemini", "copilot", "opencode", "test"}
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
	// We have 6 agents: codex, claude-code, gemini, copilot, opencode, test
	if len(agents) < 6 {
		t.Errorf("Expected at least 6 agents, got %d: %v", len(agents), agents)
	}

	expected := map[string]bool{
		"codex":       false,
		"claude-code": false,
		"gemini":      false,
		"copilot":     false,
		"opencode":    false,
		"test":        false,
	}

	for _, a := range agents {
		if _, ok := expected[a]; ok {
			expected[a] = true
		}
	}

	for name, found := range expected {
		if !found {
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
