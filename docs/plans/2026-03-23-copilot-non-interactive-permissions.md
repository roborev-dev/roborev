# Copilot Non-Interactive Permissions Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix copilot agent permission denials in daemon mode by adding `--allow-all-tools` with a deny-list for destructive operations.

**Architecture:** Feature detection (cached sync.Map) gates the new flags so old copilot binaries degrade gracefully. Two permission tiers: review mode (allow-all + deny destructive) and agentic mode (allow-all, no deny). Follows the same pattern as Claude (`--allowedTools`), Codex (`--sandbox`), and Gemini (`--approval-mode`).

**Tech Stack:** Go, copilot CLI flags (`--allow-all-tools`, `--deny-tool`, `-s`), testify

**Design doc:** `docs/plans/2026-03-23-copilot-non-interactive-permissions-design.md`

---

### Task 1: Add feature detection for --allow-all-tools

**Files:**
- Modify: `internal/agent/copilot.go` (add sync.Map + detection function)
- Test: `internal/agent/copilot_test.go`

**Step 1: Write the failing tests**

Add to `copilot_test.go`:

```go
func TestCopilotSupportsAllowAllTools(t *testing.T) {
	skipIfWindows(t)

	t.Run("supported", func(t *testing.T) {
		mock := mockAgentCLI(t, MockCLIOpts{
			HelpOutput: "Usage: copilot [flags]\n\n  --allow-all-tools  Auto-approve all tool calls",
		})
		supported, err := copilotSupportsAllowAllTools(context.Background(), mock.CmdPath)
		require.NoError(t, err)
		assert.True(t, supported)
	})

	t.Run("not supported", func(t *testing.T) {
		mock := mockAgentCLI(t, MockCLIOpts{
			HelpOutput: "Usage: copilot [flags]\n\n  --model  Model to use",
		})
		supported, err := copilotSupportsAllowAllTools(context.Background(), mock.CmdPath)
		require.NoError(t, err)
		assert.False(t, supported)
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestCopilotSupportsAllowAllTools -v`
Expected: compilation error — `copilotSupportsAllowAllTools` undefined

**Step 3: Implement feature detection**

Add to `copilot.go` after the imports, before the struct:

```go
var copilotAllowAllToolsSupport sync.Map

// copilotSupportsAllowAllTools checks whether the copilot binary supports
// the --allow-all-tools flag needed for non-interactive tool approval.
// Results are cached per command path.
func copilotSupportsAllowAllTools(ctx context.Context, command string) (bool, error) {
	if cached, ok := copilotAllowAllToolsSupport.Load(command); ok {
		return cached.(bool), nil
	}
	cmd := exec.CommandContext(ctx, command, "--help")
	output, err := cmd.CombinedOutput()
	supported := strings.Contains(string(output), "--allow-all-tools")
	if err != nil && !supported {
		return false, fmt.Errorf("check %s --help: %w: %s", command, err, output)
	}
	copilotAllowAllToolsSupport.Store(command, supported)
	return supported, nil
}
```

