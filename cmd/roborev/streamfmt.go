package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// Styles for TTY-mode stream output.
var (
	sfToolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{
			Light: "30", Dark: "51",
		}) // Cyan — matches tuiAddressedStyle
	sfArgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{
			Light: "242", Dark: "246",
		}) // Gray — de-emphasize detail
	sfGutterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{
			Light: "242", Dark: "240",
		}) // Dim — subtle visual grouping
	sfReasoningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{
			Light: "242", Dark: "243",
		}).Italic(true) // Dim italic — thinking indicator
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
	width int // terminal width; 0 = no wrapping

	glamourStyle gansi.StyleConfig // detected once at init

	writeErr    error // first write error encountered during formatting
	lastWasTool bool  // tracks tool vs text transitions for spacing
	hasOutput   bool  // whether any output has been written

	// Tracks opencode tool call IDs that have already been rendered.
	opencodeRenderedToolIDs map[string]struct{}

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
	f := &streamFormatter{w: w, isTTY: isTTY}
	if isTTY {
		f.glamourStyle = sfGlamourStyle()
		f.width = sfTerminalWidth(w)
	}
	return f
}

// newStreamFormatterWithWidth creates a stream formatter with an
// explicit width and pre-computed glamour style. Used when rendering
// into a buffer (e.g. the TUI log view) where terminal queries
// aren't possible.
func newStreamFormatterWithWidth(
	w io.Writer, width int, style gansi.StyleConfig,
) *streamFormatter {
	return &streamFormatter{
		w: w, isTTY: true, width: width, glamourStyle: style,
	}
}

// sfTerminalWidth returns the terminal width for the given writer,
// defaulting to 100 if detection fails.
func sfTerminalWidth(w io.Writer) int {
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return 100
}

// sfGlamourStyle returns a glamour style config with zero margins,
// matching the TUI's rendering. Detects dark/light background once.
func sfGlamourStyle() gansi.StyleConfig {
	style := styles.LightStyleConfig
	if termenv.HasDarkBackground() {
		style = styles.DarkStyleConfig
	}
	zeroMargin := uint(0)
	style.Document.Margin = &zeroMargin
	style.CodeBlock.Margin = &zeroMargin
	style.Code.Prefix = ""
	style.Code.Suffix = ""
	return style
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
	// OpenCode: nested part payload
	Part json.RawMessage `json:"part,omitempty"`
}

// codexItem represents the item field in codex JSONL events.
type codexItem struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type,omitempty"`
	Text    string `json:"text,omitempty"`
	Command string `json:"command,omitempty"`
}

// opencodeToolPart represents the part payload for opencode tool events.
type opencodeToolPart struct {
	Type  string `json:"type"`
	Tool  string `json:"tool"`
	ID    string `json:"id,omitempty"`
	State struct {
		Status string                     `json:"status,omitempty"`
		Input  map[string]json.RawMessage `json:"input,omitempty"`
	} `json:"state"`
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
			if json.Unmarshal(ev.Content, &text) == nil {
				f.writeText(text)
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
	case "text", "reasoning", "tool":
		// OpenCode format: event body nested under "part"
		if ev.Part != nil {
			f.processOpenCodePart(ev.Type, ev.Part)
		}
	case "step_start", "step_finish":
		// OpenCode lifecycle events — suppress
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
	case "reasoning":
		if eventType != "item.completed" {
			return
		}
		text := strings.TrimSpace(sanitizeControl(item.Text))
		if text != "" {
			f.writeReasoning(text)
		}
	case "agent_message":
		if eventType != "item.completed" {
			return
		}
		f.writeText(sanitizeControlKeepNewlines(item.Text))
	case "command_execution":
		cmd := strings.TrimSpace(sanitizeControl(item.Command))
		if !f.shouldRenderCodexCommand(eventType, item, cmd) {
			return
		}
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		f.writeTool("Bash", cmd)
	case "file_change":
		if eventType != "item.completed" {
			return
		}
		f.writeTool("Edit", "")
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

func (f *streamFormatter) processOpenCodePart(
	eventType string, raw json.RawMessage,
) {
	switch eventType {
	case "text":
		var part struct{ Text string }
		if json.Unmarshal(raw, &part) == nil && part.Text != "" {
			f.writeText(sanitizeControlKeepNewlines(part.Text))
		}
	case "reasoning":
		var part struct{ Text string }
		if json.Unmarshal(raw, &part) == nil {
			text := strings.TrimSpace(sanitizeControl(part.Text))
			if text != "" {
				f.writeReasoning(text)
			}
		}
	case "tool":
		var tp opencodeToolPart
		if json.Unmarshal(raw, &tp) != nil || tp.Tool == "" {
			return
		}
		// Only render on "running" or "completed" status to
		// skip the initial "pending" event that has no details.
		status := tp.State.Status
		if status != "running" && status != "completed" {
			return
		}
		// Deduplicate by tool call ID.
		if tp.ID != "" {
			if f.opencodeRenderedToolIDs == nil {
				f.opencodeRenderedToolIDs = make(
					map[string]struct{},
				)
			}
			if _, seen := f.opencodeRenderedToolIDs[tp.ID]; seen {
				return
			}
			f.opencodeRenderedToolIDs[tp.ID] = struct{}{}
		}
		f.formatToolUse(tp.Tool, f.opencodeToolInput(tp))
	}
}

// opencodeToolInput returns the raw JSON input map from an opencode
// tool part, suitable for passing to formatToolUse.
func (f *streamFormatter) opencodeToolInput(
	tp opencodeToolPart,
) json.RawMessage {
	if len(tp.State.Input) == 0 {
		return nil
	}
	b, err := json.Marshal(tp.State.Input)
	if err != nil {
		return nil
	}
	return b
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
		if err := json.Unmarshal(raw, &text); err == nil {
			f.writeText(text)
		}
		return
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			f.writeText(b.Text)
		case "tool_use":
			f.formatToolUse(b.Name, b.Input)
		}
	}
}

