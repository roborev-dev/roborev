package daemon

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionCaptureWriterStopsBufferingAfterCapture(t *testing.T) {
	var dst bytes.Buffer
	var captured []string

	w := newSessionCaptureWriter(&dst, func(sessionID string) {
		captured = append(captured, sessionID)
	})

	first := `{"type":"system","subtype":"init","session_id":"session-123"}` + "\n"
	_, err := w.Write([]byte(first))
	require.NoError(t, err, "first write")
	require.Equal(t, "session-123", w.SessionID(),
		"SessionID() = %q, want %q", w.SessionID(), "session-123")
	require.Equal(t, []string{"session-123"}, captured,
		"captured = %v, want [session-123]", captured)
	require.Equal(t, 0, w.buf.Len(),
		"buffer length after capture = %d, want 0", w.buf.Len())

	trailing := strings.Repeat("later output\n", 128)
	_, err = w.Write([]byte(trailing))
	require.NoError(t, err, "second write")
	require.Len(t, captured, 1,
		"callback count = %d, want 1", len(captured))
	require.Equal(t, 0, w.buf.Len(),
		"buffer length after trailing output = %d, want 0", w.buf.Len())
	require.Equal(t, first+trailing, dst.String(),
		"destination writer did not receive full stream")
}

func TestSessionCaptureWriterFlushCapturesPartialLine(t *testing.T) {
	var dst bytes.Buffer
	var captured string

	w := newSessionCaptureWriter(&dst, func(sessionID string) {
		captured = sessionID
	})

	line := `{"type":"thread.started","thread_id":"thread-456"}`
	_, err := w.Write([]byte(line))
	require.NoError(t, err, "write")
	require.Empty(t, w.SessionID(),
		"SessionID() before flush = %q, want empty", w.SessionID())

	w.Flush()

	require.Equal(t, "thread-456", w.SessionID(),
		"SessionID() after flush = %q, want %q", w.SessionID(), "thread-456")
	require.Equal(t, "thread-456", captured,
		"captured = %q, want %q", captured, "thread-456")
}
