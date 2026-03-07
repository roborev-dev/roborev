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

	tests := []struct {
		name          string
		agentSession  string
		clientSession string
		requestID     acp.SessionId
		wantErr       bool
		wantErrMsg    string
	}{
		{
			name:         "Valid session ID matching agent session",
			agentSession: "test-session-123",
			requestID:    "test-session-123",
			wantErr:      false,
		},
		{
			name:         "Invalid session ID not matching agent session",
			agentSession: "test-session-123",
			requestID:    "wrong-session-456",
			wantErr:      true,
			wantErrMsg:   "session ID mismatch: expected test-session-123, got wrong-session-456",
		},
		{
			name:         "Empty agent session ID rejects non-empty request session ID",
			agentSession: "",
			requestID:    "any-session-789",
			wantErr:      true,
			wantErrMsg:   "session ID mismatch: no active session, got any-session-789",
		},
		{
			name:         "Empty request session ID with non-empty agent session",
			agentSession: "test-session-123",
			requestID:    "",
			wantErr:      true,
			wantErrMsg:   "session ID mismatch: expected test-session-123, got ",
		},
		{
			name:         "Both session IDs empty",
			agentSession: "",
			requestID:    "",
			wantErr:      false,
		},
		{
			name:          "Client session ID takes precedence over agent session ID",
			agentSession:  "agent-session",
			clientSession: "client-session",
			requestID:     "agent-session",
			wantErr:       true,
			wantErrMsg:    "session ID mismatch: expected client-session, got agent-session",
		},
		{
			name:          "Client session ID matches request",
			agentSession:  "agent-session",
			clientSession: "client-session",
			requestID:     "client-session",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &acpClient{
				agent:     &ACPAgent{SessionId: tt.agentSession},
				sessionID: tt.clientSession,
			}

			err := client.validateSessionID(tt.requestID)

			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSessionID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && err.Error() != tt.wantErrMsg {
				t.Errorf("error %q did not match expected %q", err.Error(), tt.wantErrMsg)
			}
		})
	}
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

	tests := []struct {
		name       string
		validateFn func() error
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "Configured mode requires mode capability",
			validateFn: func() error {
				return validateConfiguredMode("plan", nil)
			},
			wantErr:    true,
			wantErrMsg: "agent does not support session modes (configured mode: plan)",
		},
		{
			name: "Configured model requires model capability",
			validateFn: func() error {
				return validateConfiguredModel("model-x", nil)
			},
			wantErr:    true,
			wantErrMsg: "agent does not support session models (configured model: model-x)",
		},
		{
			name: "Mode validation succeeds when available",
			validateFn: func() error {
				modeState := &acp.SessionModeState{
					AvailableModes: []acp.SessionMode{
						{Id: acp.SessionModeId("plan"), Name: "Plan"},
					},
					CurrentModeId: acp.SessionModeId("plan"),
				}
				return validateConfiguredMode("plan", modeState)
			},
			wantErr: false,
		},
		{
			name: "Model validation succeeds when available",
			validateFn: func() error {
				modelState := &acp.SessionModelState{
					AvailableModels: []acp.ModelInfo{
						{ModelId: acp.ModelId("model-x"), Name: "Model X"},
					},
					CurrentModelId: acp.ModelId("model-x"),
				}
				return validateConfiguredModel("model-x", modelState)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.validateFn()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validation error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && err.Error() != tt.wantErrMsg {
				t.Fatalf("Expected error %q, got: %q", tt.wantErrMsg, err.Error())
			}
		})
	}
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
