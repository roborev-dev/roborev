# Kilo Agent Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add Kilo CLI (`kilo run`) as a roborev code review agent.

**Architecture:** Standalone `KiloAgent` struct in `internal/agent/kilo.go`, cloned from `opencode.go` since kilo may diverge in the future. Reuses `filterOpencodeToolCallLines` for output cleanup. Prompt via stdin, model via `--model`, agentic via `--auto`, reasoning via `--variant`.

**Tech Stack:** Go stdlib, `os/exec`

---

### Task 1: Create kilo.go with KiloAgent struct and Review method

**Files:**
- Create: `internal/agent/kilo.go`

**Step 1: Write the implementation**

```go
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// KiloAgent runs code reviews using the Kilo CLI (https://kilo.ai).
// This implementation is intentionally cloned from OpenCodeAgent rather than
// sharing a base struct, because kilo may drift from opencode in the future.
type KiloAgent struct {
	Command   string         // The kilo command to run (default: "kilo")
	Model     string         // Model to use (provider/model format, e.g., "anthropic/claude-sonnet-4-20250514")
	Reasoning ReasoningLevel // Reasoning level mapped to --variant flag
	Agentic   bool           // Whether agentic mode is enabled (uses --auto)
}

// NewKiloAgent creates a new Kilo agent
func NewKiloAgent(command string) *KiloAgent {
	if command == "" {
		command = "kilo"
	}
	return &KiloAgent{Command: command, Reasoning: ReasoningStandard}
}

func (a *KiloAgent) WithReasoning(level ReasoningLevel) Agent {
	return &KiloAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: level,
		Agentic:   a.Agentic,
	}
}

func (a *KiloAgent) WithAgentic(agentic bool) Agent {
	return &KiloAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

func (a *KiloAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &KiloAgent{
		Command:   a.Command,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

func (a *KiloAgent) Name() string {
	return "kilo"
}

func (a *KiloAgent) CommandName() string {
	return a.Command
}

// kiloVariant maps ReasoningLevel to kilo's --variant flag values
func (a *KiloAgent) kiloVariant() string {
	switch a.Reasoning {
	case ReasoningThorough:
		return "high"
	case ReasoningFast:
		return "minimal"
	default:
		return "" // use kilo default
	}
}

func (a *KiloAgent) CommandLine() string {
	args := []string{"run", "--format", "default"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	agenticMode := a.Agentic || AllowUnsafeAgents()
	if agenticMode {
		args = append(args, "--auto")
	}
	if variant := a.kiloVariant(); variant != "" {
		args = append(args, "--variant", variant)
	}
	return a.Command + " " + strings.Join(args, " ")
}

func (a *KiloAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	args := []string{"run", "--format", "default"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	agenticMode := a.Agentic || AllowUnsafeAgents()
	if agenticMode {
		args = append(args, "--auto")
	}
	if variant := a.kiloVariant(); variant != "" {
		args = append(args, "--variant", variant)
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	if sw := newSyncWriter(output); sw != nil {
		cmd.Stdout = io.MultiWriter(&stdout, sw)
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"kilo failed: %w\nstdout: %s\nstderr: %s",
			err,
			stdout.String(),
			stderr.String(),
		)
	}

	result := filterOpencodeToolCallLines(stdout.String())
	if len(result) == 0 {
		return "No review output generated", nil
	}
	return result, nil
}

func init() {
	Register(NewKiloAgent(""))
}
```

**Step 2: Run build to verify compilation**

Run: `go build ./internal/agent/`
Expected: Success (no errors)

**Step 3: Commit**

```bash
git add internal/agent/kilo.go
git commit -m "feat: add Kilo agent (kilo CLI)"
```

---

### Task 2: Create kilo_test.go

**Files:**
- Create: `internal/agent/kilo_test.go`

**Step 1: Write the tests**

Mirror the patterns from `opencode_test.go`: model flag tests, prompt-via-stdin test, tool-call filtering test, variant/auto flag tests.

