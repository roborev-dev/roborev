package agent

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

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

func TestCopilotReview(t *testing.T) {
	skipIfWindows(t)

	tests := []struct {
		name       string
		prompt     string
		mockOpts   MockCLIOpts
		wantErr    bool
		wantErrStr string
		wantResult string
	}{
		{
			name:   "Pipes prompt via stdin",
			prompt: "Review this commit carefully",
			mockOpts: MockCLIOpts{
				CaptureArgs:  true,
				CaptureStdin: true,
				StdoutLines:  []string{"ok"},
			},
			wantErr:    false,
			wantResult: "ok\n",
		},
		{
			name:   "CLI failure (exit non-zero)",
			prompt: "Review this commit",
			mockOpts: MockCLIOpts{
				ExitCode:    1,
				StderrLines: []string{"error: failed to generate review"},
			},
			wantErr:    true,
			wantErrStr: "copilot failed",
		},
		{
			name:   "Empty output from CLI",
			prompt: "Review this commit",
			mockOpts: MockCLIOpts{
				CaptureArgs:  true,
				CaptureStdin: true,
				StdoutLines:  []string{},
			},
			wantErr:    false,
			wantResult: "No review output generated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := mockAgentCLI(t, tt.mockOpts)
			a := NewCopilotAgent(mock.CmdPath)

			res, err := a.Review(
				context.Background(), t.TempDir(), "HEAD", tt.prompt, nil,
			)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrStr != "" {
					require.Contains(t, err.Error(), tt.wantErrStr, "Review() error = %v, want to contain %v", err, tt.wantErrStr)
				}
			} else {
				require.NoError(t, err, "Review failed")
				assert.Equal(t, tt.wantResult, res, "Review() result = %q, want %q", res, tt.wantResult)

				assertFileContent(t, mock.StdinFile, tt.prompt)

				assertFileNotContains(t, mock.ArgsFile, tt.prompt)
			}
		})
	}
}
