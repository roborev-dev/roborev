package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/roborev-dev/roborev/internal/config"
)

func TestACPAgent(t *testing.T) {
	// Test agent creation and configuration
	acpAgent := NewACPAgent("test-acp-agent")

	if acpAgent.Name() != "acp" {
		t.Errorf("Expected Name() to return 'acp', got '%s'", acpAgent.Name())
	}

	if acpAgent.CommandName() != "test-acp-agent" {
		t.Errorf("Expected CommandName() to be 'test-acp-agent', got '%s'", acpAgent.CommandName())
	}

	// Test WithReasoning
	thoroughAgent := acpAgent.WithReasoning(ReasoningThorough)
	if thoroughAgent.(*ACPAgent).Reasoning != ReasoningThorough {
		t.Errorf("Expected Reasoning to be 'thorough', got '%s'", thoroughAgent.(*ACPAgent).Reasoning)
	}

	// Test WithAgentic
	agenticAgent := acpAgent.WithAgentic(true)
	if !agenticAgent.(*ACPAgent).Agentic {
		t.Error("Expected Agentic to be true")
	}

	// Test WithModel
	modelAgent := acpAgent.WithModel("gpt-4")
	if modelAgent.(*ACPAgent).Model != "gpt-4" {
		t.Errorf("Expected Model to be 'gpt-4', got '%s'", modelAgent.(*ACPAgent).Model)
	}

	// Agentic state should survive model overrides.
	if !acpAgent.WithAgentic(true).WithModel("gpt-4").(*ACPAgent).Agentic {
		t.Errorf("Expected Agentic to be preserved through WithModel")
	}

	defaultAgent := NewACPAgent("")
	if defaultAgent.Command != "acp-agent" {
		t.Errorf("Expected default command to be 'acp-agent', got '%s'", defaultAgent.Command)
	}
	if defaultAgent.Mode != "plan" {
		t.Errorf("Expected default mode to be 'plan', got '%s'", defaultAgent.Mode)
	}
	if defaultAgent.Timeout != 10*time.Minute {
		t.Errorf("Expected default timeout to be 10m, got %s", defaultAgent.Timeout)
	}

	configuredAgent := NewACPAgentFromConfig(&config.ACPAgentConfig{
		Name:            "custom-acp",
		Command:         "custom-command",
		ReadOnlyMode:    "plan",
		AutoApproveMode: "auto-approve",
		Mode:            "plan",
	})
	if configuredAgent.Name() != "custom-acp" {
		t.Errorf("Expected Name() to return custom config name, got '%s'", configuredAgent.Name())
	}
}

func TestACPAgentCommandLine(t *testing.T) {
	agent := NewACPAgent("acp-agent")

	// Test default command line
	cmdLine := agent.CommandLine()
	if !strings.Contains(cmdLine, "acp-agent") {
		t.Errorf("Expected command line to contain 'acp-agent', got '%s'", cmdLine)
	}

	// Test with model
	withModel := agent.WithModel("claude-3-opus")
	if withModel.CommandLine() != agent.CommandLine() {
		t.Errorf("Expected command line to be unchanged. expected: \"%s\", got: \"%s\"", agent.CommandLine(), withModel.CommandLine())
	}

	// Test with agentic mode
	agentic := agent.WithAgentic(true)
	if agent.CommandLine() != agentic.CommandLine() {
		t.Errorf("Expected command line to be unchanged. expected: \"%s\", got: \"%s\"", agent.CommandLine(), withModel.CommandLine())
	}
}

