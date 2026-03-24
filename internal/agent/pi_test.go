package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePiJSON(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"session","id":"pi-session-123"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hello"},{"type":"text","text":"World"}]}}`,
	}, "\n") + "\n"

	result, err := parsePiJSON(strings.NewReader(input))
	require.NoError(t, err)
	assert.Equal(t, "Hello\nWorld", result)
}

func TestParsePiJSONLargeMessage(t *testing.T) {
	bigText := strings.Repeat("x", 128*1024)
	input := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"` + bigText + `"}]}}` + "\n"

	result, err := parsePiJSON(strings.NewReader(input))
	require.NoError(t, err)
	assert.Equal(t, bigText, result)
}

func TestResolvePiSessionPath(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dataDir)

	sessionID := "46109439-3160-40f0-81e7-7dfa4f3647b3"
	sessionPath := filepath.Join(dataDir, "sessions", "--repo--", "2026-03-08T18-44-39-718Z_"+sessionID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionPath), 0o755))
	require.NoError(t, os.WriteFile(sessionPath, []byte("{}\n"), 0o644))
	assert.Equal(t, sessionPath, resolvePiSessionPath(sessionID))
}

func TestPiReviewSessionFlag(t *testing.T) {
	skipIfWindows(t)

	dataDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dataDir)

	sessionID := "46109439-3160-40f0-81e7-7dfa4f3647b3"
	sessionPath := filepath.Join(dataDir, "sessions", "--repo--", "2026-03-08T18-44-39-718Z_"+sessionID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionPath), 0o755))
	require.NoError(t, os.WriteFile(sessionPath, []byte("{}\n"), 0o644))

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs: true,
		StdoutLines: []string{
			`{"type":"session","id":"` + sessionID + `"}`,
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
		},
	})

	a := NewPiAgent(mock.CmdPath).WithSessionID(sessionID).(*PiAgent)
	result, err := a.Review(context.Background(), t.TempDir(), "HEAD", "prompt", &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)

	args := readMockArgs(t, mock.ArgsFile)
	assertContainsArg(t, args, "--session")
	assertContainsArg(t, args, sessionPath)
	assertContainsArg(t, args, "--mode")
	assertContainsArg(t, args, "json")
}

func TestPiCommandLineOmitsResolvedSessionPath(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dataDir)

	sessionID := "46109439-3160-40f0-81e7-7dfa4f3647b3"
	sessionPath := filepath.Join(dataDir, "sessions", "--repo--", "2026-03-08T18-44-39-718Z_"+sessionID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionPath), 0o755))
	require.NoError(t, os.WriteFile(sessionPath, []byte("{}\n"), 0o644))

	a := NewPiAgent("pi").WithSessionID(sessionID).(*PiAgent)
	cmdLine := a.CommandLine()

	assert.NotContains(t, cmdLine, "--session")
	assert.NotContains(t, cmdLine, sessionPath)
	assert.Contains(t, cmdLine, "-p --mode json")
}
