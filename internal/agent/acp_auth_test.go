package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
)

// Helper function to create int pointers for testing
func authTestIntPtr(i int) *int {
	return &i
}

const (
	authAllowOnceOptionID    acp.PermissionOptionId = "allow-once-id"
	authAllowAlwaysOptionID  acp.PermissionOptionId = "allow-always-id"
	authRejectOnceOptionID   acp.PermissionOptionId = "reject-once-id"
	authRejectAlwaysOptionID acp.PermissionOptionId = "reject-always-id"
)

func authTestPermissionOptions() []acp.PermissionOption {
	return []acp.PermissionOption{
		{
			OptionId: authAllowOnceOptionID,
			Kind:     acp.PermissionOptionKindAllowOnce,
			Name:     "Allow once",
		},
		{
			OptionId: authAllowAlwaysOptionID,
			Kind:     acp.PermissionOptionKindAllowAlways,
			Name:     "Allow always",
		},
		{
			OptionId: authRejectOnceOptionID,
			Kind:     acp.PermissionOptionKindRejectOnce,
			Name:     "Reject once",
		},
		{
			OptionId: authRejectAlwaysOptionID,
			Kind:     acp.PermissionOptionKindRejectAlways,
			Name:     "Reject always",
		},
	}
}

// TestACPAuthPermissionModes tests RequestPermission behavior and mode switching
// This provides context for the H2 authorization bypass fix
func TestACPAuthPermissionModes(t *testing.T) {
	t.Parallel()

	t.Run("RequestPermission: ReadOnlyMode denies destructive operations", func(t *testing.T) {
		// Create agent in read-only mode (non-agentic)
		agent := &ACPAgent{
			agentName:       "test-acp",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "plan", // Start in read-only mode
			Model:           "test-model",
			Timeout:         30 * time.Second,
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test destructive operation (edit)
		editKind := acp.ToolKind("edit")
		response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &editKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed: %v", err)
		}

		// Should be denied for destructive operations in read-only mode
		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != authRejectAlwaysOptionID {
			t.Errorf("Expected %q for destructive operation in read-only mode, got '%v'", authRejectAlwaysOptionID, response.Outcome)
		}
	})

	t.Run("RequestPermission: ReadOnlyMode allows non-destructive operations", func(t *testing.T) {
		// Create agent in read-only mode (non-agentic)
		agent := &ACPAgent{
			agentName:       "test-acp",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "plan", // Start in read-only mode
			Model:           "test-model",
			Timeout:         30 * time.Second,
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test non-destructive operation (read)
		readKind := acp.ToolKind("read")
		response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &readKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed: %v", err)
		}

		// Should be allowed for non-destructive operations in read-only mode
		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != authAllowAlwaysOptionID {
			t.Errorf("Expected %q for non-destructive operation in read-only mode, got '%v'", authAllowAlwaysOptionID, response.Outcome)
		}
	})

	t.Run("RequestPermission: Empty mode defaults to read-only behavior for permissions", func(t *testing.T) {
		agent := &ACPAgent{
			agentName:       "test-acp",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "",
			Model:           "test-model",
			Timeout:         30 * time.Second,
		}

		client := &acpClient{
			agent: agent,
		}

		readKind := acp.ToolKind("read")
		readResponse, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &readKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed for read kind: %v", err)
		}
		if readResponse.Outcome.Selected == nil || readResponse.Outcome.Selected.OptionId != authAllowAlwaysOptionID {
			t.Errorf("Expected %q for non-destructive operation when mode is disabled, got '%v'", authAllowAlwaysOptionID, readResponse.Outcome)
		}

		editKind := acp.ToolKind("edit")
		editResponse, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &editKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed for edit kind: %v", err)
		}
		if editResponse.Outcome.Selected == nil || editResponse.Outcome.Selected.OptionId != authRejectAlwaysOptionID {
			t.Errorf("Expected %q for destructive operation when mode is disabled, got '%v'", authRejectAlwaysOptionID, editResponse.Outcome)
		}
	})

	t.Run("RequestPermission: AutoApproveMode allows all known operations", func(t *testing.T) {
		// Create agent in auto-approve mode (agentic)
		agent := &ACPAgent{
			agentName:       "test-acp",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "auto-approve", // Start in auto-approve mode
			Model:           "test-model",
			Timeout:         30 * time.Second,
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test destructive operation (should be allowed in auto-approve mode)
		editKind := acp.ToolKind("edit")
		response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &editKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed: %v", err)
		}

		// Should be allowed for known operations in auto-approve mode
		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != authAllowAlwaysOptionID {
			t.Errorf("Expected %q for known operation in auto-approve mode, got '%v'", authAllowAlwaysOptionID, response.Outcome)
		}
	})

	t.Run("RequestPermission: Unknown tool kinds are always denied", func(t *testing.T) {
		// Create agent in auto-approve mode (agentic)
		agent := &ACPAgent{
			agentName:       "test-acp",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "auto-approve", // Start in auto-approve mode
			Model:           "test-model",
			Timeout:         30 * time.Second,
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test unknown operation
		unknownKind := acp.ToolKind("unknown-operation")
		response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &unknownKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed: %v", err)
		}

		// Unknown tool kinds should always be denied for safety
		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != authRejectAlwaysOptionID {
			t.Errorf("Expected %q for unknown operation, got '%v'", authRejectAlwaysOptionID, response.Outcome)
		}
	})

	t.Run("RequestPermission: Known destructive operations are denied outside explicit auto-approve mode", func(t *testing.T) {
		agent := &ACPAgent{
			agentName:       "test-acp",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "custom-mode",
			Model:           "test-model",
			Timeout:         30 * time.Second,
		}

		client := &acpClient{
			agent: agent,
		}

		editKind := acp.ToolKind("edit")
		response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &editKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed: %v", err)
		}
		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != authRejectAlwaysOptionID {
			t.Errorf("Expected %q outside explicit auto-approve mode, got '%v'", authRejectAlwaysOptionID, response.Outcome)
		}
	})

	t.Run("RequestPermission: Nil ToolCall.Kind is denied", func(t *testing.T) {
		// Create agent in auto-approve mode (agentic)
		agent := &ACPAgent{
			agentName:       "test-acp",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "auto-approve", // Start in auto-approve mode
			Model:           "test-model",
			Timeout:         30 * time.Second,
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test nil ToolCall.Kind
		response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: nil, // Explicitly nil
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed: %v", err)
		}

		// Nil ToolCall.Kind should always be denied
		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != authRejectAlwaysOptionID {
			t.Errorf("Expected %q for nil ToolCall.Kind, got '%v'", authRejectAlwaysOptionID, response.Outcome)
		}
	})

	t.Run("Mode switching: WithAgentic sets correct mode", func(t *testing.T) {
		// Create base agent
		baseAgent := &ACPAgent{
			agentName:       "test-acp",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "plan", // Start in read-only mode
			Model:           "test-model",
			Timeout:         30 * time.Second,
		}

		// Test non-agentic mode (should use read-only mode)
		nonAgenticAgent := baseAgent.WithAgentic(false).(*ACPAgent)
		if nonAgenticAgent.Mode != "plan" {
			t.Errorf("Non-agentic mode should be 'plan', got '%s'", nonAgenticAgent.Mode)
		}

		// Test agentic mode (should use auto-approve mode)
		agenticAgent := baseAgent.WithAgentic(true).(*ACPAgent)
		if agenticAgent.Mode != "auto-approve" {
			t.Errorf("Agentic mode should be 'auto-approve', got '%s'", agenticAgent.Mode)
		}
	})
}