func TestApplyACPAgentConfigOverrideModeResolution(t *testing.T) {
	tests := []struct {
		name               string
		override           *config.ACPAgentConfig
		wantReadOnly       string
		wantMode           string
		wantDisableModeNeg bool
	}{
		{
			name: "read_only_mode only",
			override: &config.ACPAgentConfig{
				ReadOnlyMode: "safe-plan",
			},
			wantReadOnly:       "safe-plan",
			wantMode:           "safe-plan",
			wantDisableModeNeg: false,
		},
		{
			name: "mode only",
			override: &config.ACPAgentConfig{
				Mode: "custom-mode",
			},
			wantReadOnly:       defaultACPReadOnlyMode,
			wantMode:           "custom-mode",
			wantDisableModeNeg: false,
		},
		{
			name: "both mode and read_only_mode",
			override: &config.ACPAgentConfig{
				ReadOnlyMode: "safe-plan",
				Mode:         "agentic-mode",
			},
			wantReadOnly:       "safe-plan",
			wantMode:           "agentic-mode",
			wantDisableModeNeg: false,
		},
		{
			name: "disable mode negotiation",
			override: &config.ACPAgentConfig{
				DisableModeNegotiation: true,
			},
			wantReadOnly:       defaultACPReadOnlyMode,
			wantMode:           "",
			wantDisableModeNeg: true,
		},
		{
			name: "neither mode nor read_only_mode",
			override: &config.ACPAgentConfig{
				Model: "devstral-2",
			},
			wantReadOnly:       defaultACPReadOnlyMode,
			wantMode:           defaultACPReadOnlyMode,
			wantDisableModeNeg: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultACPAgentConfig()
			applyACPAgentConfigOverride(cfg, tc.override)

			if cfg.ReadOnlyMode != tc.wantReadOnly {
				t.Fatalf("ReadOnlyMode = %q, want %q", cfg.ReadOnlyMode, tc.wantReadOnly)
			}
			if cfg.Mode != tc.wantMode {
				t.Fatalf("Mode = %q, want %q", cfg.Mode, tc.wantMode)
			}
			if cfg.DisableModeNegotiation != tc.wantDisableModeNeg {
				t.Fatalf("DisableModeNegotiation = %v, want %v", cfg.DisableModeNegotiation, tc.wantDisableModeNeg)
			}
		})
	}
}

func TestNewACPAgentFromConfigDisableModeNegotiation(t *testing.T) {
	t.Parallel()

	agent := NewACPAgentFromConfig(&config.ACPAgentConfig{
		Command:                "go",
		Mode:                   "plan",
		ReadOnlyMode:           "plan",
		AutoApproveMode:        "auto-approve",
		DisableModeNegotiation: true,
	})
	if agent.Mode != "" {
		t.Fatalf("expected mode negotiation disabled (empty mode), got %q", agent.Mode)
	}

	agentic := agent.WithAgentic(true).(*ACPAgent)
	if agentic.Mode != "" {
		t.Fatalf("expected WithAgentic(true) to preserve disabled mode negotiation, got %q", agentic.Mode)
	}
	if !agentic.mutatingOperationsAllowed() {
		t.Fatalf("expected mutating operations allowed in agentic mode when negotiation is disabled")
	}

	nonAgentic := agent.WithAgentic(false).(*ACPAgent)
	if nonAgentic.Mode != "" {
		t.Fatalf("expected WithAgentic(false) to preserve disabled mode negotiation, got %q", nonAgentic.Mode)
	}
	if nonAgentic.mutatingOperationsAllowed() {
		t.Fatalf("expected mutating operations denied in non-agentic mode when negotiation is disabled")
	}
}

func TestGetAvailableWithConfigResolvesACPAlias(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		ACP: &config.ACPAgentConfig{
			Name:    "custom-acp",
			Command: "go",
		},
	}

	resolved, err := GetAvailableWithConfig("custom-acp", cfg)
	if err != nil {
		t.Fatalf("GetAvailableWithConfig failed: %v", err)
	}

	acpAgent, ok := resolved.(*ACPAgent)
	if !ok {
		t.Fatalf("Expected ACP agent, got %T", resolved)
	}
	if acpAgent.Name() != "acp" {
		t.Fatalf("Expected canonical ACP name 'acp', got %q", acpAgent.Name())
	}
	if acpAgent.Command != "go" {
		t.Fatalf("Expected ACP command from config, got %q", acpAgent.Command)
	}
}

