package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"
)

// TestValidateSessionID tests the session ID validation logic
func TestValidateSessionID(t *testing.T) {
	t.Parallel()

	t.Run("Valid session ID matching agent session", func(t *testing.T) {
		// Create agent with a session ID
		agent := &ACPAgent{
			SessionId: "test-session-123",
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test with matching session ID
		sessionID := acp.SessionId("test-session-123")
		err := client.validateSessionID(sessionID)
		if err != nil {
			t.Errorf("Expected no error for matching session ID, got: %v", err)
		}
	})

	t.Run("Invalid session ID not matching agent session", func(t *testing.T) {
		// Create agent with a session ID
		agent := &ACPAgent{
			SessionId: "test-session-123",
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test with non-matching session ID
		sessionID := acp.SessionId("wrong-session-456")
		err := client.validateSessionID(sessionID)
		if err == nil {
			t.Error("Expected error for non-matching session ID")
		} else if err.Error() != "session ID mismatch: expected test-session-123, got wrong-session-456" {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})

	t.Run("Empty agent session ID rejects non-empty request session ID", func(t *testing.T) {
		// Create agent with empty session ID (before session is established)
		agent := &ACPAgent{
			SessionId: "",
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test with non-empty session ID in request.
		sessionID := acp.SessionId("any-session-789")
		err := client.validateSessionID(sessionID)
		if err == nil {
			t.Error("Expected error for non-empty request session ID before session is established")
		} else if err.Error() != "session ID mismatch: no active session, got any-session-789" {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})

	t.Run("Empty request session ID with non-empty agent session", func(t *testing.T) {
		// Create agent with a session ID
		agent := &ACPAgent{
			SessionId: "test-session-123",
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test with empty session ID in request
		sessionID := acp.SessionId("")
		err := client.validateSessionID(sessionID)
		if err == nil {
			t.Error("Expected error for empty session ID when agent has session ID")
		} else if err.Error() != "session ID mismatch: expected test-session-123, got " {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})

	t.Run("Both session IDs empty", func(t *testing.T) {
		// Create agent with empty session ID
		agent := &ACPAgent{
			SessionId: "",
		}

		// Create client
		client := &acpClient{
			agent: agent,
		}

		// Test with empty session ID in request
		sessionID := acp.SessionId("")
		err := client.validateSessionID(sessionID)
		if err != nil {
			t.Errorf("Expected no error when both session IDs are empty, got: %v", err)
		}
	})

	t.Run("Client session ID takes precedence over agent session ID", func(t *testing.T) {
		agent := &ACPAgent{
			SessionId: "agent-session",
		}
		client := &acpClient{
			agent:     agent,
			sessionID: "client-session",
		}

		if err := client.validateSessionID(acp.SessionId("client-session")); err != nil {
			t.Fatalf("Expected client session ID to validate, got: %v", err)
		}

		err := client.validateSessionID(acp.SessionId("agent-session"))
		if err == nil {
			t.Fatal("Expected mismatch when request matches agent session but not client session")
		}
		if !strings.Contains(err.Error(), "expected client-session") {
			t.Fatalf("Expected error to use client session ID, got: %v", err)
		}
	})
}

func TestValidateAndResolvePathUsesClientRepoRootPrecedence(t *testing.T) {
	t.Parallel()

	clientRoot := t.TempDir()
	agentRoot := t.TempDir()

	clientFile := filepath.Join(clientRoot, "client.txt")
	if err := os.WriteFile(clientFile, []byte("ok"), 0o644); err != nil {
		t.Fatalf("Failed to create client-root file: %v", err)
	}

	agent := &ACPAgent{
		repoRoot: agentRoot,
	}
	client := &acpClient{
		agent:    agent,
		repoRoot: clientRoot,
	}

	resolvedPath, err := client.validateAndResolvePath("client.txt", false)
	if err != nil {
		t.Fatalf("Expected client repo root to be used, got error: %v", err)
	}

	resolvedClientRoot, err := filepath.EvalSymlinks(clientRoot)
	if err != nil {
		t.Fatalf("Failed to resolve client root: %v", err)
	}

	resolvedAgentRoot, err := filepath.EvalSymlinks(agentRoot)
	if err != nil {
		t.Fatalf("Failed to resolve agent root: %v", err)
	}

	if !pathWithinRoot(resolvedPath, resolvedClientRoot) {
		t.Fatalf("Expected resolved path %q to be under client repo root %q", resolvedPath, resolvedClientRoot)
	}
	if pathWithinRoot(resolvedPath, resolvedAgentRoot) {
		t.Fatalf("Expected resolved path %q to avoid agent repo root %q", resolvedPath, resolvedAgentRoot)
	}
}

func TestValidateConfiguredSessionCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("Configured mode requires mode capability", func(t *testing.T) {
		err := validateConfiguredMode("plan", nil)
		if err == nil {
			t.Fatal("Expected error when mode is configured but session mode capability is absent")
		}
		if !strings.Contains(err.Error(), "does not support session modes") {
			t.Fatalf("Expected unsupported mode capability error, got: %v", err)
		}
	})

	t.Run("Configured model requires model capability", func(t *testing.T) {
		err := validateConfiguredModel("model-x", nil)
		if err == nil {
			t.Fatal("Expected error when model is configured but session model capability is absent")
		}
		if !strings.Contains(err.Error(), "does not support session models") {
			t.Fatalf("Expected unsupported model capability error, got: %v", err)
		}
	})

	t.Run("Mode and model validation succeeds when available", func(t *testing.T) {
		modeState := &acp.SessionModeState{
			AvailableModes: []acp.SessionMode{
				{Id: acp.SessionModeId("plan"), Name: "Plan"},
			},
			CurrentModeId: acp.SessionModeId("plan"),
		}
		modelState := &acp.SessionModelState{
			AvailableModels: []acp.ModelInfo{
				{ModelId: acp.ModelId("model-x"), Name: "Model X"},
			},
			CurrentModelId: acp.ModelId("model-x"),
		}

		if err := validateConfiguredMode("plan", modeState); err != nil {
			t.Fatalf("Expected mode validation success, got: %v", err)
		}
		if err := validateConfiguredModel("model-x", modelState); err != nil {
			t.Fatalf("Expected model validation success, got: %v", err)
		}
	})
}

func TestSessionUpdateValidatesSessionID(t *testing.T) {
	t.Parallel()

	client := &acpClient{
		agent: &ACPAgent{
			SessionId: "expected-session",
		},
		result: &bytes.Buffer{},
	}

	messageChunk := &acp.SessionUpdateAgentMessageChunk{
		Content:       acp.TextBlock("hello"),
		SessionUpdate: "agent_message_chunk",
	}

	err := client.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: acp.SessionId("wrong-session"),
		Update: acp.SessionUpdate{
			AgentMessageChunk: messageChunk,
		},
	})
	if err == nil {
		t.Fatal("Expected session mismatch error for SessionUpdate")
	}
	if !strings.Contains(err.Error(), "session ID mismatch") {
		t.Fatalf("Expected session mismatch error, got: %v", err)
	}
	if client.result.String() != "" {
		t.Fatalf("Expected no output to be appended on session mismatch, got: %q", client.result.String())
	}

	if err := client.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: acp.SessionId("expected-session"),
		Update: acp.SessionUpdate{
			AgentMessageChunk: messageChunk,
		},
	}); err != nil {
		t.Fatalf("Expected valid SessionUpdate to succeed, got: %v", err)
	}
	if client.result.String() != "hello" {
		t.Fatalf("Expected session output to be appended, got: %q", client.result.String())
	}
}
