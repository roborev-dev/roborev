package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/roborev-dev/roborev/internal/testutil"
)

// reviewHarness encapsulates a test git repo, cobra command, and output buffer.
type reviewHarness struct {
	t   *testing.T
	Dir string
	Cmd *cobra.Command
	Out *bytes.Buffer
}

// newReviewHarness creates a harness with the real CLI command structure.
func newReviewHarness(t *testing.T) *reviewHarness {
	t.Helper()
	repo := testutil.NewTestRepoWithCommit(t)
	cmd := reviewCmd() // Use real factory
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
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

// runCmd executes the command with the given arguments.
func (h *reviewHarness) runCmd(args ...string) error {
	h.t.Helper()
	h.Cmd.SetArgs(args)
	return h.Cmd.Execute()
}

// assertOutputContains checks if the output contains the expected string.
func (h *reviewHarness) assertOutputContains(want string) {
	h.t.Helper()
	if got := h.Out.String(); !strings.Contains(got, want) {
		h.t.Errorf("Output missing %q. Got:\n%s", want, got)
	}
}

// assertOutputNotContains checks if the output does not contain the forbidden string.
func (h *reviewHarness) assertOutputNotContains(want string) {
	h.t.Helper()
	if got := h.Out.String(); strings.Contains(got, want) {
		h.t.Errorf("Output should not contain %q. Got:\n%s", want, got)
	}
}

// assertErrorContains checks if the error contains the expected string.
func (h *reviewHarness) assertErrorContains(err error, want string) {
	h.t.Helper()
	if err == nil {
		h.t.Fatal("Expected error, got nil")
	}
	if !strings.Contains(err.Error(), want) {
		h.t.Errorf("Error missing %q. Got: %v", want, err)
	}
}

// runOpts holds optional parameters for run, mapping to CLI flags.
type runOpts struct {
	Revision   string
	Agent      string
	Model      string
	Reasoning  string
	ReviewType string
	Quiet      bool
	Dirty      bool
}

// run converts opts to CLI flags and executes the command.
func (h *reviewHarness) run(opts runOpts) error {
	h.t.Helper()
	// Always enforce --local and --repo to ensure we target the test repo and don't need daemon
	args := []string{"--local", "--repo", h.Dir}

	if opts.Revision != "" {
		args = append(args, opts.Revision)
	}
	if opts.Dirty {
		args = append(args, "--dirty")
	}
	if opts.Agent != "" {
		args = append(args, "--agent", opts.Agent)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Reasoning != "" {
		args = append(args, "--reasoning", opts.Reasoning)
	}
	if opts.ReviewType != "" {
		args = append(args, "--type", opts.ReviewType)
	}
	if opts.Quiet {
		args = append(args, "--quiet")
	}

	return h.runCmd(args...)
}

func TestLocalReviewFlag(t *testing.T) {
	h := newReviewHarness(t)
	// Passing --help to cobra usually returns nil error but prints usage.

	if err := h.runCmd("--local", "--help"); err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	h.assertOutputContains("--local")
	h.assertOutputContains("run review locally without daemon")
}

func TestLocalReviewRequiresAgent(t *testing.T) {
	h := newReviewHarness(t)

	err := h.run(runOpts{Agent: "test", Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected no error with test agent, got: %v", err)
	}

	h.assertOutputContains("Running test review")
}

func TestLocalReviewWithDirtyDiff(t *testing.T) {
	h := newReviewHarness(t)

	// Create a new file to make the repo dirty
	newFile := filepath.Join(h.Dir, "newfile.go")
	if err := os.WriteFile(newFile, []byte("package main\nfunc test() {}"), 0644); err != nil {
		t.Fatal(err)
	}

	err := h.run(runOpts{Dirty: true, Agent: "test", Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	// We don't check output for diff content because agent output is mocked.
	h.assertOutputContains("Commit: dirty")
}

func TestLocalReviewAgentResolution(t *testing.T) {
	h := newReviewHarness(t)
	h.writeConfig(`agent = "test"`)

	err := h.run(runOpts{Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	h.assertOutputContains("Running test review")
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

	h.assertOutputContains("model: test-model")
}

func TestLocalReviewValidation(t *testing.T) {
	tests := []struct {
		name       string
		opts       runOpts
		wantErr    string
		wantOutput string
	}{
		{
			name:    "Invalid Reasoning",
			opts:    runOpts{Agent: "test", Reasoning: "invalid-reasoning"},
			wantErr: "invalid reasoning",
		},
		{
			name:    "Invalid Type",
			opts:    runOpts{Agent: "test", Reasoning: "fast", ReviewType: "bogus"},
			wantErr: "invalid --type",
		},
		{
			name: "Valid Security Type",
			opts: runOpts{Agent: "test", Reasoning: "fast", ReviewType: "security"},
		},
		{
			name: "Valid Design Type",
			opts: runOpts{Agent: "test", Reasoning: "fast", ReviewType: "design"},
		},
		{
			name: "Empty Type Accepted",
			opts: runOpts{Agent: "test", Reasoning: "fast", ReviewType: ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newReviewHarness(t)
			err := h.run(tc.opts)
			if tc.wantErr != "" {
				h.assertErrorContains(err, tc.wantErr)
			} else if err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}
			if tc.wantOutput != "" {
				h.assertOutputContains(tc.wantOutput)
			}
		})
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

	h.assertOutputContains("Running test review")
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

	h.assertOutputContains("model: thorough-model")
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

	h.assertOutputContains("reasoning: fast")
}

func TestLocalReviewQuietMode(t *testing.T) {
	h := newReviewHarness(t)

	err := h.run(runOpts{Agent: "test", Reasoning: "fast", Quiet: true})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	h.assertOutputNotContains("Running")
}

func TestLocalReviewSkipsDaemon(t *testing.T) {
	h := newReviewHarness(t)

	t.Setenv("HOME", t.TempDir())

	err := h.run(runOpts{Agent: "test", Reasoning: "fast"})
	if err != nil {
		t.Fatalf("Expected --local to work without daemon, got: %v", err)
	}
}