func TestGetAvailableWithConfigResolvesConfiguredACPNameAlias(t *testing.T) {
	fakeBin := t.TempDir()
	binName := defaultACPCommand
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	acpPath := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(acpPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake acp-agent binary: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	cfg := &config.Config{
		ACP: &config.ACPAgentConfig{
			Name:    "claude",
			Command: defaultACPCommand,
		},
	}

	resolved, err := GetAvailableWithConfig("claude", cfg)
	if err != nil {
		t.Fatalf("GetAvailableWithConfig failed: %v", err)
	}

	acpAgent, ok := resolved.(*ACPAgent)
	if !ok {
		t.Fatalf("Expected ACP agent, got %T", resolved)
	}
	if acpAgent.Name() != "acp" {
		t.Fatalf("Expected canonical ACP name 'acp', got %q", acpAgent.Name())
	}
	if acpAgent.Command != defaultACPCommand {
		t.Fatalf("Expected ACP command %q, got %q", defaultACPCommand, acpAgent.Command)
	}
}

func TestGetAvailableWithConfigFallsBackToCanonicalACPWhenConfiguredCommandMissing(t *testing.T) {
	fakeBin := t.TempDir()
	binName := "acp-agent"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	acpPath := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(acpPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake acp-agent binary: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	cfg := &config.Config{
		ACP: &config.ACPAgentConfig{
			Name:    "custom-acp",
			Command: "missing-acp-command",
		},
	}

	resolved, err := GetAvailableWithConfig("custom-acp", cfg)
	if err != nil {
		t.Fatalf("GetAvailableWithConfig failed: %v", err)
	}

	commandAgent, ok := resolved.(CommandAgent)
	if !ok {
		t.Fatalf("Expected resolved agent to implement CommandAgent, got %T", resolved)
	}
	if commandAgent.CommandName() != defaultACPCommand {
		t.Fatalf("Expected fallback to canonical command %q, got %q", defaultACPCommand, commandAgent.CommandName())
	}
}

func TestGetAvailableWithConfigResolvedACPBranchFallsBackWhenConfiguredCommandMissing(t *testing.T) {
	originalRegistry := registry
	registry = map[string]Agent{
		defaultACPName: NewACPAgent(""),
	}
	t.Cleanup(func() { registry = originalRegistry })

	fakeBin := t.TempDir()
	binName := defaultACPCommand
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	acpPath := filepath.Join(fakeBin, binName)
	if err := os.WriteFile(acpPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake acp-agent binary: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	cfg := &config.Config{
		ACP: &config.ACPAgentConfig{
			Name:    "custom-acp",
			Command: "missing-acp-command",
		},
	}

	resolved, err := GetAvailableWithConfig("nonexistent-agent", cfg)
	if err != nil {
		t.Fatalf("GetAvailableWithConfig failed: %v", err)
	}

	commandAgent, ok := resolved.(CommandAgent)
	if !ok {
		t.Fatalf("Expected resolved agent to implement CommandAgent, got %T", resolved)
	}
	if commandAgent.CommandName() != defaultACPCommand {
		t.Fatalf("Expected fallback to canonical command %q, got %q", defaultACPCommand, commandAgent.CommandName())
	}
}

// Helper function for string containment checks

// Helper function for testing
func intPtr(i int) *int {
	return &i
}

func terminalCount(client *acpClient) int {
	client.terminalsMutex.Lock()
	defer client.terminalsMutex.Unlock()
	return len(client.terminals)
}

func terminalExists(client *acpClient, terminalID string) bool {
	_, exists := client.getTerminal(terminalID)
	return exists
}

func TestACPAgentTerminalFunctionality(t *testing.T) {
	// Create agent and client for testing
	// Set agent to auto-approve mode to allow terminal creation
	agent := &ACPAgent{
		SessionId:       "test-session",
		ReadOnlyMode:    "read-only",
		AutoApproveMode: "auto-approve",
		Mode:            "auto-approve", // Set to auto-approve mode for testing
	}
	client := &acpClient{
		agent:          agent,
		terminals:      make(map[string]*acpTerminal),
		nextTerminalID: 1,
	}

	t.Run("Terminal creation and storage", func(t *testing.T) {
		cmd, args := acpTestEchoCommand("test")
		resp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			SessionId: "test-session",
			Command:   cmd,
			Args:      args,
		})
		if err != nil {
			t.Fatalf("Failed to create terminal: %v", err)
		}

		if resp.TerminalId != "term-1" {
			t.Errorf("Expected terminal ID 'term-1', got '%s'", resp.TerminalId)
		}

		if terminalCount(client) != 1 {
			t.Errorf("Expected 1 terminal, got %d", terminalCount(client))
		}

		// Clean up
		_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Errorf("Failed to release terminal: %v", err)
		}
	})

	t.Run("Output truncation", func(t *testing.T) {
		cmd, args := acpTestEchoCommand("test output")
		resp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			SessionId:       "test-session",
			Command:         cmd,
			Args:            args,
			OutputByteLimit: intPtr(5), // Very small limit
		})
		if err != nil {
			t.Fatalf("Failed to create terminal: %v", err)
		}

		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := client.WaitForTerminalExit(waitCtx, acp.WaitForTerminalExitRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		}); err != nil {
			t.Fatalf("WaitForTerminalExit failed: %v", err)
		}

		// Get output and check truncation
		outputResp, err := client.TerminalOutput(context.Background(), acp.TerminalOutputRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Errorf("Failed to get terminal output: %v", err)
		} else {
			if len(outputResp.Output) > 5 {
				t.Errorf("Expected output to be limited to 5 bytes, got %d bytes", len(outputResp.Output))
			}
			if !outputResp.Truncated {
				t.Errorf("Expected output to be marked truncated when output exceeds byte limit")
			}
		}

		// Clean up
		_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Errorf("Failed to release terminal: %v", err)
		}
	})

	t.Run("Terminal release with context cancellation", func(t *testing.T) {
		cmd, args := acpTestEchoCommand("test")
		resp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			SessionId: "test-session",
			Command:   cmd,
			Args:      args,
		})
		if err != nil {
			t.Fatalf("Failed to create terminal: %v", err)
		}

		// Release should cancel the context
		_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Errorf("Failed to release terminal: %v", err)
		}

		// Terminal should be removed immediately on explicit release.
		if terminalCount(client) != 0 {
			t.Errorf("Expected 0 terminals after release, got %d", terminalCount(client))
		}
	})

	t.Run("Session ID validation", func(t *testing.T) {
		cmd, args := acpTestEchoCommand("test")
		resp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			SessionId: "test-session",
			Command:   cmd,
			Args:      args,
		})
		if err != nil {
			t.Fatalf("Failed to create terminal: %v", err)
		}

		// Try to access with wrong session ID
		_, err = client.TerminalOutput(context.Background(), acp.TerminalOutputRequest{
			SessionId:  "wrong-session",
			TerminalId: resp.TerminalId,
		})
		if err == nil {
			t.Error("Expected error for wrong session ID")
		} else if !strings.Contains(err.Error(), "session ID mismatch") {
			t.Errorf("Expected session ID mismatch error, got: %v", err)
		}

		// Clean up
		_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Errorf("Failed to release terminal: %v", err)
		}
	})

	t.Run("Terminal lifecycle - persists after command completion", func(t *testing.T) {
		cmd, args := acpTestEchoCommand("test")
		resp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			SessionId: "test-session",
			Command:   cmd,
			Args:      args,
		})
		if err != nil {
			t.Fatalf("Failed to create terminal: %v", err)
		}

		// Terminal should exist.
		if terminalCount(client) != 1 {
			t.Errorf("Expected 1 terminal, got %d", terminalCount(client))
		}

		// Wait for process completion and verify the terminal remains accessible.
		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		waitResp, err := client.WaitForTerminalExit(waitCtx, acp.WaitForTerminalExitRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Fatalf("WaitForTerminalExit failed: %v", err)
		}
		if waitResp.ExitCode == nil || *waitResp.ExitCode != 0 {
			t.Fatalf("Expected exit code 0, got %+v", waitResp)
		}

		// Terminal should still exist after command completion (no auto-cleanup).
		// This verifies the ACP spec requirement that terminals remain valid until explicit release
		if !terminalExists(client, resp.TerminalId) {
			t.Fatalf("Expected terminal %s to persist after completion", resp.TerminalId)
		}

		outputResp, err := client.TerminalOutput(context.Background(), acp.TerminalOutputRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Fatalf("TerminalOutput failed: %v", err)
		}
		if outputResp.ExitStatus == nil || outputResp.ExitStatus.ExitCode == nil || *outputResp.ExitStatus.ExitCode != 0 {
			t.Fatalf("Expected terminal output to include exit status 0, got %+v", outputResp.ExitStatus)
		}

		// Clean up
		_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Errorf("Failed to release terminal: %v", err)
		}
	})

	t.Run("CreateTerminal defaults cwd to repo root when omitted", func(t *testing.T) {
		tempDir := t.TempDir()
		client.agent.repoRoot = tempDir
		client.repoRoot = tempDir

		cmd, args := acpTestEchoCommand("cwd")
		resp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			SessionId: "test-session",
			Command:   cmd,
			Args:      args,
		})
		if err != nil {
			t.Fatalf("Failed to create terminal: %v", err)
		}

		term, exists := client.getTerminal(resp.TerminalId)
		if !exists {
			t.Fatalf("terminal %s not found", resp.TerminalId)
		}
		resolvedTempDir, err := filepath.EvalSymlinks(tempDir)
		if err != nil {
			t.Fatalf("Failed to resolve temp dir: %v", err)
		}
		if !pathWithinRoot(term.cmd.Dir, resolvedTempDir) {
			t.Fatalf("Expected terminal cwd %q to be within %q", term.cmd.Dir, resolvedTempDir)
		}

		_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Errorf("Failed to release terminal: %v", err)
		}
	})

	t.Run("CreateTerminal resolves relative cwd against repo root", func(t *testing.T) {
		tempDir := t.TempDir()
		subdir := filepath.Join(tempDir, "sub")
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatalf("Failed to create subdir: %v", err)
		}
		client.agent.repoRoot = tempDir
		client.repoRoot = tempDir

		relative := "sub"
		cmd, args := acpTestEchoCommand("cwd")
		resp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			SessionId: "test-session",
			Command:   cmd,
			Args:      args,
			Cwd:       &relative,
		})
		if err != nil {
			t.Fatalf("Failed to create terminal: %v", err)
		}

		term, exists := client.getTerminal(resp.TerminalId)
		if !exists {
			t.Fatalf("terminal %s not found", resp.TerminalId)
		}
		resolvedSubdir, err := filepath.EvalSymlinks(subdir)
		if err != nil {
			t.Fatalf("Failed to resolve subdir: %v", err)
		}
		if term.cmd.Dir != resolvedSubdir {
			t.Fatalf("Expected terminal cwd %q, got %q", resolvedSubdir, term.cmd.Dir)
		}

		_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Errorf("Failed to release terminal: %v", err)
		}
	})

	t.Run("CreateTerminal rejects cwd traversal outside repo root", func(t *testing.T) {
		tempDir := t.TempDir()
		client.agent.repoRoot = tempDir
		client.repoRoot = tempDir

		outsideDir := filepath.Join(filepath.Dir(tempDir), "outside")
		if err := os.MkdirAll(outsideDir, 0o755); err != nil {
			t.Fatalf("Failed to create outside dir: %v", err)
		}

		relative := "../outside"
		cmd, args := acpTestEchoCommand("cwd")
		_, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			SessionId: "test-session",
			Command:   cmd,
			Args:      args,
			Cwd:       &relative,
		})
		if err == nil {
			t.Fatal("Expected error for cwd traversal outside repo root")
		}
		if !strings.Contains(err.Error(), "outside repository root") {
			t.Fatalf("Expected outside repository root error, got: %v", err)
		}
	})

	t.Run("CreateTerminal lifetime outlives request context", func(t *testing.T) {
		cmd, args, ok := acpTestSleepCommand(1)
		if !ok {
			t.Skip("sleep command not available")
		}
		client.agent.repoRoot = ""
		client.repoRoot = ""

		reqCtx, reqCancel := context.WithCancel(context.Background())
		resp, err := client.CreateTerminal(reqCtx, acp.CreateTerminalRequest{
			SessionId: "test-session",
			Command:   cmd,
			Args:      args,
		})
		if err != nil {
			t.Fatalf("Failed to create terminal: %v", err)
		}

		reqCancel()

		term, exists := client.getTerminal(resp.TerminalId)
		if !exists {
			t.Fatalf("terminal %s not found", resp.TerminalId)
		}
		select {
		case <-term.context.Done():
			t.Fatalf("terminal context canceled by request context cancellation")
		case <-time.After(50 * time.Millisecond):
			// expected
		}

		_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
			SessionId:  "test-session",
			TerminalId: resp.TerminalId,
		})
		if err != nil {
			t.Errorf("Failed to release terminal: %v", err)
		}
	})

	t.Run("WaitForTerminalExit does not block other terminal operations", func(t *testing.T) {
		blockedDone := make(chan struct{})
		blockedTerminal := &acpTerminal{
			id:   "blocked",
			done: blockedDone,
		}
		client.addTerminal(blockedTerminal)

		waitDone := make(chan struct{})
		waitErr := make(chan error, 1)
		waitResp := make(chan acp.WaitForTerminalExitResponse, 1)
		go func() {
			resp, err := client.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{
				SessionId:  "test-session",
				TerminalId: "blocked",
			})
			if err != nil {
				waitErr <- err
				close(waitDone)
				return
			}
			waitResp <- resp
			close(waitDone)
		}()

		// If WaitForTerminalExit still held the global map mutex, this add would block.
		addDone := make(chan struct{})
		go func() {
			client.addTerminal(&acpTerminal{
				id:   "secondary",
				done: make(chan struct{}),
			})
			close(addDone)
		}()

		select {
		case <-addDone:
			// expected
		case <-time.After(200 * time.Millisecond):
			t.Fatal("addTerminal blocked while WaitForTerminalExit was waiting")
		}

		blockedTerminal.setExitStatus(&acp.TerminalExitStatus{ExitCode: intPtr(0)})
		close(blockedDone)

		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
			t.Fatal("WaitForTerminalExit did not return after done channel close")
		}

		select {
		case err := <-waitErr:
			t.Fatalf("WaitForTerminalExit returned error: %v", err)
		default:
		}

		select {
		case resp := <-waitResp:
			if resp.ExitCode == nil || *resp.ExitCode != 0 {
				t.Fatalf("Expected exit code 0, got %+v", resp)
			}
		default:
			t.Fatal("missing WaitForTerminalExit response")
		}

		client.removeTerminal("blocked")
		client.removeTerminal("secondary")
	})
}

