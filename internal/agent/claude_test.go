package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestClaudeBuildArgsUnsafeOptIn(t *testing.T) {
	t.Cleanup(func() { SetAllowUnsafeAgents(false) })

	a := NewClaudeAgent("claude")

	SetAllowUnsafeAgents(false)
	args := a.buildArgs("prompt")
	if containsString(args, claudeDangerousFlag) {
		t.Fatalf("expected no unsafe flag when disabled, got %v", args)
	}

	SetAllowUnsafeAgents(true)
	args = a.buildArgs("prompt")
	if !containsString(args, claudeDangerousFlag) {
		t.Fatalf("expected unsafe flag when enabled, got %v", args)
	}
}

func TestClaudeSupportsDangerousFlagAllowsNonZeroHelp(t *testing.T) {
	cmdPath := writeTempCommand(t, "#!/bin/sh\necho \"usage "+claudeDangerousFlag+"\"; exit 1\n")

	supported, err := claudeSupportsDangerousFlag(context.Background(), cmdPath)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !supported {
		t.Fatalf("expected dangerous flag support, got false")
	}
}

func TestClaudeReviewUnsafeMissingFlagErrors(t *testing.T) {
	t.Cleanup(func() { SetAllowUnsafeAgents(false) })

	cmdPath := writeTempCommand(t, "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then echo \"usage\"; exit 0; fi\nexit 0\n")
	SetAllowUnsafeAgents(true)

	a := NewClaudeAgent(cmdPath)
	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("expected unsupported flag error, got %v", err)
	}
}

func TestClaudeReviewNonTTYErrorsWhenUnsafeDisabled(t *testing.T) {
	prevAllowUnsafe := AllowUnsafeAgents()
	SetAllowUnsafeAgents(false)
	t.Cleanup(func() { SetAllowUnsafeAgents(prevAllowUnsafe) })

	stdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = stdin
		r.Close()
		w.Close()
	})

	a := NewClaudeAgent("claude")
	_, err = a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "requires a TTY") {
		t.Fatalf("expected TTY error, got %v", err)
	}
}
