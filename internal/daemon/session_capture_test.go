package daemon

import (
	"bytes"
	"strings"
	"testing"
)

func TestSessionCaptureWriterStopsBufferingAfterCapture(t *testing.T) {
	var dst bytes.Buffer
	var captured []string

	w := newSessionCaptureWriter(&dst, func(sessionID string) {
		captured = append(captured, sessionID)
	})

	first := `{"type":"system","subtype":"init","session_id":"session-123"}` + "\n"
	if _, err := w.Write([]byte(first)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if got := w.SessionID(); got != "session-123" {
		t.Fatalf("SessionID() = %q, want %q", got, "session-123")
	}
	if len(captured) != 1 || captured[0] != "session-123" {
		t.Fatalf("captured = %v, want [session-123]", captured)
	}
	if w.buf.Len() != 0 {
		t.Fatalf("buffer length after capture = %d, want 0", w.buf.Len())
	}

	trailing := strings.Repeat("later output\n", 128)
	if _, err := w.Write([]byte(trailing)); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("callback count = %d, want 1", len(captured))
	}
	if w.buf.Len() != 0 {
		t.Fatalf("buffer length after trailing output = %d, want 0", w.buf.Len())
	}
	if dst.String() != first+trailing {
		t.Fatal("destination writer did not receive full stream")
	}
}

func TestSessionCaptureWriterFlushCapturesPartialLine(t *testing.T) {
	var dst bytes.Buffer
	var captured string

	w := newSessionCaptureWriter(&dst, func(sessionID string) {
		captured = sessionID
	})

	line := `{"type":"thread.started","thread_id":"thread-456"}`
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := w.SessionID(); got != "" {
		t.Fatalf("SessionID() before flush = %q, want empty", got)
	}

	w.Flush()

	if got := w.SessionID(); got != "thread-456" {
		t.Fatalf("SessionID() after flush = %q, want %q", got, "thread-456")
	}
	if captured != "thread-456" {
		t.Fatalf("captured = %q, want %q", captured, "thread-456")
	}
}
