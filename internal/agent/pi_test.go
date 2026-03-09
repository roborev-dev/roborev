package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePiJSON(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"session","id":"pi-session-123"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hello"},{"type":"text","text":"World"}]}}`,
	}, "\n") + "\n"

	result, err := parsePiJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parsePiJSON: %v", err)
	}
	if result != "Hello\nWorld" {
		t.Fatalf("parsePiJSON() = %q, want %q", result, "Hello\nWorld")
	}
}

func TestResolvePiSessionPath(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dataDir)

	sessionID := "46109439-3160-40f0-81e7-7dfa4f3647b3"
	sessionPath := filepath.Join(dataDir, "sessions", "--repo--", "2026-03-08T18-44-39-718Z_"+sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if got := resolvePiSessionPath(sessionID); got != sessionPath {
		t.Fatalf("resolvePiSessionPath() = %q, want %q", got, sessionPath)
	}
}

func TestPiReviewSessionFlag(t *testing.T) {
	skipIfWindows(t)

	dataDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dataDir)

	sessionID := "46109439-3160-40f0-81e7-7dfa4f3647b3"
	sessionPath := filepath.Join(dataDir, "sessions", "--repo--", "2026-03-08T18-44-39-718Z_"+sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs: true,
		StdoutLines: []string{
			`{"type":"session","id":"` + sessionID + `"}`,
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
		},
	})

	a := NewPiAgent(mock.CmdPath).WithSessionID(sessionID).(*PiAgent)
	result, err := a.Review(context.Background(), t.TempDir(), "HEAD", "prompt", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}
	if result != "ok" {
		t.Fatalf("Review() = %q, want %q", result, "ok")
	}

	args := readMockArgs(t, mock.ArgsFile)
	assertContainsArg(t, args, "--session")
	assertContainsArg(t, args, sessionPath)
	assertContainsArg(t, args, "--mode")
	assertContainsArg(t, args, "json")
}
