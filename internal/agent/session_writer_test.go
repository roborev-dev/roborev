package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionCaptureWriter_StopsBufferingAfterCapture(t *testing.T) {
	var dst bytes.Buffer
	var captured []string

	w := NewSessionCaptureWriter(&dst, func(sessionID string) {
		captured = append(captured, sessionID)
	})

	first := `{"type":"system","subtype":"init","session_id":"session-123"}` + "\n"
	_, err := w.Write([]byte(first))
	require.NoError(t, err)
	require.Equal(t, "session-123", w.SessionID())
	require.Equal(t, []string{"session-123"}, captured)
	require.Equal(t, 0, w.buf.Len())

	trailing := strings.Repeat("later output\n", 128)
	_, err = w.Write([]byte(trailing))
	require.NoError(t, err)
	require.Len(t, captured, 1, "callback fires only once")
	require.Equal(t, 0, w.buf.Len(), "buffer is not retained after capture")
	require.Equal(t, first+trailing, dst.String(), "destination receives full stream")
}

func TestSessionCaptureWriter_FlushCapturesPartialLine(t *testing.T) {
	var dst bytes.Buffer
	var captured string

	w := NewSessionCaptureWriter(&dst, func(sessionID string) {
		captured = sessionID
	})

	line := `{"type":"thread.started","thread_id":"thread-456"}`
	_, err := w.Write([]byte(line))
	require.NoError(t, err)
	require.Empty(t, w.SessionID(), "no newline yet, no capture")

	w.Flush()

	require.Equal(t, "thread-456", w.SessionID())
	require.Equal(t, "thread-456", captured)
}

func TestSessionCaptureWriter_NoSessionEvent(t *testing.T) {
	var dst bytes.Buffer
	w := NewSessionCaptureWriter(&dst, nil)

	payload := "plain text output\nwith multiple lines\nbut no session\n"
	_, err := w.Write([]byte(payload))
	require.NoError(t, err)
	w.Flush()

	require.Empty(t, w.SessionID())
	require.Equal(t, payload, dst.String())
}