func (f *streamFormatter) formatToolUse(name string, input json.RawMessage) {
	name = sanitizeControl(name)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		f.writeTool(name, "")
		return
	}

	switch name {
	case "Read":
		f.writeTool(name, jsonString(fields["file_path"]))
	case "Edit", "MultiEdit":
		f.writeTool(name, jsonString(fields["file_path"]))
	case "Write":
		f.writeTool(name, jsonString(fields["file_path"]))
	case "Bash":
		cmd := jsonString(fields["command"])
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		f.writeTool(name, cmd)
	case "Grep":
		pattern := jsonString(fields["pattern"])
		path := jsonString(fields["path"])
		if path != "" {
			f.writeTool(name, pattern+"  "+path)
		} else {
			f.writeTool(name, pattern)
		}
	case "Glob":
		f.writeTool(name, jsonString(fields["pattern"]))
	default:
		f.writeTool(name, "")
	}
}

// writef writes formatted output, capturing the first error.
func (f *streamFormatter) writef(format string, args ...any) {
	if f.writeErr != nil || f.w == nil {
		return
	}
	_, f.writeErr = fmt.Fprintf(f.w, format, args...)
}

// writeText writes agent text, rendering markdown and wrapping to
// terminal width when in TTY mode with a known width.
func (f *streamFormatter) writeText(text string) {
	text = sanitizeControlKeepNewlines(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if f.lastWasTool && f.hasOutput {
		f.writef("\n")
	}
	f.lastWasTool = false
	f.hasOutput = true
	if f.width <= 0 {
		f.writef("%s\n", text)
		return
	}
	lines := renderMarkdownLines(
		text, f.width, f.width, f.glamourStyle, 2,
	)
	for _, line := range lines {
		f.writef("%s\n", line)
	}
}

// writeReasoning writes a dimmed reasoning summary line.
func (f *streamFormatter) writeReasoning(text string) {
	text = sanitizeControlKeepNewlines(text)
	if f.lastWasTool && f.hasOutput {
		f.writef("\n")
	}
	f.lastWasTool = false
	f.hasOutput = true
	f.writef("%s\n", sfReasoningStyle.Render(text))
}

// writeTool writes a styled tool-call line with a gutter prefix
// for visual grouping:
//
//	│ Read   internal/daemon/worker.go
//	│ Edit   internal/daemon/worker.go
func (f *streamFormatter) writeTool(name, arg string) {
	name = sanitizeControl(name)
	arg = sanitizeControl(arg)
	if !f.lastWasTool && f.hasOutput {
		f.writef("\n")
	}
	f.lastWasTool = true
	f.hasOutput = true
	gutter := sfGutterStyle.Render(" │")
	styled := fmt.Sprintf(
		"%s %s %s",
		gutter,
		sfToolStyle.Render(fmt.Sprintf("%-6s", name)),
		sfArgStyle.Render(arg),
	)
	f.writef("%s\n", styled)
}

// writerIsTerminal checks if a writer is backed by a terminal.
func writerIsTerminal(w io.Writer) bool {
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		return isTerminal(f.Fd())
	}
	return false
}

// printMarkdownOrPlain renders text as glamour-styled markdown when
// writing to a TTY, or prints it as-is otherwise.
func printMarkdownOrPlain(w io.Writer, text string) {
	if !writerIsTerminal(w) {
		fmt.Fprintln(w, text)
		return
	}
	width := sfTerminalWidth(w)
	style := sfGlamourStyle()
	lines := renderMarkdownLines(text, width, width, style, 2)
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

// sanitizeControl strips ANSI escape sequences and non-printable control
// characters from s. Newlines are replaced with spaces to produce
// single-line output (used for command text summaries).
func sanitizeControl(s string) string {
	return sanitizeControlChars(s, false)
}

// sanitizeControlKeepNewlines strips ANSI escape sequences and
// non-printable control characters but preserves newlines. Used for
// agent text content that needs to retain paragraph structure.
func sanitizeControlKeepNewlines(s string) string {
	return sanitizeControlChars(s, true)
}

func sanitizeControlChars(s string, keepNewlines bool) string {
	s = ansiEscapePattern.ReplaceAllString(s, "")
	if keepNewlines {
		// Normalize line endings but preserve them.
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	} else {
		s = strings.ReplaceAll(s, "\r\n", " ")
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, "\r", " ")
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r == '\n' || !unicode.IsControl(r) {
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
