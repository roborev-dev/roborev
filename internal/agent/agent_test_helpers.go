package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// expectedAgents is the single source of truth for registered agent names.
var expectedAgents = []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "test"}

// verifyAgentPassesFlag creates a mock command that echoes args, runs the agent's Review method,
// and validates that the output contains the expected flag and value.
func verifyAgentPassesFlag(t *testing.T, createAgent func(cmdPath string) Agent, expectedFlag, expectedValue string) {
	t.Helper()
	skipIfWindows(t)

	script := "#!/bin/sh\necho \"args: $@\"\n"
	cmdPath := writeTempCommand(t, script)
	agent := createAgent(cmdPath)

	result, err := agent.Review(context.Background(), t.TempDir(), "head", "test prompt", nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	if !strings.Contains(result, expectedFlag) {
		t.Errorf("expected flag %q in args, got: %q", expectedFlag, result)
	}
	if expectedValue != "" && !strings.Contains(result, expectedValue) {
		t.Errorf("expected value %q in args, got: %q", expectedValue, result)
	}
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}

// skipIfWindows skips the test on Windows with a message.
// Use for tests that rely on Unix shell scripts.
func skipIfWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping test that requires Unix shell scripts")
	}
}

func writeTempCommand(t *testing.T, script string) string {
	t.Helper()
	skipIfWindows(t)

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cmd")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		t.Fatalf("write temp command: %v", err)
	}
	if _, err := f.Write([]byte(script)); err != nil {
		f.Close()
		t.Fatalf("write temp command: %v", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		t.Fatalf("write temp command sync: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("write temp command close: %v", err)
	}
	// On Linux (especially under -race), exec can race against the
	// kernel releasing the inode write reference and hit ETXTBSY.
	// Verify the script is execable by attempting a no-op exec.
	// Retry with exponential backoff if we hit the race.
	const maxRetries = 10
	for i := range maxRetries {
		_, err = exec.Command(path, "--help-probe-etxtbsy").CombinedOutput()
		if err == nil || !strings.Contains(err.Error(), "text file busy") {
			break
		}
		if i == maxRetries-1 {
			t.Fatalf("write temp command: ETXTBSY persisted after %d retries", maxRetries)
		}
		time.Sleep(time.Duration(1<<i) * time.Millisecond)
	}
	return path
}

// withUnsafeAgents sets the global AllowUnsafeAgents flag and restores it on cleanup.
func withUnsafeAgents(t *testing.T, allow bool) {
	t.Helper()
	prev := AllowUnsafeAgents()
	SetAllowUnsafeAgents(allow)
	t.Cleanup(func() { SetAllowUnsafeAgents(prev) })
}

// MockCLIOpts controls the behavior of a mock agent CLI script.
type MockCLIOpts struct {
	HelpOutput   string // Output when --help is passed; empty means no --help handling
	ExitCode     int    // Exit code for normal (non-help) invocations
	CaptureArgs  bool   // Write "$@" to a capture file
	CaptureStdin bool   // Write stdin to a capture file
	StdoutLines  []string
	StderrLines  []string
}

// MockCLIResult holds paths to the mock command and any capture files.
type MockCLIResult struct {
	CmdPath   string
	ArgsFile  string // Non-empty when CaptureArgs was set
	StdinFile string // Non-empty when CaptureStdin was set
}

// readMockArgs reads the captured arguments from a mock CLI's ArgsFile and splits them into a slice.
func readMockArgs(t *testing.T, path string) []string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read args file %s: %v", path, err)
	}
	return strings.Split(strings.TrimSpace(string(content)), " ")
}

// assertContainsArg checks that args contains target, failing with a descriptive message.
func assertContainsArg(t *testing.T, args []string, target string) {
	t.Helper()
	if !containsString(args, target) {
		t.Fatalf("expected %q in args, got %v", target, args)
	}
}

// assertNotContainsArg checks that args does not contain target.
func assertNotContainsArg(t *testing.T, args []string, target string) {
	t.Helper()
	if containsString(args, target) {
		t.Fatalf("unexpected %q in args %v", target, args)
	}
}

