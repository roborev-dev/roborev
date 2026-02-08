package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// reviewHarness encapsulates a test git repo, cobra command, and output buffer.
type reviewHarness struct {
	t   *testing.T
	Dir string
	Cmd *cobra.Command
	Out *bytes.Buffer
}

func newReviewHarness(t *testing.T) *reviewHarness {
	t.Helper()
	repo := testutil.NewTestRepoWithCommit(t)
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	return &reviewHarness{t: t, Dir: repo.Root, Cmd: cmd, Out: &out}
}

// writeConfig writes a .roborev.toml in the repo directory.
func (h *reviewHarness) writeConfig(content string) {
	h.t.Helper()
	path := filepath.Join(h.Dir, ".roborev.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		h.t.Fatal(err)
	}
}

// runOpts holds optional parameters for runLocalReview, with sensible defaults.
type runOpts struct {
	Revision   string
	Diff       string
	Agent      string
	Model      string
	Reasoning  string
	ReviewType string
	Quiet      bool
}

// run calls runLocalReview with defaults applied.
func (h *reviewHarness) run(opts runOpts) error {
	h.t.Helper()
	if opts.Revision == "" {
		opts.Revision = "HEAD"
	}
	return runLocalReview(h.Cmd, h.Dir, opts.Revision, opts.Diff, opts.Agent, opts.Model, opts.Reasoning, opts.ReviewType, opts.Quiet)
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
	h := newReviewHarness(t)

	err := h.run(runOpts{Agent: "test", Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected no error with test agent, got: %v", err)
	}

	output := h.Out.String()
	if !strings.Contains(output, "Running test review") {
		t.Errorf("Expected 'Running test review' in output, got: %s", output)
	}
}

func TestLocalReviewWithDirtyDiff(t *testing.T) {
	h := newReviewHarness(t)

	diffContent := `diff --git a/test.go b/test.go
new file mode 100644
--- /dev/null
+++ b/test.go
@@ -0,0 +1,3 @@
+package main
+
+func test() {}
`

	err := h.run(runOpts{Revision: "dirty", Diff: diffContent, Agent: "test", Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
}

func TestLocalReviewAgentResolution(t *testing.T) {
	h := newReviewHarness(t)
	h.writeConfig(`agent = "test"`)

	err := h.run(runOpts{Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	output := h.Out.String()
	if !strings.Contains(output, "Running test review") {
		t.Errorf("Expected agent to resolve to 'test', got output: %s", output)
	}
}

func TestLocalReviewModelResolution(t *testing.T) {
	h := newReviewHarness(t)
	h.writeConfig(`
agent = "test"
model = "test-model"
`)

	err := h.run(runOpts{Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	output := h.Out.String()
	if !strings.Contains(output, "model: test-model") {
		t.Errorf("Expected model 'test-model' in output, got: %s", output)
	}
}

func TestLocalReviewReasoningLevels(t *testing.T) {
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
			h := newReviewHarness(t)
			err := h.run(runOpts{Agent: "test", Reasoning: tc.reasoning})
			if err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}

			output := h.Out.String()
			if !strings.Contains(output, tc.expected) {
				t.Errorf("Expected '%s' in output, got: %s", tc.expected, output)
			}
		})
	}
}

func TestLocalReviewInvalidReasoning(t *testing.T) {
	h := newReviewHarness(t)

	err := h.run(runOpts{Agent: "test", Reasoning: "invalid-reasoning"})
	if err == nil {
		t.Fatal("Expected error for invalid reasoning")
	}
	if !strings.Contains(err.Error(), "invalid reasoning") {
		t.Errorf("Expected 'invalid reasoning' in error, got: %v", err)
	}
}

func TestLocalReviewWorkflowSpecificAgent(t *testing.T) {
	h := newReviewHarness(t)
	h.writeConfig(`
agent = "codex"
review_agent_fast = "test"
`)

	err := h.run(runOpts{Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	output := h.Out.String()
	if !strings.Contains(output, "Running test review") {
		t.Errorf("Expected workflow-specific agent 'test', got output: %s", output)
	}
}

func TestLocalReviewWorkflowSpecificModel(t *testing.T) {
	h := newReviewHarness(t)
	h.writeConfig(`
agent = "test"
model = "default-model"
review_model_thorough = "thorough-model"
`)

	err := h.run(runOpts{})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	output := h.Out.String()
	if !strings.Contains(output, "model: thorough-model") {
		t.Errorf("Expected workflow-specific model 'thorough-model', got output: %s", output)
	}
}

func TestLocalReviewReasoningFromConfig(t *testing.T) {
	h := newReviewHarness(t)
	h.writeConfig(`
agent = "test"
review_reasoning = "fast"
`)

	err := h.run(runOpts{})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	output := h.Out.String()
	if !strings.Contains(output, "reasoning: fast") {
		t.Errorf("Expected reasoning to resolve to 'fast' from config, got output: %s", output)
	}
}

func TestLocalReviewQuietMode(t *testing.T) {
	h := newReviewHarness(t)

	err := h.run(runOpts{Agent: "test", Reasoning: "fast", Quiet: true})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	output := h.Out.String()
	if strings.Contains(output, "Running") {
		t.Errorf("Expected no 'Running' message in quiet mode, got: %s", output)
	}
}

func TestLocalReviewSkipsDaemon(t *testing.T) {
	h := newReviewHarness(t)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	defer os.Setenv("HOME", origHome)

	err := h.run(runOpts{Agent: "test", Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected --local to work without daemon, got: %v", err)
	}
}

// Ensure config package is used (for the linker)
var _ = config.LoadGlobal