// TestACPAuthDirectEnforcement tests H2 authorization bypass fix
// Direct authorization enforcement at operation entry points
func TestACPAuthDirectEnforcement(t *testing.T) {
	t.Parallel()

	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Create agent with temp directory as repo root
	agent := &ACPAgent{
		agentName:       "test-acp",
		Command:         "test-command",
		ReadOnlyMode:    "plan",
		AutoApproveMode: "auto-approve",
		Mode:            "plan", // Start in read-only mode
		Model:           "test-model",
		Timeout:         30,
		repoRoot:        tempDir,
	}

	client := &acpClient{
		agent:     agent,
		terminals: make(map[string]*acpTerminal),
	}

	t.Run("H2: WriteTextFile authorization in read-only mode", func(t *testing.T) {
		// Test that WriteTextFile is blocked in read-only mode
		_, err := client.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
			Path:    "test.txt",
			Content: "test content",
		})
		if err == nil {
			t.Errorf("Expected authorization error in read-only mode, got nil")
		} else if !strings.Contains(err.Error(), "write operation not permitted in read-only mode") {
			t.Errorf("Expected specific authorization error message, got: %v", err)
		}
	})

	t.Run("H2: CreateTerminal authorization in read-only mode", func(t *testing.T) {
		// Test that CreateTerminal is blocked in read-only mode
		cmd, args := acpTestEchoCommand("test")
		_, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			Command: cmd,
			Args:    args,
		})
		if err == nil {
			t.Errorf("Expected authorization error in read-only mode, got nil")
		} else if !strings.Contains(err.Error(), "terminal creation not permitted in read-only mode") {
			t.Errorf("Expected specific authorization error message, got: %v", err)
		}
	})

	t.Run("H2: Authorization bypass prevention - direct method calls", func(t *testing.T) {
		// Test that authorization is enforced even when calling methods directly
		// This simulates a compromised agent trying to bypass RequestPermission
		testCases := []struct {
			name string
			exec func() error
		}{
			{
				name: "WriteTextFile direct call",
				exec: func() error {
					_, err := client.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
						Path:    "malicious.txt",
						Content: "malicious content",
					})
					return err
				},
			},
			{
				name: "CreateTerminal direct call",
				exec: func() error {
					_, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
						Command: "rm",
						Args:    []string{"-rf", "/"},
					})
					return err
				},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				err := tc.exec()
				if err == nil {
					t.Errorf("Expected authorization error for %s, got nil", tc.name)
				} else if !strings.Contains(err.Error(), "not permitted in read-only mode") {
					t.Errorf("Expected authorization error for %s, got: %v", tc.name, err)
				}
			})
		}
	})

	t.Run("H2: Auto-approve mode allows mutating operations", func(t *testing.T) {
		// Create agent in auto-approve mode
		autoApproveAgent := &ACPAgent{
			agentName:       "test-acp-auto",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "auto-approve", // Auto-approve mode
			Model:           "test-model",
			Timeout:         30,
			repoRoot:        tempDir,
		}

		autoApproveClient := &acpClient{
			agent:     autoApproveAgent,
			terminals: make(map[string]*acpTerminal), // Initialize terminals map
		}

		// Test that WriteTextFile is allowed in auto-approve mode
		tempFile := filepath.Join(tempDir, "auto-approve-test.txt")
		_, err := autoApproveClient.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
			Path:    "auto-approve-test.txt",
			Content: "test content",
		})
		if err != nil {
			t.Errorf("Expected success in auto-approve mode, got error: %v", err)
		} else {
			// Verify file was actually created
			defer os.Remove(tempFile)
			if _, err := os.Stat(tempFile); os.IsNotExist(err) {
				t.Errorf("File was not created despite successful WriteTextFile call")
			}
		}

		t.Run("WriteTextFile preserves existing executable bit", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX permission bits are not consistent on Windows")
			}

			targetPath := filepath.Join(tempDir, "exec-mode.sh")
			if err := os.WriteFile(targetPath, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
				t.Fatalf("Failed to create executable test file: %v", err)
			}
			defer os.Remove(targetPath)
			originalInfo, err := os.Stat(targetPath)
			if err != nil {
				t.Fatalf("Failed to stat original file: %v", err)
			}
			originalMode := originalInfo.Mode().Perm()

			if _, err := autoApproveClient.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
				Path:    "exec-mode.sh",
				Content: "#!/bin/sh\necho new\n",
			}); err != nil {
				t.Fatalf("WriteTextFile failed: %v", err)
			}

			info, err := os.Stat(targetPath)
			if err != nil {
				t.Fatalf("Failed to stat updated file: %v", err)
			}
			if info.Mode().Perm() != originalMode {
				t.Fatalf("Expected mode %04o to be preserved, got %04o", originalMode, info.Mode().Perm())
			}
		})

		t.Run("WriteTextFile respects existing read-only file permissions", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX permission bits are not consistent on Windows")
			}

			targetPath := filepath.Join(tempDir, "read-only.txt")
			if err := os.WriteFile(targetPath, []byte("old"), 0o444); err != nil {
				t.Fatalf("Failed to create read-only test file: %v", err)
			}
			defer func() {
				_ = os.Chmod(targetPath, 0o644)
				_ = os.Remove(targetPath)
			}()

			// Skip this assertion in environments where read-only files remain writable
			// (for example privileged users with DAC override).
			probe, probeErr := os.OpenFile(targetPath, os.O_WRONLY, 0)
			if probeErr == nil {
				_ = probe.Close()
				t.Skip("environment allows writes to read-only files")
			}

			if _, err := autoApproveClient.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
				Path:    "read-only.txt",
				Content: "new",
			}); err == nil {
				t.Fatal("Expected WriteTextFile to fail for read-only existing file")
			}
		})

		// Test that CreateTerminal is allowed in auto-approve mode
		cmd, args := acpTestEchoCommand("test")
		_, err = autoApproveClient.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			Command: cmd,
			Args:    args,
		})
		if err != nil {
			t.Errorf("Expected success in auto-approve mode, got error: %v", err)
		}
	})

	t.Run("H2: Mode switching validation", func(t *testing.T) {
		// Test that switching from read-only to auto-approve works correctly
		switchableAgent := &ACPAgent{
			agentName:       "test-acp-switch",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "plan", // Start in read-only
			Model:           "test-model",
			Timeout:         30,
			repoRoot:        tempDir,
		}

		switchableClient := &acpClient{
			agent:     switchableAgent,
			terminals: make(map[string]*acpTerminal), // Initialize terminals map
		}

		// Should be blocked in read-only mode
		_, err := switchableClient.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
			Path:    "test.txt",
			Content: "test",
		})
		if err == nil {
			t.Errorf("Expected authorization error in read-only mode")
		}

		// Switch to auto-approve mode
		switchableAgent.Mode = "auto-approve"

		// Should now be allowed
		tempFile := filepath.Join(tempDir, "switch-test.txt")
		_, err = switchableClient.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
			Path:    "switch-test.txt",
			Content: "test",
		})
		if err != nil {
			t.Errorf("Expected success after switching to auto-approve mode, got: %v", err)
		} else {
			defer os.Remove(tempFile)
		}
	})

	t.Run("H2: Non-read-only custom mode still blocks mutating operations", func(t *testing.T) {
		customModeAgent := &ACPAgent{
			agentName:       "test-acp-custom",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "custom-mode",
			Model:           "test-model",
			Timeout:         30,
			repoRoot:        tempDir,
		}

		customModeClient := &acpClient{
			agent:     customModeAgent,
			terminals: make(map[string]*acpTerminal),
		}

		_, err := customModeClient.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
			Path:    "custom-mode.txt",
			Content: "test",
		})
		if err == nil {
			t.Fatalf("Expected write to be blocked outside explicit auto-approve mode")
		}
		if !strings.Contains(err.Error(), "auto-approve mode is explicitly enabled") {
			t.Fatalf("Expected explicit auto-approve mode error, got: %v", err)
		}

		cmd, args := acpTestEchoCommand("test")
		_, err = customModeClient.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
			Command: cmd,
			Args:    args,
		})
		if err == nil {
			t.Fatalf("Expected terminal creation to be blocked outside explicit auto-approve mode")
		}
		if !strings.Contains(err.Error(), "auto-approve mode is explicitly enabled") {
			t.Fatalf("Expected explicit auto-approve mode error, got: %v", err)
		}
	})
}