// mockAgentCLI creates a temporary shell script that simulates an agent CLI.
// It handles --help output, argument/stdin capture, and exit codes.
func mockAgentCLI(t *testing.T, opts MockCLIOpts) *MockCLIResult {
	t.Helper()
	skipIfWindows(t)

	tmpDir := t.TempDir()
	result := &MockCLIResult{}

	var script strings.Builder
	script.WriteString("#!/bin/sh\n")

	// Handle --help: write output to a file and cat it to avoid shell escaping issues
	if opts.HelpOutput != "" {
		helpFile := filepath.Join(tmpDir, "help_output.txt")
		if err := os.WriteFile(helpFile, []byte(opts.HelpOutput), 0644); err != nil {
			t.Fatalf("write help output file: %v", err)
		}
		script.WriteString(fmt.Sprintf(`if [ "$1" = "--help" ]; then cat %q; exit 0; fi`, helpFile) + "\n")
	}

	// Capture args
	if opts.CaptureArgs {
		result.ArgsFile = filepath.Join(tmpDir, "args.txt")
		fmt.Fprintf(&script, "echo \"$@\" > %q\n", result.ArgsFile)
	}

	// Capture stdin
	if opts.CaptureStdin {
		result.StdinFile = filepath.Join(tmpDir, "stdin.txt")
		fmt.Fprintf(&script, "cat > %q\n", result.StdinFile)
	}

	// Emit configured stdout lines
	if len(opts.StdoutLines) > 0 {
		stdoutFile := filepath.Join(tmpDir, "stdout.txt")
		content := strings.Join(opts.StdoutLines, "\n")
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if err := os.WriteFile(stdoutFile, []byte(content), 0644); err != nil {
			t.Fatalf("write stdout file: %v", err)
		}
		script.WriteString(fmt.Sprintf(`cat %q`, stdoutFile) + "\n")
	}

	// Emit configured stderr lines
	if len(opts.StderrLines) > 0 {
		stderrFile := filepath.Join(tmpDir, "stderr.txt")
		content := strings.Join(opts.StderrLines, "\n")
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if err := os.WriteFile(stderrFile, []byte(content), 0644); err != nil {
			t.Fatalf("write stderr file: %v", err)
		}
		script.WriteString(fmt.Sprintf(`cat %q >&2`, stderrFile) + "\n")
	}

	script.WriteString("exit " + strings.TrimSpace(fmt.Sprintf("%d", opts.ExitCode)) + "\n")

	result.CmdPath = writeTempCommand(t, script.String())
	return result
}

// makeToolCallJSON returns a JSON string representing a tool call with the
// given name and arguments. It is useful for building test inputs that
// contain tool-call lines without hardcoding brittle JSON strings.
func makeToolCallJSON(name string, args map[string]any) string {
	m := map[string]any{
		"name":      name,
		"arguments": args,
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("makeToolCallJSON: %v", err))
	}
	return string(b)
}

// assertContains fails the test if s does not contain substr.
func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected string to contain %q\nDocument content:\n%s", substr, s)
	}
}

// assertNotContains fails the test if s contains substr.
func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("expected %q to not contain %q", s, substr)
	}
}

// assertEqual fails the test if got != want.
func assertEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ScriptBuilder helps construct shell scripts for mocking CLI output in tests.
type ScriptBuilder struct {
	lines []string
}

// NewScriptBuilder creates a new ScriptBuilder with the shebang line.
func NewScriptBuilder() *ScriptBuilder {
	return &ScriptBuilder{lines: []string{"#!/bin/sh"}}
}

// AddOutput adds a line that echoes the given string to stdout.
func (b *ScriptBuilder) AddOutput(s string) *ScriptBuilder {
	b.lines = append(b.lines, fmt.Sprintf("echo %q", s))
	return b
}

// AddToolCall adds a line that prints a tool-call JSON object to stdout.
func (b *ScriptBuilder) AddToolCall(name string, args map[string]any) *ScriptBuilder {
	j := makeToolCallJSON(name, args)
	escaped := strings.ReplaceAll(j, "'", "'\\''")
	b.lines = append(b.lines, fmt.Sprintf("printf '%%s\\n' '%s'", escaped))
	return b
}

// AddRaw adds a raw shell line to the script.
func (b *ScriptBuilder) AddRaw(line string) *ScriptBuilder {
	b.lines = append(b.lines, line)
	return b
}

// Build returns the complete shell script as a string.
func (b *ScriptBuilder) Build() string {
	return strings.Join(b.lines, "\n") + "\n"
}

// FailingWriter always returns an error on Write.
type FailingWriter struct {
	Err error
}

func (w *FailingWriter) Write(p []byte) (int, error) {
	return 0, w.Err
}

// assertFileContent reads the file at path and verifies it matches expected content.
func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	if got := strings.TrimSpace(string(content)); got != expected {
		t.Errorf("file %s content = %q, want %q", path, got, expected)
	}
}

// assertFileNotContains reads the file at path and verifies it does NOT contain the unexpected string.
func assertFileNotContains(t *testing.T, path, unexpected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	if strings.Contains(string(content), unexpected) {
		t.Errorf("file %s contained leaked string: %q", path, unexpected)
	}
}

// assertNoError checks that the given error is nil, failing the test with the given message if it is not.
func assertNoError(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

// FakeAgent implements Agent for testing purposes.
type FakeAgent struct {
	NameStr  string
	ReviewFn func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error)
}

func (a *FakeAgent) Name() string { return a.NameStr }
func (a *FakeAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	if a.ReviewFn != nil {
		return a.ReviewFn(ctx, repoPath, commitSHA, prompt, output)
	}
	return "", nil
}
func (a *FakeAgent) WithReasoning(level ReasoningLevel) Agent { return a }
func (a *FakeAgent) WithAgentic(agentic bool) Agent           { return a }
func (a *FakeAgent) WithModel(model string) Agent             { return a }
func (a *FakeAgent) CommandLine() string                      { return "" }
