package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDroidBuildArgs(t *testing.T) {
	tests := []struct {
		name     string
		agentic  bool
		setup    func(*DroidAgent) *DroidAgent
		wantArgs []string
		dontWant []string
	}{
		{
			name:     "Non-agentic default",
			agentic:  false,
			wantArgs: []string{"--auto", "low"},
			dontWant: []string{"medium"},
		},
		{
			name:     "Agentic mode",
			agentic:  true,
			wantArgs: []string{"--auto", "medium"},
		},
		{
			name:     "Reasoning Thorough",
			setup:    func(a *DroidAgent) *DroidAgent { return a.WithReasoning(ReasoningThorough).(*DroidAgent) },
			wantArgs: []string{"--reasoning-effort", "high"},
		},
		{
			name:     "Reasoning Fast",
			setup:    func(a *DroidAgent) *DroidAgent { return a.WithReasoning(ReasoningFast).(*DroidAgent) },
			wantArgs: []string{"--reasoning-effort", "low"},
		},
		{
			name:     "Reasoning Standard",
			setup:    func(a *DroidAgent) *DroidAgent { return a.WithReasoning(ReasoningStandard).(*DroidAgent) },
			dontWant: []string{"--reasoning-effort"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewDroidAgent("droid")
			if tt.setup != nil {
				a = tt.setup(a)
			}

			args := a.buildArgs(tt.agentic)

			for _, want := range tt.wantArgs {
				assertContainsArg(t, args, want)
			}
			for _, dont := range tt.dontWant {
				assertNotContainsArg(t, args, dont)
			}
		})
	}
}

func TestDroidName(t *testing.T) {
	a := NewDroidAgent("")
	if a.Name() != "droid" {
		t.Fatalf("expected name 'droid', got %s", a.Name())
	}
	if a.CommandName() != "droid" {
		t.Fatalf("expected command name 'droid', got %s", a.CommandName())
	}
}

func TestDroidWithAgentic(t *testing.T) {
	a := NewDroidAgent("droid")
	if a.Agentic {
		t.Fatal("expected non-agentic by default")
	}

	a2 := a.WithAgentic(true).(*DroidAgent)
	if !a2.Agentic {
		t.Fatal("expected agentic after WithAgentic(true)")
	}
	if a.Agentic {
		t.Fatal("original should be unchanged")
	}
}

func TestDroidReviewOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		mockOpts    MockCLIOpts
		wantError   bool
		errContains string
		wantResult  string
		exactMatch  bool
	}{
		{
			name:       "Success",
			mockOpts:   MockCLIOpts{StdoutLines: []string{"Review feedback from Droid"}},
			wantResult: "Review feedback from Droid\n",
			exactMatch: true,
		},
		{
			name:        "Failure",
			mockOpts:    MockCLIOpts{StderrLines: []string{"error: something went wrong"}, ExitCode: 1},
			wantError:   true,
			errContains: "droid failed",
		},
		{
			name:       "Empty Output",
			mockOpts:   MockCLIOpts{},
			wantResult: "No review output generated",
			exactMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := mockAgentCLI(t, tt.mockOpts)
			a := NewDroidAgent(mock.CmdPath)

			result, err := a.Review(context.Background(), t.TempDir(), "HEAD", "review this commit", nil)

			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("expected error to contain %q, got %v", tt.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tt.exactMatch {
				if result != tt.wantResult {
					t.Fatalf("expected exact result %q, got %q", tt.wantResult, result)
				}
			} else if !strings.Contains(result, tt.wantResult) {
				t.Fatalf("expected result to contain %q, got %q", tt.wantResult, result)
			}
		})
	}
}

func TestDroidReviewWithProgress(t *testing.T) {
	skipIfWindows(t)
	tmpDir := t.TempDir()
	progressFile := filepath.Join(tmpDir, "progress.txt")

	mock := mockAgentCLI(t, MockCLIOpts{
		StderrLines: []string{"Processing..."},
		StdoutLines: []string{"Done"},
	})
	a := NewDroidAgent(mock.CmdPath)

	f, err := os.Create(progressFile)
	if err != nil {
		t.Fatalf("create progress file: %v", err)
	}
	defer f.Close()

	_, err = a.Review(context.Background(), tmpDir, "deadbeef", "review this commit", f)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	progress, _ := os.ReadFile(progressFile)
	if !strings.Contains(string(progress), "Processing") {
		t.Fatalf("expected progress output, got %q", string(progress))
	}
}

func TestDroidReviewPipesPromptViaStdin(t *testing.T) {
	skipIfWindows(t)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines:  []string{"ok"},
	})

	a := NewDroidAgent(mock.CmdPath)
	prompt := "Review this commit carefully"
	_, err := a.Review(
		context.Background(), t.TempDir(), "HEAD", prompt, nil,
	)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Prompt must be in stdin
	assertFileContent(t, mock.StdinFile, prompt)

	// Prompt must not be in argv
	assertFileNotContains(t, mock.ArgsFile, prompt)
}

func TestDroidReviewAgenticModeFromGlobal(t *testing.T) {
	withUnsafeAgents(t, true)

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs: true,
		StdoutLines: []string{"result"},
	})

	a := NewDroidAgent(mock.CmdPath)
	if _, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	args, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(args), "medium") {
		t.Fatalf("expected '--auto medium' in args when global unsafe enabled, got %s", strings.TrimSpace(string(args)))
	}
}