// TestACPAuthEdgeCases tests security edge cases and validation
func TestACPAuthEdgeCases(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	// On Windows, t.TempDir() may return an 8.3 short path (e.g.
	// RUNNER~1) while filepath.EvalSymlinks resolves to the long
	// form (runneradmin). Normalize so substring checks match.
	if resolved, err := filepath.EvalSymlinks(tempDir); err == nil {
		tempDir = resolved
	}

	agent := &ACPAgent{
		agentName:       "test-acp",
		Command:         "test-command",
		ReadOnlyMode:    "plan",
		AutoApproveMode: "auto-approve",
		Mode:            "plan", // Start in read-only mode
		Model:           "test-model",
		Timeout:         30,
		repoRoot:        tempDir,
	}

	client := &acpClient{
		agent:     agent,
		terminals: make(map[string]*acpTerminal),
	}

	t.Run("Path validation prevents directory traversal", func(t *testing.T) {
		// Test path traversal attempt
		_, err := client.validateAndResolvePath("../../../etc/passwd", false) // false = read operation
		if err == nil {
			t.Errorf("Expected error for path traversal, got nil")
		}
	})

	t.Run("Path validation prevents symlink traversal", func(t *testing.T) {
		// Create a symlink outside the repo
		symlinkPath := filepath.Join(tempDir, "symlink")
		targetPath := "/etc/passwd" // This should be blocked
		err := os.Symlink(targetPath, symlinkPath)
		if err != nil {
			t.Skip("Could not create symlink for test")
		}
		defer os.Remove(symlinkPath)

		// Test symlink traversal attempt
		_, err = client.validateAndResolvePath("symlink", false) // false = read operation
		if err == nil {
			t.Errorf("Expected error for symlink traversal, got nil")
		}
	})

	t.Run("Path validation blocks writes to symlinks escaping repo root", func(t *testing.T) {
		externalDir := t.TempDir()
		externalTarget := filepath.Join(externalDir, "outside.txt")
		if err := os.WriteFile(externalTarget, []byte("outside"), 0644); err != nil {
			t.Fatalf("Failed to create external target: %v", err)
		}

		symlinkPath := filepath.Join(tempDir, "write-link-outside")
		if err := os.Symlink(externalTarget, symlinkPath); err != nil {
			t.Skip("Could not create symlink for write traversal test")
		}
		defer os.Remove(symlinkPath)

		_, err := client.validateAndResolvePath("write-link-outside", true) // true = write operation
		if err == nil {
			t.Fatal("Expected error for write symlink traversal, got nil")
		}
	})

	t.Run("Path validation allows writes to symlinks that resolve inside repo root", func(t *testing.T) {
		internalTarget := filepath.Join(tempDir, "inside.txt")
		if err := os.WriteFile(internalTarget, []byte("inside"), 0644); err != nil {
			t.Fatalf("Failed to create internal target: %v", err)
		}
		defer os.Remove(internalTarget)

		symlinkPath := filepath.Join(tempDir, "write-link-inside")
		if err := os.Symlink(internalTarget, symlinkPath); err != nil {
			t.Skip("Could not create symlink for write validation test")
		}
		defer os.Remove(symlinkPath)

		resolvedPath, err := client.validateAndResolvePath("write-link-inside", true) // true = write operation
		if err != nil {
			t.Fatalf("Unexpected error for in-repo symlink write target: %v", err)
		}
		if !strings.HasSuffix(resolvedPath, "inside.txt") {
			t.Fatalf("Expected resolved path to use resolved target path, got %s", resolvedPath)
		}
	})

	t.Run("Path validation allows valid paths", func(t *testing.T) {
		// Create a valid file
		validFile := filepath.Join(tempDir, "valid.txt")
		err := os.WriteFile(validFile, []byte("test"), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}
		defer os.Remove(validFile)

		// Test valid path
		resolvedPath, err := client.validateAndResolvePath("valid.txt", false) // false = read operation
		if err != nil {
			t.Errorf("Unexpected error for valid path: %v", err)
		}
		// Check that the resolved path is within the temp directory
		if !strings.HasSuffix(resolvedPath, "valid.txt") {
			t.Errorf("Expected resolved path to end with 'valid.txt', got %s", resolvedPath)
		}
		if !strings.Contains(resolvedPath, tempDir) {
			t.Errorf("Expected resolved path to contain temp dir %s, got %s", tempDir, resolvedPath)
		}
	})

	t.Run("Numeric parameter validation", func(t *testing.T) {
		// Test negative line number
		_, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{
			Path: "test.txt",
			Line: authTestIntPtr(-1),
		})
		if err == nil {
			t.Errorf("Expected error for negative line number, got nil")
		}

		// Test negative limit
		_, err = client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{
			Path:  "test.txt",
			Limit: authTestIntPtr(-1),
		})
		if err == nil {
			t.Errorf("Expected error for negative limit, got nil")
		}

		// Test empty path
		_, err = client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{
			Path: "",
		})
		if err == nil {
			t.Errorf("Expected error for empty path, got nil")
		}

		// Test excessively large line number
		_, err = client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{
			Path: "test.txt",
			Line: authTestIntPtr(2000000),
		})
		if err == nil {
			t.Errorf("Expected error for excessively large line number, got nil")
		}

		// Test excessively large limit
		_, err = client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{
			Path:  "test.txt",
			Limit: authTestIntPtr(2000000),
		})
		if err == nil {
			t.Errorf("Expected error for excessively large limit, got nil")
		}
	})

	t.Run("WriteTextFile input validation", func(t *testing.T) {
		// Create agent and client
		agent := &ACPAgent{
			agentName:       "test-acp",
			Command:         "test-command",
			ReadOnlyMode:    "plan",
			AutoApproveMode: "auto-approve",
			Mode:            "auto-approve", // Start in auto-approve mode
			Model:           "test-model",
			Timeout:         30 * time.Second,
		}

		client := &acpClient{
			agent: agent,
		}

		// Test empty path
		_, err := client.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
			Path:    "",
			Content: "test content",
		})
		if err == nil {
			t.Errorf("Expected error for empty path, got nil")
		}

		// Test excessively large content
		largeContent := string(make([]byte, 11000000)) // 11MB > 10MB limit
		_, err = client.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
			Path:    "test.txt",
			Content: largeContent,
		})
		if err == nil {
			t.Errorf("Expected error for excessively large content, got nil")
		}
	})

	t.Run("Permission logic defaults to deny", func(t *testing.T) {
		// Test unknown tool kind
		unknownKind := acp.ToolKind("unknown")
		response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &unknownKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed: %v", err)
		}

		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != authRejectAlwaysOptionID {
			t.Errorf("Expected %q for unknown operation, got '%v'", authRejectAlwaysOptionID, response.Outcome)
		}
	})

	t.Run("Read-only mode denies destructive operations", func(t *testing.T) {
		// Test destructive operation in read-only mode
		editKind := acp.ToolKind("edit")
		response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &editKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed: %v", err)
		}

		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != authRejectAlwaysOptionID {
			t.Errorf("Expected %q for destructive operation in read-only mode, got '%v'", authRejectAlwaysOptionID, response.Outcome)
		}
	})

	t.Run("Read-only mode allows non-destructive operations", func(t *testing.T) {
		// Test non-destructive operation in read-only mode
		readKind := acp.ToolKind("read")
		response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			Options: authTestPermissionOptions(),
			ToolCall: acp.RequestPermissionToolCall{
				Kind: &readKind,
			},
		})
		if err != nil {
			t.Fatalf("RequestPermission failed: %v", err)
		}

		if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != authAllowAlwaysOptionID {
			t.Errorf("Expected %q for non-destructive operation in read-only mode, got '%v'", authAllowAlwaysOptionID, response.Outcome)
		}
	})
}
