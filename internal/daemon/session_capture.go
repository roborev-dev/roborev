package daemon

import (
	"bytes"
	"io"
	"sync"

	"github.com/roborev-dev/roborev/internal/agent"
)

type sessionCaptureWriter struct {
	dst       io.Writer
	onCapture func(string)
	mu        sync.Mutex
	buf       bytes.Buffer
	sessionID string
}

func newSessionCaptureWriter(dst io.Writer, onCapture func(string)) *sessionCaptureWriter {
	return &sessionCaptureWriter{dst: dst, onCapture: onCapture}
}

func (w *sessionCaptureWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.capture(p[:n])
	}
	return n, err
}

func (w *sessionCaptureWriter) Flush() {
	var captured string

	w.capture(nil)

	w.mu.Lock()
	if w.sessionID == "" && w.buf.Len() > 0 {
		if sessionID := agent.ExtractSessionID(w.buf.String()); sessionID != "" {
			w.sessionID = sessionID
			captured = sessionID
		}
	}
	w.mu.Unlock()

	if captured != "" && w.onCapture != nil {
		w.onCapture(captured)
	}
}

func (w *sessionCaptureWriter) SessionID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessionID
}

func (w *sessionCaptureWriter) capture(p []byte) {
	var captured string

	w.mu.Lock()
	if w.sessionID != "" {
		w.mu.Unlock()
		return
	}
	if len(p) > 0 {
		_, _ = w.buf.Write(p)
	}
	for w.sessionID == "" {
		data := w.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := string(data[:idx+1])
		w.buf.Next(idx + 1)
		if sessionID := agent.ExtractSessionID(line); sessionID != "" {
			w.sessionID = sessionID
			w.buf.Reset()
			captured = sessionID
		}
	}
	w.mu.Unlock()

	if captured != "" && w.onCapture != nil {
		w.onCapture(captured)
	}
}