Add `"sync"` to the imports block.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestCopilotSupportsAllowAllTools -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/agent/copilot.go internal/agent/copilot_test.go
git commit -m "Add copilot feature detection for --allow-all-tools (#555)"
```

---

### Task 2: Add buildArgs method with deny-list

**Files:**
- Modify: `internal/agent/copilot.go` (add deny-list constant + buildArgs method)
- Test: `internal/agent/copilot_test.go`

**Step 1: Write the failing tests**

Add to `copilot_test.go`:

```go
func TestCopilotBuildArgs(t *testing.T) {
	a := NewCopilotAgent("copilot")

	t.Run("review mode includes deny list", func(t *testing.T) {
		assert := assert.New(t)
		args := a.buildArgs(false)
		assert.Contains(args, "-s")
		assert.Contains(args, "--allow-all-tools")
		// Verify deny-tool pairs exist for each denied tool
		for _, tool := range copilotReviewDenyTools {
			found := false
			for i, arg := range args {
				if arg == "--deny-tool" && i+1 < len(args) && args[i+1] == tool {
					found = true
					break
				}
			}
			assert.True(found, "missing --deny-tool %q", tool)
		}
	})

	t.Run("agentic mode has no deny list", func(t *testing.T) {
		assert := assert.New(t)
		args := a.buildArgs(true)
		assert.Contains(args, "-s")
		assert.Contains(args, "--allow-all-tools")
		assert.NotContains(args, "--deny-tool")
	})

	t.Run("model flag included when set", func(t *testing.T) {
		withModel := NewCopilotAgent("copilot").WithModel("gpt-4o").(*CopilotAgent)
		args := withModel.buildArgs(false)
		found := false
		for i, arg := range args {
			if arg == "--model" && i+1 < len(args) && args[i+1] == "gpt-4o" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected --model gpt-4o in args: %v", args)
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestCopilotBuildArgs -v`
Expected: compilation error — `buildArgs` and `copilotReviewDenyTools` undefined

**Step 3: Implement buildArgs and deny-list constant**

Add to `copilot.go` after the feature detection function:

```go
// copilotReviewDenyTools lists tools denied in review mode to enforce read-only
// behavior. Deny rules take precedence over --allow-all-tools in copilot's
// permission system.
var copilotReviewDenyTools = []string{
	"write",
	"shell(git push:*)",
	"shell(git commit:*)",
	"shell(git checkout:*)",
	"shell(git reset:*)",
	"shell(git rebase:*)",
	"shell(git merge:*)",
	"shell(git stash:*)",
	"shell(git clean:*)",
	"shell(rm:*)",
}

// buildArgs constructs CLI arguments for a copilot invocation.
// In review mode, destructive tools are denied. In agentic mode, all tools
// are allowed without restriction.
func (a *CopilotAgent) buildArgs(agenticMode bool) []string {
	args := []string{"-s", "--allow-all-tools"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if !agenticMode {
		for _, tool := range copilotReviewDenyTools {
			args = append(args, "--deny-tool", tool)
		}
	}
	return args
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestCopilotBuildArgs -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/agent/copilot.go internal/agent/copilot_test.go
git commit -m "Add copilot buildArgs with review-mode deny list (#555)"
```

---

### Task 3: Update Review() to use permission flags

**Files:**
- Modify: `internal/agent/copilot.go:79-111` (rewrite Review method)
- Test: `internal/agent/copilot_test.go`

**Step 1: Write the failing tests**

Add to `copilot_test.go`:

```go
func TestCopilotReviewPermissionFlags(t *testing.T) {
	skipIfWindows(t)

	t.Run("review mode passes permission flags", func(t *testing.T) {
		assert := assert.New(t)
		mock := mockAgentCLI(t, MockCLIOpts{
			HelpOutput:   "--allow-all-tools",
			CaptureArgs:  true,
			CaptureStdin: true,
			StdoutLines:  []string{"review output"},
		})
		a := NewCopilotAgent(mock.CmdPath)
		res, err := a.Review(context.Background(), t.TempDir(), "HEAD", "prompt", nil)
		require.NoError(t, err)
		assert.Equal("review output\n", res)

		args := readFileContent(t, mock.ArgsFile)
		assert.Contains(args, "--allow-all-tools")
		assert.Contains(args, "-s")
		assert.Contains(args, "--deny-tool")
		assertFileContent(t, mock.StdinFile, "prompt")
	})

	t.Run("agentic mode omits deny list", func(t *testing.T) {
		assert := assert.New(t)
		mock := mockAgentCLI(t, MockCLIOpts{
			HelpOutput:   "--allow-all-tools",
			CaptureArgs:  true,
			CaptureStdin: true,
			StdoutLines:  []string{"fix output"},
		})
		a := NewCopilotAgent(mock.CmdPath).WithAgentic(true)
		res, err := a.Review(context.Background(), t.TempDir(), "HEAD", "prompt", nil)
		require.NoError(t, err)
		assert.Equal("fix output\n", res)

		args := readFileContent(t, mock.ArgsFile)
		assert.Contains(args, "--allow-all-tools")
		assert.NotContains(args, "--deny-tool")
	})

	t.Run("AllowUnsafeAgents overrides to agentic", func(t *testing.T) {
		SetAllowUnsafeAgents(true)
		defer SetAllowUnsafeAgents(false)

		mock := mockAgentCLI(t, MockCLIOpts{
			HelpOutput:  "--allow-all-tools",
			CaptureArgs: true,
			StdoutLines: []string{"output"},
		})
		a := NewCopilotAgent(mock.CmdPath) // not WithAgentic(true)
		_, err := a.Review(context.Background(), t.TempDir(), "HEAD", "prompt", nil)
		require.NoError(t, err)

		args := readFileContent(t, mock.ArgsFile)
		assert.Contains(t, args, "--allow-all-tools")
		assert.NotContains(t, args, "--deny-tool")
	})

	t.Run("falls back to no flags when unsupported", func(t *testing.T) {
		mock := mockAgentCLI(t, MockCLIOpts{
			HelpOutput:   "Usage: copilot [flags]", // no --allow-all-tools
			CaptureArgs:  true,
			CaptureStdin: true,
			StdoutLines:  []string{"output"},
		})
		a := NewCopilotAgent(mock.CmdPath)
		_, err := a.Review(context.Background(), t.TempDir(), "HEAD", "prompt", nil)
		require.NoError(t, err)

		args := readFileContent(t, mock.ArgsFile)
		assert.NotContains(t, args, "--allow-all-tools")
		assert.NotContains(t, args, "--deny-tool")
		assert.NotContains(t, args, "-s")
	})
}

// readFileContent reads a file and returns its trimmed content.
func readFileContent(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read %s", path)
	return strings.TrimSpace(string(content))
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestCopilotReviewPermissionFlags -v`
Expected: FAIL — Review() doesn't pass permission flags yet

**Step 3: Rewrite Review() method**

Replace `copilot.go` lines 79-111 with:

```go
func (a *CopilotAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	agenticMode := a.Agentic || AllowUnsafeAgents()

	supported, err := copilotSupportsAllowAllTools(ctx, a.Command)
	if err != nil {
		log.Printf("copilot: cannot detect --allow-all-tools support: %v", err)
	}

	var args []string
	if supported {
		args = a.buildArgs(agenticMode)
	} else {
		if a.Model != "" {
			args = append(args, "--model", a.Model)
		}
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Dir = repoPath
	tracker := configureSubprocess(cmd)

	var stdout, stderr bytes.Buffer
	if sw := newSyncWriter(output); sw != nil {
		cmd.Stdout = io.MultiWriter(&stdout, sw)
		cmd.Stderr = io.MultiWriter(&stderr, sw)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		if ctxErr := contextProcessError(ctx, tracker, err, nil); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("copilot failed: %w\nstderr: %s", err, stderr.String())
	}

	result := stdout.String()
	if len(result) == 0 {
		return "No review output generated", nil
	}

	return result, nil
}
```

Add `"log"` to the imports block.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestCopilotReview -v`
Expected: PASS (both old and new tests)

**Step 5: Commit**

```bash
git add internal/agent/copilot.go internal/agent/copilot_test.go
git commit -m "Wire copilot permission flags into Review() with fallback (#555)"
```

---

### Task 4: Update WithAgentic comment and CommandLine

**Files:**
- Modify: `internal/agent/copilot.go:38-40` (WithAgentic comment), `copilot.go:71-77` (CommandLine)

**Step 1: Update WithAgentic comment**

Replace lines 38-40:

```go
// WithAgentic returns a copy of the agent configured for agentic mode.
// In agentic mode, all tools are allowed without restriction. In review mode
// (default), destructive tools are denied via --deny-tool flags.
```

**Step 2: Update Agentic field comment**

Replace line 17:

```go
	Agentic   bool           // Whether agentic mode is enabled (controls --deny-tool flags)
```

**Step 3: Update CommandLine to show -s flag**

Replace lines 71-77:

```go
func (a *CopilotAgent) CommandLine() string {
	args := []string{"-s"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	return a.Command + " " + strings.Join(args, " ")
}
```

**Step 4: Run full test suite**

Run: `go test ./internal/agent/ -v`
Expected: all PASS

**Step 5: Format and vet**

Run: `go fmt ./... && go vet ./...`

**Step 6: Commit**

```bash
git add internal/agent/copilot.go
git commit -m "Update copilot agent comments and CommandLine (#555)"
```
