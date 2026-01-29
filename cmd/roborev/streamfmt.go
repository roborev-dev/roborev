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
	w        io.Writer
	buf      []byte
	isTTY    bool
	writeErr error // first write error encountered during formatting
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
	if f.writeErr != nil {
		return n, f.writeErr
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

// streamEvent is a unified representation of stream-json events from
// both Claude Code and Gemini CLI.
//
// Claude:  {"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{...}}]}}
// Gemini:  {"type":"tool_use","tool_name":"read_file","parameters":{"file_path":"..."}}
//          {"type":"message","role":"assistant","content":"...","delta":true}
type streamEvent struct {
	Type string `json:"type"`
	// Claude: nested message with content blocks
	Message *struct {
		Content json.RawMessage `json:"content,omitempty"`
	} `json:"message,omitempty"`
	// Gemini: top-level fields
	Role       string          `json:"role,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// geminiToolNames maps Gemini tool names to display names.
var geminiToolNames = map[string]string{
	"read_file":         "Read",
	"replace":           "Edit",
	"write_file":        "Write",
	"run_shell_command": "Bash",
	"grep":              "Grep",
	"glob":              "Glob",
	"list_dir":          "Glob",
}

func (f *streamFormatter) processLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}

	switch ev.Type {
	case "assistant":
		// Claude format
		if ev.Message != nil {
			f.processAssistantContent(ev.Message.Content)
		}
	case "message":
		// Gemini format: assistant text
		if ev.Role == "assistant" {
			var text string
			if json.Unmarshal(ev.Content, &text) == nil && strings.TrimSpace(text) != "" {
				f.writef("%s\n", strings.TrimSpace(text))
			}
		}
	case "tool_use":
		// Gemini format: top-level tool use
		if ev.ToolName != "" {
			displayName := geminiToolNames[ev.ToolName]
			if displayName == "" {
				displayName = ev.ToolName
			}
			f.formatToolUse(displayName, ev.Parameters)
		}
	case "result", "tool_result", "init":
		// Suppress
	default:
		// Suppress system, user, and other events
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
			f.writef("%s\n", text)
		}
		return
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			if text := strings.TrimSpace(b.Text); text != "" {
				f.writef("%s\n", text)
			}
		case "tool_use":
			f.formatToolUse(b.Name, b.Input)
		}
	}
}

func (f *streamFormatter) formatToolUse(name string, input json.RawMessage) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		f.writef("%-6s\n", name)
		return
	}

	switch name {
	case "Read":
		f.writef("%-6s %s\n", name, jsonString(fields["file_path"]))
	case "Edit", "MultiEdit":
		f.writef("%-6s %s\n", name, jsonString(fields["file_path"]))
	case "Write":
		f.writef("%-6s %s\n", name, jsonString(fields["file_path"]))
	case "Bash":
		cmd := jsonString(fields["command"])
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		f.writef("%-6s %s\n", name, cmd)
	case "Grep":
		pattern := jsonString(fields["pattern"])
		path := jsonString(fields["path"])
		if path != "" {
			f.writef("%-6s %s  %s\n", name, pattern, path)
		} else {
			f.writef("%-6s %s\n", name, pattern)
		}
	case "Glob":
		f.writef("%-6s %s\n", name, jsonString(fields["pattern"]))
	default:
		f.writef("%-6s\n", name)
	}
}

// writef writes formatted output, capturing the first error.
func (f *streamFormatter) writef(format string, args ...interface{}) {
	if f.writeErr != nil {
		return
	}
	_, f.writeErr = fmt.Fprintf(f.w, format, args...)
}

// writerIsTerminal checks if a writer is backed by a terminal.
func writerIsTerminal(w io.Writer) bool {
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		return isTerminal(f.Fd())
	}
	return false
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