// TestACPNoDoubleMutexUnlockPanics tests that the double mutex unlock panic issue is fixed
func TestACPNoDoubleMutexUnlockPanics(t *testing.T) {
	// Create agent and client for testing
	agent := &ACPAgent{
		SessionId:       "test-session",
		ReadOnlyMode:    "read-only",
		AutoApproveMode: "auto-approve",
		Mode:            "auto-approve", // Set to auto-approve mode for testing
	}
	client := &acpClient{
		agent:          agent,
		terminals:      make(map[string]*acpTerminal),
		nextTerminalID: 1,
	}

	// Test WaitForTerminalExit with non-existent terminal
	// This should not panic with double mutex unlock
	_, err := client.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{
		TerminalId: "non-existent-terminal",
	})
	if err != nil {
		t.Logf("Expected error for non-existent terminal: %v", err)
	}
	// If we get here without panic, the test passes
}

// TestBoundedWriter tests the boundedWriter functionality
func TestBoundedWriter(t *testing.T) {
	t.Run("Write within limit", func(t *testing.T) {
		buf := &bytes.Buffer{}
		mutex := &sync.Mutex{}
		writer := &boundedWriter{
			writer: &threadSafeWriter{
				buf:   buf,
				mutex: mutex,
			},
			maxSize:   10,
			truncated: false,
		}

		n, err := writer.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != 5 {
			t.Errorf("Expected to write 5 bytes, got %d", n)
		}
		if buf.String() != "hello" {
			t.Errorf("Expected buffer to contain 'hello', got '%s'", buf.String())
		}
		if writer.truncated {
			t.Error("Writer should not be marked as truncated")
		}
	})

	t.Run("Write exactly at limit", func(t *testing.T) {
		buf := &bytes.Buffer{}
		mutex := &sync.Mutex{}
		writer := &boundedWriter{
			writer: &threadSafeWriter{
				buf:   buf,
				mutex: mutex,
			},
			maxSize:   5,
			truncated: false,
		}

		n, err := writer.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != 5 {
			t.Errorf("Expected to write 5 bytes, got %d", n)
		}
		if buf.String() != "hello" {
			t.Errorf("Expected buffer to contain 'hello', got '%s'", buf.String())
		}
		if writer.truncated {
			t.Error("Writer should not be marked as truncated when exactly at limit")
		}
	})

	t.Run("Write exceeding limit with ASCII", func(t *testing.T) {
		buf := &bytes.Buffer{}
		mutex := &sync.Mutex{}
		writer := &boundedWriter{
			writer: &threadSafeWriter{
				buf:   buf,
				mutex: mutex,
			},
			maxSize:   5,
			truncated: false,
		}

		n, err := writer.Write([]byte("hello world"))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != len("hello world") {
			t.Errorf("Expected consumed byte count %d, got %d", len("hello world"), n)
		}
		if buf.String() != "world" {
			t.Errorf("Expected buffer to contain latest suffix 'world', got '%s'", buf.String())
		}
		if !writer.truncated {
			t.Error("Writer should be marked as truncated")
		}

		// Subsequent writes should keep the latest suffix within limit.
		n2, err := writer.Write([]byte(" more"))
		if err != nil {
			t.Fatalf("Second write failed: %v", err)
		}
		if n2 != 5 {
			t.Errorf("Expected second write to return 5 (length of input), got %d", n2)
		}
		if buf.String() != " more" {
			t.Errorf("Expected buffer to contain latest suffix ' more', got '%s'", buf.String())
		}
	})

	t.Run("Write exceeding limit with UTF-8 characters", func(t *testing.T) {
		buf := &bytes.Buffer{}
		mutex := &sync.Mutex{}
		writer := &boundedWriter{
			writer: &threadSafeWriter{
				buf:   buf,
				mutex: mutex,
			},
			maxSize:   5,
			truncated: false,
		}

		// Write UTF-8 text that would be cut mid-character
		n, err := writer.Write([]byte("héllo world")) // héllo is 6 bytes (h, é is 2 bytes, l, l, o)
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		// Buffer stores the bounded prefix, but Write should report full consumption.
		if n != len("héllo world") {
			t.Errorf("Expected consumed byte count %d, got %d", len("héllo world"), n)
		}
		if buf.String() != "world" {
			t.Errorf("Expected buffer to contain latest suffix 'world', got '%s' (len: %d)", buf.String(), len(buf.String()))
		}
		if !writer.truncated {
			t.Error("Writer should be marked as truncated")
		}
	})

	t.Run("Write exceeding limit inside first UTF-8 rune keeps valid boundary", func(t *testing.T) {
		buf := &bytes.Buffer{}
		mutex := &sync.Mutex{}
		writer := &boundedWriter{
			writer: &threadSafeWriter{
				buf:   buf,
				mutex: mutex,
			},
			maxSize:   1,
			truncated: false,
		}

		n, err := writer.Write([]byte("é"))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != len("é") {
			t.Errorf("Expected consumed byte count %d, got %d", len("é"), n)
		}
		if buf.Len() != 0 {
			t.Errorf("Expected no bytes written because retained suffix would split the rune, got %q", buf.String())
		}
		if !writer.truncated {
			t.Error("Writer should be marked as truncated")
		}
	})

	t.Run("Write with zero limit", func(t *testing.T) {
		buf := &bytes.Buffer{}
		mutex := &sync.Mutex{}
		writer := &boundedWriter{
			writer: &threadSafeWriter{
				buf:   buf,
				mutex: mutex,
			},
			maxSize:   0,
			truncated: false,
		}

		n, err := writer.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != 5 {
			t.Errorf("Expected to return 5 (length of input), got %d", n)
		}
		if buf.String() != "" {
			t.Errorf("Expected buffer to be empty, got '%s'", buf.String())
		}
		if !writer.truncated {
			t.Error("Writer should be marked as truncated with zero limit")
		}
	})

	t.Run("Multiple writes within limit", func(t *testing.T) {
		buf := &bytes.Buffer{}
		mutex := &sync.Mutex{}
		writer := &boundedWriter{
			writer: &threadSafeWriter{
				buf:   buf,
				mutex: mutex,
			},
			maxSize:   11, // "hello world" is 11 bytes
			truncated: false,
		}

		n1, err := writer.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("First write failed: %v", err)
		}
		if n1 != 5 {
			t.Errorf("Expected first write to write 5 bytes, got %d", n1)
		}

		n2, err := writer.Write([]byte(" "))
		if err != nil {
			t.Fatalf("Second write failed: %v", err)
		}
		if n2 != 1 {
			t.Errorf("Expected second write to write 1 byte, got %d", n2)
		}

		n3, err := writer.Write([]byte("world"))
		if err != nil {
			t.Fatalf("Third write failed: %v", err)
		}
		if n3 != 5 {
			t.Errorf("Expected third write to write 5 bytes, got %d", n3)
		}

		if buf.String() != "hello world" {
			t.Errorf("Expected buffer to contain 'hello world', got '%s'", buf.String())
		}
		if writer.truncated {
			t.Error("Writer should not be marked as truncated")
		}
	})
}

