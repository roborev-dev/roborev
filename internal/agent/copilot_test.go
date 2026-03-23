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
