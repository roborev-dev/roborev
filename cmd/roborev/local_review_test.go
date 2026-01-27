package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/roborev-dev/roborev/internal/config"
)

// setupTestRepo creates a git repo with a commit for testing
func setupTestRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")

	// Create a test file
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(`package main

func main() {
	println("hello")
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	runGit("add", "main.go")
	runGit("commit", "-m", "initial commit")

	return tmpDir
}

func TestLocalReviewFlag(t *testing.T) {
	// Test that --local flag is recognized
	cmd := reviewCmd()
	cmd.SetArgs([]string{"--local", "--help"})

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "--local") {
		t.Error("Expected --local flag in help output")
	}
	if !strings.Contains(output, "run review locally without daemon") {
		t.Error("Expected --local description in help output")
	}
}

func TestLocalReviewRequiresAgent(t *testing.T) {
	tmpDir := setupTestRepo(t)

	// Create a test command
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	// Test with no available agents (test agent should work)
	err := runLocalReview(cmd, tmpDir, "HEAD", "", "test", "", "fast")
	if err != nil {
		t.Fatalf("Expected no error with test agent, got: %v", err)
	}

	// Verify output contains expected content
	output := out.String()
	if !strings.Contains(output, "Running test review") {
		t.Errorf("Expected 'Running test review' in output, got: %s", output)
	}
}

func TestLocalReviewWithDirtyDiff(t *testing.T) {
	tmpDir := setupTestRepo(t)

	// Create a dirty diff
	diffContent := `diff --git a/test.go b/test.go
new file mode 100644
--- /dev/null
+++ b/test.go
@@ -0,0 +1,3 @@
+package main
+
+func test() {}
`

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runLocalReview(cmd, tmpDir, "dirty", diffContent, "test", "", "fast")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
}

func TestLocalReviewAgentResolution(t *testing.T) {
	tmpDir := setupTestRepo(t)

	// Write a .roborev.toml with agent config
	configPath := filepath.Join(tmpDir, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(`agent = "test"`), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	// Empty agent should resolve from config
	err := runLocalReview(cmd, tmpDir, "HEAD", "", "", "", "fast")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "Running test review") {
		t.Errorf("Expected agent to resolve to 'test', got output: %s", output)
	}
}

func TestLocalReviewModelResolution(t *testing.T) {
	tmpDir := setupTestRepo(t)

	// Write a .roborev.toml with model config
	configPath := filepath.Join(tmpDir, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(`
agent = "test"
model = "test-model"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := runLocalReview(cmd, tmpDir, "HEAD", "", "", "", "fast")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "model: test-model") {
		t.Errorf("Expected model 'test-model' in output, got: %s", output)
	}
}

func TestLocalReviewReasoningLevels(t *testing.T) {
	tmpDir := setupTestRepo(t)

	tests := []struct {
		reasoning string
		expected  string
	}{
		{"fast", "reasoning: fast"},
		{"standard", "reasoning: standard"},
		{"thorough", "reasoning: thorough"},
		{"", "reasoning: thorough"}, // default
	}

	for _, tc := range tests {
		t.Run(tc.reasoning, func(t *testing.T) {
			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)

			err := runLocalReview(cmd, tmpDir, "HEAD", "", "test", "", tc.reasoning)
			if err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}

			output := out.String()
			if !strings.Contains(output, tc.expected) {
				t.Errorf("Expected '%s' in output, got: %s", tc.expected, output)
			}
		})
	}
}

func TestLocalReviewSkipsDaemon(t *testing.T) {
	// This test verifies that --local doesn't try to connect to daemon
	// We do this by checking the code path - if daemon was required,
	// this would fail since no daemon is running

	tmpDir := setupTestRepo(t)

	// Override HOME to prevent reading real daemon.json
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	defer os.Setenv("HOME", origHome)

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	// This should succeed without a daemon
	err := runLocalReview(cmd, tmpDir, "HEAD", "", "test", "", "fast")
	if err != nil {
		t.Fatalf("Expected --local to work without daemon, got: %v", err)
	}
}

// Ensure config package is used (for the linker)
var _ = config.LoadGlobal