func TestTruncateOutputUTF8Boundary(t *testing.T) {
	buf := &bytes.Buffer{}
	mutex := &sync.Mutex{}

	buf.WriteString("abécd")

	out, truncated := truncateOutput(buf, 3, mutex)
	if !truncated {
		t.Fatal("Expected truncation to be reported")
	}
	if out != "cd" {
		t.Fatalf("Expected UTF-8 safe suffix 'cd', got %q", out)
	}
	if buf.String() != "cd" {
		t.Fatalf("Expected buffer to be rewritten with UTF-8 safe suffix, got %q", buf.String())
	}
}

func TestReadTextFileWindow(t *testing.T) {
	t.Parallel()

	testPath := filepath.Join(t.TempDir(), "window.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(testPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	tests := []struct {
		name      string
		startLine int
		limit     *int
		expected  string
	}{
		{
			name:      "keep suffix when no limit",
			startLine: 1,
			limit:     nil,
			expected:  "line2\nline3\n",
		},
		{
			name:      "apply limit",
			startLine: 1,
			limit:     intPtr(1),
			expected:  "line2",
		},
		{
			name:      "start beyond content",
			startLine: 10,
			limit:     nil,
			expected:  "",
		},
		{
			name:      "zero limit returns empty",
			startLine: 0,
			limit:     intPtr(0),
			expected:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := readTextFileWindow(testPath, tc.startLine, tc.limit, maxACPTextFileBytes)
			if err != nil {
				t.Fatalf("readTextFileWindow failed: %v", err)
			}
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}

	t.Run("enforces byte limit", func(t *testing.T) {
		t.Parallel()

		tooLargePath := filepath.Join(t.TempDir(), "too-large.txt")
		tooLarge := strings.Repeat("x", maxACPTextFileBytes+1)
		if err := os.WriteFile(tooLargePath, []byte(tooLarge), 0o644); err != nil {
			t.Fatalf("failed to write large test file: %v", err)
		}

		_, err := readTextFileWindow(tooLargePath, 0, nil, maxACPTextFileBytes)
		if err == nil {
			t.Fatal("expected byte-limit error, got nil")
		}
		if !strings.Contains(err.Error(), "file content too large") {
			t.Fatalf("expected byte-limit error, got: %v", err)
		}
	})
}
