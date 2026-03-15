package daemon

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenCaptureWriter_Passthrough(t *testing.T) {
	var dst bytes.Buffer
	w := newTokenCaptureWriter(&dst)

	data := []byte("hello world\n")
	n, err := w.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)
	require.Equal(t, "hello world\n", dst.String())
}

func TestTokenCaptureWriter_CodexTokens(t *testing.T) {
	var dst bytes.Buffer
	w := newTokenCaptureWriter(&dst)

	// Simulate codex turn.completed events
	lines := `{"type":"turn.started"}
{"type":"message","content":"review output"}
{"type":"turn.completed","usage":{"input_tokens":1000,"output_tokens":500}}
{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":200,"output_tokens":100}}
`
	_, err := w.Write([]byte(lines))
	require.NoError(t, err)

	require.Equal(t, int64(1200), w.InputTokens())
	require.Equal(t, int64(600), w.OutputTokens())
	require.Equal(t, lines, dst.String())
}

func TestTokenCaptureWriter_GeminiTokens(t *testing.T) {
	var dst bytes.Buffer
	w := newTokenCaptureWriter(&dst)

	lines := `{"type":"text","content":"review"}
{"type":"result","status":"success","stats":{"total_tokens":1500}}
`
	_, err := w.Write([]byte(lines))
	require.NoError(t, err)

	require.Equal(t, int64(0), w.InputTokens())
	require.Equal(t, int64(1500), w.OutputTokens())
}

func TestTokenCaptureWriter_NoTokens(t *testing.T) {
	var dst bytes.Buffer
	w := newTokenCaptureWriter(&dst)

	lines := `plain text output
more output
`
	_, err := w.Write([]byte(lines))
	require.NoError(t, err)

	require.Equal(t, int64(0), w.InputTokens())
	require.Equal(t, int64(0), w.OutputTokens())
}

func TestTokenCaptureWriter_PartialLines(t *testing.T) {
	var dst bytes.Buffer
	w := newTokenCaptureWriter(&dst)

	// Write partial line across two writes
	_, err := w.Write([]byte(`{"type":"turn.completed","usage":{"input_to`))
	require.NoError(t, err)
	_, err = w.Write([]byte(`kens":300,"output_tokens":150}}` + "\n"))
	require.NoError(t, err)

	require.Equal(t, int64(300), w.InputTokens())
	require.Equal(t, int64(150), w.OutputTokens())
}

func TestTokenCaptureWriter_FlushPartialLine(t *testing.T) {
	var dst bytes.Buffer
	w := newTokenCaptureWriter(&dst)

	// Write without trailing newline
	_, err := w.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50}}`))
	require.NoError(t, err)

	// Before flush, no tokens captured
	require.Equal(t, int64(0), w.InputTokens())

	w.Flush()
	require.Equal(t, int64(100), w.InputTokens())
	require.Equal(t, int64(50), w.OutputTokens())
}

func TestTokenCaptureWriter_InvalidJSON(t *testing.T) {
	var dst bytes.Buffer
	w := newTokenCaptureWriter(&dst)

	lines := `{invalid json}
{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50}}
`
	_, err := w.Write([]byte(lines))
	require.NoError(t, err)

	require.Equal(t, int64(100), w.InputTokens())
	require.Equal(t, int64(50), w.OutputTokens())
}

func TestTokenCaptureWriter_MixedCodexGemini(t *testing.T) {
	var dst bytes.Buffer
	w := newTokenCaptureWriter(&dst)

	lines := `{"type":"turn.completed","usage":{"input_tokens":500,"output_tokens":200}}
{"type":"result","stats":{"total_tokens":1000}}
`
	_, err := w.Write([]byte(lines))
	require.NoError(t, err)

	require.Equal(t, int64(500), w.InputTokens())
	require.Equal(t, int64(1200), w.OutputTokens())
}
