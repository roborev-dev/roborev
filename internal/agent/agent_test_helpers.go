package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write temp command: %v", err)
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
}

// MockCLIResult holds paths to the mock command and any capture files.
type MockCLIResult struct {
	CmdPath   string
	ArgsFile  string // Non-empty when CaptureArgs was set
	StdinFile string // Non-empty when CaptureStdin was set
}

// newMockDroidAgent creates a DroidAgent backed by a temporary shell script.
// Returns the agent and the temp directory used by writeTempCommand.
func newMockDroidAgent(t *testing.T, scriptContent string) *DroidAgent {
	t.Helper()
	cmdPath := writeTempCommand(t, scriptContent)
	return NewDroidAgent(cmdPath)
}

// runReviewScenario creates a mock DroidAgent from a shell script, runs Review,
// and returns the result and error.
func runReviewScenario(t *testing.T, script, prompt string) (string, error) {
	t.Helper()
	a := newMockDroidAgent(t, script)
	return a.Review(context.Background(), t.TempDir(), "deadbeef", prompt, nil)
}

// assertArgsOrder verifies that each element in sequence appears in args
// in strictly increasing index order.
func assertArgsOrder(t *testing.T, args []string, sequence ...string) {
	t.Helper()
	lastIdx := -1
	for _, want := range sequence {
		found := false
		for i := lastIdx + 1; i < len(args); i++ {
			if args[i] == want {
				lastIdx = i
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %q after index %d in args %v", want, lastIdx, args)
		}
	}
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
		t.Setenv("MOCK_ARGS_FILE", result.ArgsFile)
		script.WriteString(`echo "$@" > "$MOCK_ARGS_FILE"` + "\n")
	}

	// Capture stdin
	if opts.CaptureStdin {
		result.StdinFile = filepath.Join(tmpDir, "stdin.txt")
		t.Setenv("MOCK_STDIN_FILE", result.StdinFile)
		script.WriteString(`cat > "$MOCK_STDIN_FILE"` + "\n")
	}

	script.WriteString("exit " + strings.TrimSpace(fmt.Sprintf("%d", opts.ExitCode)) + "\n")

	result.CmdPath = writeTempCommand(t, script.String())
	return result
}

// makeToolCallJSON returns a JSON string representing a tool call with the
// given name and arguments. It is useful for building test inputs that
// contain tool-call lines without hardcoding brittle JSON strings.
func makeToolCallJSON(name string, args map[string]interface{}) string {
	m := map[string]interface{}{
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
		t.Errorf("expected %q to contain %q", s, substr)
	}
}

// assertNotContains fails the test if s contains substr.
func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("expected %q to not contain %q", s, substr)
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
func (b *ScriptBuilder) AddToolCall(name string, args map[string]interface{}) *ScriptBuilder {
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
