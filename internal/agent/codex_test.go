package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexBuildArgsUnsafeOptIn(t *testing.T) {
	a := NewCodexAgent("codex")

	// Test non-agentic mode with auto-approve
	args := a.buildArgs("/repo", "/tmp/out", false, true)
	if containsString(args, codexDangerousFlag) {
		t.Fatalf("expected no unsafe flag when agentic=false, got %v", args)
	}
	if !containsString(args, codexAutoApproveFlag) {
		t.Fatalf("expected auto-approve flag when autoApprove=true, got %v", args)
	}

	// Verify stdin marker "-" is at the end (after all flags)
	if args[len(args)-1] != "-" {
		t.Fatalf("expected stdin marker '-' at end of args, got %v", args)
	}

	// Test agentic mode (with dangerous flag, no auto-approve)
	args = a.buildArgs("/repo", "/tmp/out", true, false)
	if !containsString(args, codexDangerousFlag) {
		t.Fatalf("expected unsafe flag when agentic=true, got %v", args)
	}
	if containsString(args, codexAutoApproveFlag) {
		t.Fatalf("expected no auto-approve flag when autoApprove=false, got %v", args)
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

func TestCodexReviewAlwaysAddsAutoApprove(t *testing.T) {
	// Since we always pipe stdin, --full-auto is always required in non-agentic mode
	prevAllowUnsafe := AllowUnsafeAgents()
	SetAllowUnsafeAgents(false)
	t.Cleanup(func() { SetAllowUnsafeAgents(prevAllowUnsafe) })

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

func TestCodexReviewPipesPromptViaStdin(t *testing.T) {
	// Verify prompt is actually delivered via stdin
	prevAllowUnsafe := AllowUnsafeAgents()
	SetAllowUnsafeAgents(false)
	t.Cleanup(func() { SetAllowUnsafeAgents(prevAllowUnsafe) })

	tmpDir := t.TempDir()
	stdinFile := filepath.Join(tmpDir, "stdin.txt")
	t.Setenv("STDIN_FILE", stdinFile)

	// Script that reads stdin and writes to file
	cmdPath := writeTempCommand(t, `#!/bin/sh
if [ "$1" = "--help" ]; then echo "usage `+codexAutoApproveFlag+`"; exit 0; fi
cat > "$STDIN_FILE"
exit 0
`)
	a := NewCodexAgent(cmdPath)

	testPrompt := "This is a test prompt with special chars: <>&\nand newlines"
	if _, err := a.Review(context.Background(), t.TempDir(), "deadbeef", testPrompt, nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	received, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin file: %v", err)
	}
	if string(received) != testPrompt {
		t.Fatalf("prompt not piped correctly via stdin\nexpected: %q\ngot: %q", testPrompt, string(received))
	}
}
