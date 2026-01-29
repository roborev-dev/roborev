package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// streamFormatter wraps an io.Writer to transform raw NDJSON stream output
// from Claude into compact, human-readable progress lines.
//
// In TTY mode, tool calls are shown as one-line summaries:
//
//	Read  internal/gmail/ratelimit_test.go
//	Edit  internal/gmail/ratelimit_test.go
//	Bash  go test ./internal/gmail/ -run TestRateLimiter
//
// In non-TTY mode (piped output), raw JSON is passed through unchanged.
type streamFormatter struct {
	w     io.Writer
	buf   []byte
	isTTY bool
}

func newStreamFormatter(w io.Writer, isTTY bool) *streamFormatter {
	return &streamFormatter{w: w, isTTY: isTTY}
}

func (f *streamFormatter) Write(p []byte) (int, error) {
	if !f.isTTY {
		return f.w.Write(p)
	}

	n := len(p)
	f.buf = append(f.buf, p...)

	for {
		idx := bytes.IndexByte(f.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(f.buf[:idx])
		f.buf = f.buf[idx+1:]
		f.processLine(line)
	}
	return n, nil
}

// Flush writes any remaining buffered content.
func (f *streamFormatter) Flush() {
	if len(f.buf) > 0 {
		line := string(f.buf)
		f.buf = nil
		f.processLine(line)
	}
}

// streamMessage is a minimal representation of Claude's stream-json format.
// The message.content field is an array of content blocks.
type streamMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Message *struct {
		Content json.RawMessage `json:"content,omitempty"`
	} `json:"message,omitempty"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

func (f *streamFormatter) processLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	var msg streamMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return
	}

	switch msg.Type {
	case "assistant":
		if msg.Message == nil {
			return
		}
		f.processAssistantContent(msg.Message.Content)
	case "result":
		// Suppress result events (final summary handled elsewhere)
	default:
		// Suppress system, user (tool_result), and other events
	}
}

func (f *streamFormatter) processAssistantContent(raw json.RawMessage) {
	if raw == nil {
		return
	}

	// Try as array of content blocks
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		// Try as plain string (legacy format)
		var text string
		if err := json.Unmarshal(raw, &text); err == nil && text != "" {
			fmt.Fprintln(f.w, text)
		}
		return
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			if text := strings.TrimSpace(b.Text); text != "" {
				fmt.Fprintln(f.w, text)
			}
		case "tool_use":
			f.formatToolUse(b.Name, b.Input)
		}
	}
}

func (f *streamFormatter) formatToolUse(name string, input json.RawMessage) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		fmt.Fprintf(f.w, "%-6s\n", name)
		return
	}

	switch name {
	case "Read":
		fmt.Fprintf(f.w, "%-6s %s\n", name, jsonString(fields["file_path"]))
	case "Edit", "MultiEdit":
		fmt.Fprintf(f.w, "%-6s %s\n", name, jsonString(fields["file_path"]))
	case "Write":
		fmt.Fprintf(f.w, "%-6s %s\n", name, jsonString(fields["file_path"]))
	case "Bash":
		cmd := jsonString(fields["command"])
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		fmt.Fprintf(f.w, "%-6s %s\n", name, cmd)
	case "Grep":
		pattern := jsonString(fields["pattern"])
		path := jsonString(fields["path"])
		if path != "" {
			fmt.Fprintf(f.w, "%-6s %s  %s\n", name, pattern, path)
		} else {
			fmt.Fprintf(f.w, "%-6s %s\n", name, pattern)
		}
	case "Glob":
		fmt.Fprintf(f.w, "%-6s %s\n", name, jsonString(fields["pattern"]))
	default:
		fmt.Fprintf(f.w, "%-6s\n", name)
	}
}

// jsonString extracts a string value from a raw JSON field.
func jsonString(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return strings.Trim(string(raw), `"`)
	}
	return s
}
