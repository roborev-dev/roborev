package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCopilotReviewPipesPromptViaStdin(t *testing.T) {
	skipIfWindows(t)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines:  []string{"ok"},
	})

	a := NewCopilotAgent(mock.CmdPath)
	prompt := "Review this commit carefully"
	_, err := a.Review(
		context.Background(), t.TempDir(), "HEAD", prompt, nil,
	)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Prompt must be in stdin
	stdin, err := os.ReadFile(mock.StdinFile)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	if strings.TrimSpace(string(stdin)) != prompt {
		t.Errorf("stdin = %q, want %q", string(stdin), prompt)
	}

	// Prompt must not be in argv
	args, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("read args capture: %v", err)
	}
	if strings.Contains(string(args), prompt) {
		t.Errorf("prompt leaked into argv: %s", string(args))
	}
}
