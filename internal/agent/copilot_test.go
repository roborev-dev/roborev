package agent

import (
	"context"
	"testing"
)

func TestCopilotReview(t *testing.T) {
	skipIfWindows(t)

	tests := []struct {
		name     string
		prompt   string
		mockOpts MockCLIOpts
		wantErr  bool
	}{
		{
			name:   "Pipes prompt via stdin",
			prompt: "Review this commit carefully",
			mockOpts: MockCLIOpts{
				CaptureArgs:  true,
				CaptureStdin: true,
				StdoutLines:  []string{"ok"},
			},
			wantErr: false,
		},
		{
			name:   "CLI failure (exit non-zero)",
			prompt: "Review this commit",
			mockOpts: MockCLIOpts{
				ExitCode:    1,
				StderrLines: []string{"error: failed to generate review"},
			},
			wantErr: true,
		},
		{
			name:   "Empty output from CLI",
			prompt: "Review this commit",
			mockOpts: MockCLIOpts{
				CaptureArgs:  true,
				CaptureStdin: true,
				StdoutLines:  []string{}, // empty output
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := mockAgentCLI(t, tt.mockOpts)
			a := NewCopilotAgent(mock.CmdPath)

			_, err := a.Review(
				context.Background(), t.TempDir(), "HEAD", tt.prompt, nil,
			)

			if tt.wantErr {
				if err == nil {
					t.Fatal("Review() expected error, got nil")
				}
			} else {
				assertNoError(t, err, "Review failed")
				// Prompt must be in stdin
				assertFileContent(t, mock.StdinFile, tt.prompt)
				// Prompt must not be in argv
				assertFileNotContains(t, mock.ArgsFile, tt.prompt)
			}
		})
	}
}
