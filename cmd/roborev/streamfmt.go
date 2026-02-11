package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
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

	writeErr error // first write error encountered during formatting

	// Tracks codex command_execution items that have already been rendered.
	codexRenderedCommandIDs map[string]struct{}
	// Track started command text to suppress duplicate completed echoes, including mixed-ID pairs.
	codexStartedCommands map[string]int
	// Track started command text by ID so completed events missing command can clear started state.
	codexStartedCommandsByID map[string]string
	// Track started IDs per command in FIFO order for deterministic pairing.
	codexStartedIDsByCommand map[string][]string
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
// Claude Code, Gemini CLI, and Codex CLI.
//
// Claude:  {"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{...}}]}}
// Gemini:  {"type":"tool_use","tool_name":"read_file","parameters":{"file_path":"..."}}
//
//	{"type":"message","role":"assistant","content":"...","delta":true}
//
// Codex:   {"type":"item.completed","item":{"type":"agent_message","text":"..."}}
//
//	{"type":"item.started","item":{"type":"command_execution","command":"bash -lc ls"}}
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
	// Codex: item events
	Item *codexItem `json:"item,omitempty"`
}

// codexItem represents the item field in codex JSONL events.
type codexItem struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type,omitempty"`
	Text    string `json:"text,omitempty"`
	Command string `json:"command,omitempty"`
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
	case "item.started", "item.completed", "item.updated":
		// Codex format: item events
		f.processCodexItem(ev.Type, ev.Item)
	case "result", "tool_result", "init",
		"thread.started", "turn.started", "turn.completed":
		// Suppress lifecycle events
	default:
		// Suppress system, user, and other events
	}
}

func (f *streamFormatter) processCodexItem(eventType string, item *codexItem) {
	if item == nil {
		return
	}
	switch item.Type {
	case "agent_message":
		if eventType != "item.completed" {
			return
		}
		if text := strings.TrimSpace(sanitizeControl(item.Text)); text != "" {
			f.writef("%s\n", text)
		}
	case "command_execution":
		cmd := strings.TrimSpace(sanitizeControl(item.Command))
		if !f.shouldRenderCodexCommand(eventType, item, cmd) {
			return
		}
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		f.writef("%-6s %s\n", "Bash", cmd)
	case "file_change":
		if eventType != "item.completed" {
			return
		}
		f.writef("%-6s\n", "Edit")
	}
}

func (f *streamFormatter) shouldRenderCodexCommand(eventType string, item *codexItem, cmd string) bool {
	if eventType != "item.started" && eventType != "item.completed" {
		return false
	}
	id := strings.TrimSpace(item.ID)
	if eventType == "item.started" {
		if cmd == "" {
			return false
		}
		if id != "" {
			if f.codexRenderedCommandIDs == nil {
				f.codexRenderedCommandIDs = make(map[string]struct{})
			}
			if _, seen := f.codexRenderedCommandIDs[id]; seen {
				return false
			}
			f.codexRenderedCommandIDs[id] = struct{}{}
			if f.codexStartedCommandsByID == nil {
				f.codexStartedCommandsByID = make(map[string]string)
			}
			f.codexStartedCommandsByID[id] = cmd
			if f.codexStartedIDsByCommand == nil {
				f.codexStartedIDsByCommand = make(map[string][]string)
			}
			f.codexStartedIDsByCommand[cmd] = append(f.codexStartedIDsByCommand[cmd], id)
		}
		if f.codexStartedCommands == nil {
			f.codexStartedCommands = make(map[string]int)
		}
		f.codexStartedCommands[cmd]++
		return true
	}

	if cmd == "" {
		if id != "" {
			if startedCmd, ok := f.codexStartedCommandsByID[id]; ok {
				f.decrementCodexStartedCommand(startedCmd)
				f.removeCodexStartedIDFromQueue(startedCmd, id)
				delete(f.codexStartedCommandsByID, id)
			}
		}
		return false
	}

	if id != "" {
		if startedCmd, ok := f.codexStartedCommandsByID[id]; ok {
			f.decrementCodexStartedCommand(startedCmd)
			f.removeCodexStartedIDFromQueue(startedCmd, id)
			delete(f.codexStartedCommandsByID, id)
			if startedCmd == cmd {
				if f.codexRenderedCommandIDs == nil {
					f.codexRenderedCommandIDs = make(map[string]struct{})
				}
				f.codexRenderedCommandIDs[id] = struct{}{}
				return false
			}
		}
	}

	// Completed events should be suppressed when we've already rendered the paired
	// started event for the same command text, even if ID presence changed.
	if count := f.codexStartedCommands[cmd]; count > 0 {
		f.decrementCodexStartedCommand(cmd)
		if id == "" {
			// Keep ID->command tracking in sync when a completion is matched by command only.
			f.consumeCodexStartedCommandIDForCommand(cmd)
		}
		if id != "" {
			if f.codexRenderedCommandIDs == nil {
				f.codexRenderedCommandIDs = make(map[string]struct{})
			}
			f.codexRenderedCommandIDs[id] = struct{}{}
		}
		return false
	}

	if id != "" {
		if f.codexRenderedCommandIDs == nil {
			f.codexRenderedCommandIDs = make(map[string]struct{})
		}
		if _, seen := f.codexRenderedCommandIDs[id]; seen {
			return false
		}
		f.codexRenderedCommandIDs[id] = struct{}{}
		return true
	}

	return true
}

func (f *streamFormatter) consumeCodexStartedCommandIDForCommand(cmd string) {
	if cmd == "" {
		return
	}
	ids := f.codexStartedIDsByCommand[cmd]
	if len(ids) == 0 {
		return
	}
	// Pop the oldest ID (FIFO) for deterministic pairing.
	consumed := ids[0]
	if len(ids) == 1 {
		delete(f.codexStartedIDsByCommand, cmd)
	} else {
		f.codexStartedIDsByCommand[cmd] = ids[1:]
	}
	delete(f.codexStartedCommandsByID, consumed)
}

// removeCodexStartedIDFromQueue removes a specific ID from the per-command FIFO.
func (f *streamFormatter) removeCodexStartedIDFromQueue(cmd, id string) {
	ids := f.codexStartedIDsByCommand[cmd]
	for i, v := range ids {
		if v == id {
			f.codexStartedIDsByCommand[cmd] = append(ids[:i], ids[i+1:]...)
			if len(f.codexStartedIDsByCommand[cmd]) == 0 {
				delete(f.codexStartedIDsByCommand, cmd)
			}
			return
		}
	}
}

func (f *streamFormatter) decrementCodexStartedCommand(cmd string) {
	if cmd == "" {
		return
	}
	count := f.codexStartedCommands[cmd]
	if count <= 0 {
		return
	}
	if count == 1 {
		delete(f.codexStartedCommands, cmd)
		return
	}
	f.codexStartedCommands[cmd] = count - 1
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

// sanitizeControl strips ANSI escape sequences and non-printable control
// characters from s. This prevents terminal injection from untrusted
// model/subprocess output embedded in codex JSONL fields.
func sanitizeControl(s string) string {
	s = ansiEscapePattern.ReplaceAllString(s, "")
	// Replace newlines/carriage returns with spaces to avoid collapsing words.
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
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