```go
package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestKiloModelFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		model        string
		wantModel    bool
		wantContains string
	}{
		{
			name:      "no model omits flag",
			model:     "",
			wantModel: false,
		},
		{
			name:         "explicit model includes flag",
			model:        "anthropic/claude-sonnet-4-20250514",
			wantModel:    true,
			wantContains: "anthropic/claude-sonnet-4-20250514",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewKiloAgent("kilo")
			a.Model = tt.model
			cl := a.CommandLine()
			if tt.wantModel {
				assertContains(t, cl, "--model")
				assertContains(t, cl, tt.wantContains)
			} else {
				assertNotContains(t, cl, "--model")
			}
		})
	}
}

func TestKiloReviewModelFlag(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)
	tests := []struct {
		name      string
		model     string
		wantFlag  bool
		wantModel string
	}{
		{
			name:     "no model omits --model from args",
			model:    "",
			wantFlag: false,
		},
		{
			name:      "explicit model passes --model to subprocess",
			model:     "openai/gpt-4o",
			wantFlag:  true,
			wantModel: "openai/gpt-4o",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, args, _ := runKiloMockReview(t, tt.model, "review this", nil)
			args = strings.TrimSpace(args)
			if tt.wantFlag {
				assertContains(t, args, "--model")
				assertContains(t, args, tt.wantModel)
			} else {
				assertNotContains(t, args, "--model")
			}
		})
	}
}

func TestKiloReviewPipesPromptViaStdin(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	prompt := "Review this commit carefully"
	_, args, stdin := runKiloMockReview(t, "", prompt, nil)

	if strings.TrimSpace(stdin) != prompt {
		t.Errorf("stdin content = %q, want %q", stdin, prompt)
	}
	assertNotContains(t, args, prompt)
}

func TestKiloReviewFiltersToolCallLines(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeToolCallJSON("read", map[string]any{"path": "/foo"}),
		"**Review:** Fix the typo.",
		makeToolCallJSON("edit", map[string]any{}),
		"Done.",
	}

	result, _, _ := runKiloMockReview(t, "", "prompt", stdoutLines)
	assertContains(t, result, "**Review:**")
	assertContains(t, result, "Done.")
	assertNotContains(t, result, `"name":"read"`)
}

func TestKiloAgenticAutoFlag(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	withUnsafeAgents(t, false)
	a := NewKiloAgent("kilo").WithAgentic(true).(*KiloAgent)
	cl := a.CommandLine()
	assertContains(t, cl, "--auto")

	b := NewKiloAgent("kilo").WithAgentic(false).(*KiloAgent)
	cl2 := b.CommandLine()
	assertNotContains(t, cl2, "--auto")
}

func TestKiloVariantFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		reasoning   ReasoningLevel
		wantVariant bool
		wantValue   string
	}{
		{name: "thorough maps to high", reasoning: ReasoningThorough, wantVariant: true, wantValue: "high"},
		{name: "fast maps to minimal", reasoning: ReasoningFast, wantVariant: true, wantValue: "minimal"},
		{name: "standard omits variant", reasoning: ReasoningStandard, wantVariant: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewKiloAgent("kilo").WithReasoning(tt.reasoning).(*KiloAgent)
			cl := a.CommandLine()
			if tt.wantVariant {
				assertContains(t, cl, "--variant")
				assertContains(t, cl, tt.wantValue)
			} else {
				assertNotContains(t, cl, "--variant")
			}
		})
	}
}

func runKiloMockReview(t *testing.T, model, prompt string, stdoutLines []string) (output, args, stdin string) {
	t.Helper()

	if stdoutLines == nil {
		stdoutLines = []string{"ok"}
	}

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines:  stdoutLines,
	})

	a := NewKiloAgent(mock.CmdPath)
	if model != "" {
		a.Model = model
	}

	out, err := a.Review(context.Background(), t.TempDir(), "HEAD", prompt, nil)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	argsBytes, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("failed to read args file: %v", err)
	}
	stdinBytes, err := os.ReadFile(mock.StdinFile)
	if err != nil {
		t.Fatalf("failed to read stdin file: %v", err)
	}

	return out, string(argsBytes), string(stdinBytes)
}
```

**Step 2: Run tests**

Run: `go test ./internal/agent/ -run TestKilo -v`
Expected: All PASS

**Step 3: Commit**

```bash
git add internal/agent/kilo_test.go
git commit -m "test: add kilo agent tests"
```

---

### Task 3: Register kilo in agent lists and help strings

**Files:**
- Modify: `internal/agent/agent.go:176` (fallback list)
- Modify: `internal/agent/agent.go:192` (error message)
- Modify: `internal/agent/agent_test_helpers.go:16` (expectedAgents)
- Modify: `cmd/roborev/main.go:547` (init --agent flag)
- Modify: `cmd/roborev/main.go:1075` (review --agent flag)

**Step 1: Update agent.go fallback list (line 176)**

Change:
```go
fallbacks := []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "droid"}
```
To:
```go
fallbacks := []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "kilo", "droid"}
```

**Step 2: Update agent.go error message (line 192)**

Change:
```go
return nil, fmt.Errorf("no agents available (install one of: codex, claude-code, gemini, copilot, opencode, cursor, droid)\nYou may need to run 'roborev daemon restart' from a shell that has access to your agents")
```
To:
```go
return nil, fmt.Errorf("no agents available (install one of: codex, claude-code, gemini, copilot, opencode, cursor, kilo, droid)\nYou may need to run 'roborev daemon restart' from a shell that has access to your agents")
```

**Step 3: Update agent_test_helpers.go expectedAgents (line 16)**

Change:
```go
var expectedAgents = []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "test"}
```
To:
```go
var expectedAgents = []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "kilo", "test"}
```

**Step 4: Update cmd/roborev/main.go init flag (line 547)**

Change `"default agent (codex, claude-code, gemini, copilot, opencode, cursor)"` to include `, kilo`.

**Step 5: Update cmd/roborev/main.go review flag (line 1075)**

Change `"agent to use (codex, claude-code, gemini, copilot, opencode, cursor)"` to include `, kilo`.

**Step 6: Run all tests**

Run: `go test ./...`
Expected: All PASS

**Step 7: Run go vet and go fmt**

Run: `go fmt ./... && go vet ./...`
Expected: Clean

**Step 8: Commit**

```bash
git add internal/agent/agent.go internal/agent/agent_test_helpers.go cmd/roborev/main.go
git commit -m "feat: register kilo in agent fallback list and help text"
```
