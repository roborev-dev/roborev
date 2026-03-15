package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

// tokenCaptureWriter intercepts JSONL output from agents to extract token
// usage events. All bytes pass through to the destination writer unchanged.
// Accumulated token counts are available via InputTokens/OutputTokens after
// the agent finishes.
type tokenCaptureWriter struct {
	dst          io.Writer
	mu           sync.Mutex
	buf          bytes.Buffer
	inputTokens  int64
	outputTokens int64
}

func newTokenCaptureWriter(dst io.Writer) *tokenCaptureWriter {
	return &tokenCaptureWriter{dst: dst}
}

func (w *tokenCaptureWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.scanLines(p[:n])
	}
	return n, err
}

// Flush processes any remaining partial line in the buffer.
func (w *tokenCaptureWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.parseLine(w.buf.Bytes())
		w.buf.Reset()
	}
}

// InputTokens returns the accumulated input token count.
func (w *tokenCaptureWriter) InputTokens() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inputTokens
}

// OutputTokens returns the accumulated output token count.
func (w *tokenCaptureWriter) OutputTokens() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.outputTokens
}

func (w *tokenCaptureWriter) scanLines(p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()

	_, _ = w.buf.Write(p)
	for {
		data := w.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := data[:idx]
		w.buf.Next(idx + 1)
		w.parseLine(line)
	}
}

// tokenEvent is a lightweight struct for extracting token counts from
// agent JSONL events. Covers codex (usage.input_tokens/output_tokens)
// and gemini (stats.total_tokens).
type tokenEvent struct {
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Stats struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"stats"`
}

func (w *tokenCaptureWriter) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var ev tokenEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	if ev.Usage.InputTokens > 0 {
		w.inputTokens += ev.Usage.InputTokens
	}
	if ev.Usage.OutputTokens > 0 {
		w.outputTokens += ev.Usage.OutputTokens
	}
	// Gemini emits total_tokens without input/output split
	if ev.Stats.TotalTokens > 0 {
		w.outputTokens += ev.Stats.TotalTokens
	}
}
