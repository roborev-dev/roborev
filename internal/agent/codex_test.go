package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexBuildArgsUnsafeOptIn(t *testing.T) {
	t.Cleanup(func() { SetAllowUnsafeAgents(false) })

	a := NewCodexAgent("codex")

	SetAllowUnsafeAgents(false)
	args := a.buildArgs("/repo", "/tmp/out", "prompt")
	if containsString(args, codexDangerousFlag) {
		t.Fatalf("expected no unsafe flag when disabled, got %v", args)
	}

	SetAllowUnsafeAgents(true)
	args = a.buildArgs("/repo", "/tmp/out", "prompt")
	if !containsString(args, codexDangerousFlag) {
		t.Fatalf("expected unsafe flag when enabled, got %v", args)
	}
}

func TestCodexSupportsDangerousFlagAllowsNonZeroHelp(t *testing.T) {
	cmdPath := writeTempCommand(t, "#!/bin/sh\necho \"usage "+codexDangerousFlag+"\"; exit 1\n")

	supported, err := codexSupportsDangerousFlag(context.Background(), cmdPath)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !supported {
		t.Fatalf("expected dangerous flag support, got false")
	}
}

func TestCodexReviewUnsafeMissingFlagErrors(t *testing.T) {
	t.Cleanup(func() { SetAllowUnsafeAgents(false) })

	cmdPath := writeTempCommand(t, "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then echo \"usage\"; exit 0; fi\nexit 0\n")
	SetAllowUnsafeAgents(true)

	a := NewCodexAgent(cmdPath)
	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("expected unsupported flag error, got %v", err)
	}
}

func TestCodexReviewNonTTYAddsAutoApprove(t *testing.T) {
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

	tmpDir := t.TempDir()
	argsFile := filepath.Join(tmpDir, "args.txt")
	t.Setenv("ARGS_FILE", argsFile)

	cmdPath := writeTempCommand(t, "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then echo \"usage "+codexAutoApproveFlag+"\"; exit 0; fi\necho \"$@\" > \"$ARGS_FILE\"\nexit 0\n")
	a := NewCodexAgent(cmdPath)

	if _, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(args), codexAutoApproveFlag) {
		t.Fatalf("expected %s in args, got %s", codexAutoApproveFlag, strings.TrimSpace(string(args)))
	}
}
