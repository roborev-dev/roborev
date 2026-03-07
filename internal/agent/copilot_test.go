package agent

import (
	"context"
	"strings"
	"testing"
)

func TestCopilotReview(t *testing.T) {
	skipIfWindows(t)

	tests := []struct {
		name       string
		prompt     string
		mockOpts   MockCLIOpts
		wantErr    bool
		wantErrStr string
		wantResult string
		verifyCLI  bool
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
			verifyCLI:  true,
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
				StdoutLines:  []string{}, // empty output
			},
			wantErr:    false,
			wantResult: "No review output generated",
			verifyCLI:  true,
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
				if err == nil {
					t.Fatalf("Review() expected error containing %q, got nil", tt.wantErrStr)
				}
				if tt.wantErrStr != "" && !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("Review() error = %v, want to contain %q", err, tt.wantErrStr)
				}
				return
			}

			assertNoError(t, err, "Review failed")
			if res != tt.wantResult {
				t.Errorf("Review() result = %q, want %q", res, tt.wantResult)
			}

			if tt.verifyCLI {
				// Prompt must be in stdin
				assertFileContent(t, mock.StdinFile, tt.prompt)
				// Prompt must not be in argv
				assertFileNotContains(t, mock.ArgsFile, tt.prompt)
			}
		})
	}
}
