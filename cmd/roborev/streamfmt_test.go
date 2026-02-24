package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// streamFormatterFixture wraps the buffer and formatter setup common to most tests.
type streamFormatterFixture struct {
	buf bytes.Buffer
	f   *streamFormatter
}

func newFixture(tty bool) *streamFormatterFixture {
	fix := &streamFormatterFixture{}
	fix.f = newStreamFormatter(&fix.buf, tty)
	return fix
}

func (fix *streamFormatterFixture) writeLine(s string) {
	_, _ = fix.f.Write([]byte(s + "\n"))
}

func (fix *streamFormatterFixture) output() string {
	// Strip ANSI escape sequences so assertions test logical content
	// regardless of TTY styling.
	return ansiEscapePattern.ReplaceAllString(fix.buf.String(), "")
}

func (fix *streamFormatterFixture) assertContains(t *testing.T, substr string) {
	t.Helper()
	if !strings.Contains(fix.output(), substr) {
		t.Errorf("expected output to contain %q, got:\n%s", substr, fix.output())
	}
}

func (fix *streamFormatterFixture) assertNotContains(t *testing.T, substr string) {
	t.Helper()
	if strings.Contains(fix.output(), substr) {
		t.Errorf("expected output NOT to contain %q, got:\n%s", substr, fix.output())
	}
}

func (fix *streamFormatterFixture) assertEmpty(t *testing.T) {
	t.Helper()
	if fix.output() != "" {
		t.Errorf("expected empty output, got:\n%s", fix.output())
	}
}

func (fix *streamFormatterFixture) assertCount(t *testing.T, substr string, want int) {
	t.Helper()
	got := strings.Count(fix.output(), substr)
	if got != want {
		t.Errorf("expected output to contain %q %d time(s), got %d:\n%s", substr, want, got, fix.output())
	}
}

func toJson(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("toJson: %v", err))
	}
	return string(b)
}

// Event builders for Anthropic-style JSON.

func eventAssistantToolUse(toolName string, input map[string]any) string {
	return toJson(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"name":  toolName,
					"input": input,
				},
			},
		},
	})
}

func eventAssistantText(text string) string {
	return toJson(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": text},
			},
		},
	})
}

func eventAssistantMulti(blocks ...map[string]any) string {
	return toJson(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": blocks,
		},
	})
}

func contentBlockText(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func contentBlockToolUse(toolName string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "name": toolName, "input": input}
}

func eventAssistantLegacy(content string) string {
	return toJson(map[string]any{
		"type":    "assistant",
		"message": map[string]any{"content": content},
	})
}

// Event builders for Gemini-style JSON.

func eventGeminiToolUse(toolName, toolID string, params map[string]any) string {
	return toJson(map[string]any{
		"type":       "tool_use",
		"tool_name":  toolName,
		"tool_id":    toolID,
		"parameters": params,
	})
}

func TestStreamFormatter_ToolUse(t *testing.T) {
	fix := newFixture(true)

	fix.writeLine(eventAssistantToolUse("Read", map[string]any{"file_path": "internal/gmail/ratelimit.go"}))
	fix.writeLine(`{"type":"user","tool_use_result":{"filePath":"internal/gmail/ratelimit.go"}}`)
	fix.writeLine(eventAssistantToolUse("Edit", map[string]any{"file_path": "internal/gmail/ratelimit.go", "old_string": "foo", "new_string": "bar"}))
	fix.writeLine(eventAssistantToolUse("Bash", map[string]any{"command": "go test ./internal/gmail/ -run TestRateLimiter"}))

	fix.assertContains(t, "Read   internal/gmail/ratelimit.go")
	fix.assertContains(t, "Edit   internal/gmail/ratelimit.go")
	fix.assertContains(t, "Bash   go test ./internal/gmail/ -run TestRateLimiter")
	// tool_use_result (user type) should be suppressed
	fix.assertNotContains(t, "tool_use_result")
	fix.assertNotContains(t, "filePath")
}

func TestStreamFormatter_TextOutput(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventAssistantText("I'll fix this now."))
	fix.assertContains(t, "I'll fix this now.")
}

func TestStreamFormatter_NonTTY(t *testing.T) {
	fix := newFixture(false)
	raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`
	fix.writeLine(raw)
	// Non-TTY should pass through raw JSON
	if fix.output() != raw+"\n" {
		t.Errorf("non-TTY should pass through raw, got:\n%s", fix.output())
	}
}

func TestStreamFormatter_ResultSuppressed(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(`{"type":"result","result":"final summary text"}`)
	fix.assertEmpty(t)
}

func TestStreamFormatter_BashTruncation(t *testing.T) {
	fix := newFixture(true)
	longCmd := strings.Repeat("x", 100)
	fix.writeLine(eventAssistantToolUse("Bash", map[string]any{"command": longCmd}))

	got := fix.output()
	if len(got) > 100 {
		// Should be truncated to ~80 chars + "Bash   " prefix + "..." + newline
		if !strings.Contains(got, "...") {
			t.Errorf("long bash command should be truncated with ..., got:\n%s", got)
		}
	}
}

func TestStreamFormatter_GrepWithPath(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventAssistantToolUse("Grep", map[string]any{"pattern": "TODO", "path": "internal/"}))
	fix.assertContains(t, "Grep   TODO  internal/")
}

func TestStreamFormatter_LegacyStringContent(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventAssistantLegacy("legacy string content"))
	fix.assertContains(t, "legacy string content")
}

func TestStreamFormatter_MultipleContentBlocks(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventAssistantMulti(
		contentBlockText("thinking..."),
		contentBlockToolUse("Read", map[string]any{"file_path": "main.go"}),
	))
	fix.assertContains(t, "thinking...")
	fix.assertContains(t, "Read   main.go")
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestStreamFormatter_WriteError(t *testing.T) {
	f := newStreamFormatter(errWriter{}, true)

	line := eventAssistantText("hello") + "\n"
	_, err := f.Write([]byte(line))
	if err == nil {
		t.Fatal("expected write error to propagate")
	}
	if err != io.ErrClosedPipe {
		t.Fatalf("expected ErrClosedPipe, got %v", err)
	}
}

func TestStreamFormatter_GeminiToolUse(t *testing.T) {
	fix := newFixture(true)

	lines := []string{
		`{"type":"init","session_id":"abc"}`,
		`{"type":"message","role":"user","content":"fix this"}`,
		`{"type":"message","role":"assistant","content":"I'll fix this.","delta":true}`,
		eventGeminiToolUse("read_file", "t1", map[string]any{"file_path": "main.go"}),
		`{"type":"tool_result","tool_id":"t1","status":"success"}`,
		eventGeminiToolUse("replace", "t2", map[string]any{"file_path": "main.go", "old_string": "foo", "new_string": "bar"}),
		eventGeminiToolUse("run_shell_command", "t3", map[string]any{"command": "go test ./..."}),
		`{"type":"result","status":"success"}`,
	}

	for _, line := range lines {
		fix.writeLine(line)
	}

	fix.assertContains(t, "I'll fix this.")
	fix.assertContains(t, "Read   main.go")
	fix.assertContains(t, "Edit   main.go")
	fix.assertContains(t, "Bash   go test ./...")
	// init, user message, tool_result, and result should be suppressed
	fix.assertNotContains(t, "session_id")
	fix.assertNotContains(t, "tool_id")
	fix.assertNotContains(t, "status")
}

func TestStreamFormatter_GeminiToolResult_Suppressed(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(`{"type":"tool_result","tool_id":"t1","status":"success","output":"file contents here"}`)
	fix.assertEmpty(t)
}

func TestStreamFormatter_Codex_Scenarios(t *testing.T) {
	longCmd := "bash -lc " + strings.Repeat("x", 100)
	tests := []struct {
		name        string
		events      []string
		contains    []string
		notContains []string
		counts      map[string]int
		empty       bool
		checkOutput func(*testing.T, string)
	}{
		{
			name: "Codex Events Lifecycle",
			events: []string{
				`{"type":"thread.started","thread_id":"abc123"}`,
				`{"type":"turn.started"}`,
				`{"type":"item.started","item":{"type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.updated","item":{"type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls","exit_code":0}}`,
				`{"type":"item.updated","item":{"type":"file_change","changes":[{"path":"main.go","kind":"update"}]}}`,
				`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"main.go","kind":"update"}]}}`,
				`{"type":"item.updated","item":{"type":"agent_message","text":"draft"}}`,
				`{"type":"item.completed","item":{"type":"agent_message","text":"I fixed the issue."}}`,
				`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50}}`,
			},
			contains: []string{
				"Bash   bash -lc ls",
				"Edit",
				"I fixed the issue.",
			},
			counts: map[string]int{
				"Bash   bash -lc ls": 1,
				"Edit":               1,
			},
			notContains: []string{
				"thread_id",
				"turn.started",
				"input_tokens",
				"draft",
			},
		},
		{
			name: "Codex Updated Suppressed",
			events: []string{
				`{"type":"item.updated","item":{"type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.updated","item":{"type":"file_change","changes":[{"path":"main.go","kind":"update"}]}}`,
				`{"type":"item.updated","item":{"type":"agent_message","text":"still drafting"}}`,
			},
			empty: true,
		},
		{
			name: "Codex Sanitizes Control Chars",
			events: []string{
				`{"type":"item.completed","item":{"type":"agent_message","text":"\u001b[31mred\u001b[0m and \u0007bell"}}`,
				`{"type":"item.started","item":{"type":"command_execution","command":"bash -lc \u001b[32mls\u001b[0m"}}`,
			},
			contains: []string{
				"red and bell",
				"Bash   bash -lc ls",
			},
			notContains: []string{
				"\x1b",
				"\x07",
			},
		},
		{
			name: "Codex Lifecycle Suppressed",
			events: []string{
				`{"type":"thread.started","thread_id":"abc"}`,
				`{"type":"turn.started"}`,
				`{"type":"turn.completed","usage":{"input_tokens":100}}`,
			},
			empty: true,
		},
		{
			name: "Codex Command Truncation",
			events: []string{
				fmt.Sprintf(`{"type":"item.started","item":{"type":"command_execution","command":%q}}`, longCmd),
			},
			checkOutput: func(t *testing.T, output string) {
				if !strings.Contains(output, "...") {
					t.Errorf("long codex command should be truncated with ..., got:\n%s", output)
				}
			},
		},
		{
			name: "Codex Command Completed Fallback",
			events: []string{
				`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution"}}`,
				`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.completed","item":{"id":"cmd_2","type":"command_execution","command":"bash -lc pwd"}}`,
			},
			contains: []string{
				"Bash   bash -lc ls",
				"Bash   bash -lc pwd",
			},
			counts: map[string]int{
				"Bash   bash -lc ls":  1,
				"Bash   bash -lc pwd": 1,
			},
		},
		{
			name: "Codex Command Mixed ID Started Without ID Completed With ID",
			events: []string{
				`{"type":"item.started","item":{"type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
			},
			contains: []string{"Bash   bash -lc ls"},
			counts:   map[string]int{"Bash   bash -lc ls": 1},
		},
		{
			name: "Codex Command Mixed ID Started With ID Completed Without ID",
			events: []string{
				`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
			},
			contains: []string{"Bash   bash -lc ls"},
			counts:   map[string]int{"Bash   bash -lc ls": 1},
		},
		{
			name: "Codex Command Started With ID Completed Without Command Clears Counter",
			events: []string{
				`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution"}}`,
				`{"type":"item.completed","item":{"id":"cmd_2","type":"command_execution","command":"bash -lc ls"}}`,
			},
			contains: []string{"Bash   bash -lc ls"},
			counts:   map[string]int{"Bash   bash -lc ls": 2},
		},
		{
			name: "Codex Command Mixed ID Fallback Does Not Leave Stale ID",
			events: []string{
				`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.started","item":{"id":"cmd_2","type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution"}}`,
				`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
			},
			contains: []string{"Bash   bash -lc ls"},
			counts:   map[string]int{"Bash   bash -lc ls": 2},
		},
		{
			name: "Codex Reasoning Displayed",
			events: []string{
				`{"type":"item.completed","item":{"type":"reasoning","text":"**Reviewing error handling changes**"}}`,
			},
			contains: []string{"Reviewing error handling changes"},
		},
		{
			name: "Codex Reasoning Suppressed On Non-Completed",
			events: []string{
				`{"type":"item.started","item":{"type":"reasoning","text":"draft thinking"}}`,
				`{"type":"item.updated","item":{"type":"reasoning","text":"still thinking"}}`,
			},
			empty: true,
		},
		{
			name: "Codex Multi ID Same Command Deterministic Pairing",
			events: []string{
				`{"type":"item.started","item":{"id":"cmd_A","type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.started","item":{"id":"cmd_B","type":"command_execution","command":"bash -lc ls"}}`,
				// Command-only completion consumes cmd_A (FIFO), counter 2→1.
				`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
				// ID-only completion for cmd_B clears remaining state, counter 1→0.
				`{"type":"item.completed","item":{"id":"cmd_B","type":"command_execution"}}`,
				// With all started state drained, this completion must render.
				`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
			},
			counts: map[string]int{"Bash   bash -lc ls": 3},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fix := newFixture(true)
			for _, event := range tc.events {
				fix.writeLine(event)
			}

			if tc.empty {
				fix.assertEmpty(t)
			}
			for _, s := range tc.contains {
				fix.assertContains(t, s)
			}
			for _, s := range tc.notContains {
				fix.assertNotContains(t, s)
			}
			for s, count := range tc.counts {
				fix.assertCount(t, s, count)
			}
			if tc.checkOutput != nil {
				tc.checkOutput(t, fix.output())
			}
		})
	}
}

func TestSanitizeControl(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "hello world", "hello world"},
		{"newlines collapsed", "line1\nline2\r\nline3", "line1 line2 line3"},
		{"ansi stripped", "\x1b[31mred\x1b[0m text", "red text"},
		{"control chars removed", "bell\x07here", "bellhere"},
		{"tabs kept", "col1\tcol2", "col1\tcol2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeControl(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeControl(%q) = %q, want %q",
					tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeControlKeepNewlines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "hello", "hello"},
		{"newlines preserved", "line1\nline2", "line1\nline2"},
		{"crlf normalized", "line1\r\nline2\rline3", "line1\nline2\nline3"},
		{"ansi stripped", "\x1b[32mgreen\x1b[0m", "green"},
		{"control chars removed", "bell\x07here", "bellhere"},
		{"tabs kept", "col1\tcol2", "col1\tcol2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeControlKeepNewlines(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeControlKeepNewlines(%q) = %q, want %q",
					tt.input, got, tt.want)
			}
		})
	}
}

func TestStreamFormatter_TextToToolTransition(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventAssistantText("Reviewing code."))
	fix.writeLine(eventAssistantToolUse("Read", map[string]any{
		"file_path": "main.go",
	}))
	fix.writeLine(eventAssistantToolUse("Edit", map[string]any{
		"file_path":  "main.go",
		"old_string": "old", "new_string": "new",
	}))
	fix.writeLine(eventAssistantText("Done fixing."))

	out := fix.output()
	// Text → tool transition should have blank line separation
	lines := strings.Split(out, "\n")
	var emptyCount int
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			emptyCount++
		}
	}
	// At least 2 blank lines: text→tool and tool→text transitions
	if emptyCount < 2 {
		t.Errorf("expected at least 2 blank lines for transitions, got %d in:\n%s",
			emptyCount, out)
	}

	// Tool lines should have gutter prefix
	for _, l := range lines {
		if strings.Contains(l, "Read") && strings.Contains(l, "main.go") {
			if !strings.Contains(l, "|") && !strings.Contains(l, "│") {
				t.Errorf("tool line should have gutter prefix, got: %q", l)
			}
		}
	}

	// Consecutive tool calls should NOT have blank line between them
	fix.assertContains(t, "Read   main.go")
	fix.assertContains(t, "Edit   main.go")
	fix.assertContains(t, "Done fixing.")
}

func TestStreamFormatter_PartialWrites(t *testing.T) {
	fix := newFixture(true)

	full := eventAssistantText("hello") + "\n"
	// Write in two parts
	_, _ = fix.f.Write([]byte(full[:20]))
	if fix.buf.Len() != 0 {
		t.Errorf("partial write should buffer, got:\n%s", fix.output())
	}
	_, _ = fix.f.Write([]byte(full[20:]))
	fix.assertContains(t, "hello")
}

func TestStreamFormatter_SanitizesAssistantText(t *testing.T) {
	// ANSI/OSC escape sequences in assistant text (which can
	// originate from model-reflected untrusted repo content)
	// must be stripped before rendering. Glamour/lipgloss add
	// their own styling codes — only injected sequences should
	// be gone.
	fix := newFixture(true)
	text := "\x1b[31mred\x1b[0m normal \x1b]0;evil\x07"
	fix.writeLine(eventAssistantText(text))

	raw := fix.buf.String()
	// OSC title-change and BEL must never reach the terminal.
	if strings.Contains(raw, "\x1b]0;") {
		t.Errorf("OSC title escape leaked: %q", raw)
	}
	if strings.Contains(raw, "\x07") {
		t.Errorf("BEL char leaked: %q", raw)
	}
	// Injected SGR color (\x1b[31m) should be stripped;
	// only glamour/lipgloss styling should remain.
	if strings.Contains(raw, "\x1b[31m") {
		t.Errorf("injected SGR escape leaked: %q", raw)
	}
	// The actual text content should survive.
	fix.assertContains(t, "red")
	fix.assertContains(t, "normal")
	// "evil" should not appear (it was inside the OSC payload).
	fix.assertNotContains(t, "evil")
}

func TestStreamFormatter_SanitizesToolArgs(t *testing.T) {
	// Control sequences in tool arguments (file paths, commands)
	// must be stripped before rendering.
	fix := newFixture(true)
	fix.writeLine(eventAssistantToolUse("Read", map[string]any{
		"file_path": "/tmp/\x1b]0;evil\x07\x1b[31mred\x1b[0m.go",
	}))

	raw := fix.buf.String()
	if strings.Contains(raw, "\x1b]0;") {
		t.Errorf("OSC escape leaked in tool arg: %q", raw)
	}
	if strings.Contains(raw, "\x07") {
		t.Errorf("BEL char leaked in tool arg: %q", raw)
	}
	if strings.Contains(raw, "\x1b[31m") {
		t.Errorf("injected SGR escape leaked in tool arg: %q", raw)
	}
	fix.assertContains(t, "Read")
	fix.assertContains(t, ".go")
}

func TestStreamFormatter_SanitizesToolName(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventAssistantToolUse(
		"Read\x1b[31m", map[string]any{
			"file_path": "clean.go",
		},
	))

	raw := fix.buf.String()
	if strings.Contains(raw, "\x1b[31m") {
		t.Errorf("injected SGR in tool name leaked: %q", raw)
	}
	fix.assertContains(t, "clean.go")
}

// Event builder for OpenCode-style JSON.

func eventOpenCode(eventType string, part map[string]any) string {
	return toJson(map[string]any{
		"type": eventType,
		"part": part,
	})
}

func TestStreamFormatter_OpenCodeText(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventOpenCode("text", map[string]any{
		"type": "text",
		"text": "I found a bug in the code.",
	}))
	fix.assertContains(t, "I found a bug in the code.")
}

func TestStreamFormatter_OpenCodeTool(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventOpenCode("tool", map[string]any{
		"type": "tool",
		"tool": "Read",
		"id":   "tc_1",
		"state": map[string]any{
			"status": "running",
			"input": map[string]any{
				"file_path": "internal/server.go",
			},
		},
	}))
	fix.writeLine(eventOpenCode("tool", map[string]any{
		"type": "tool",
		"tool": "Bash",
		"id":   "tc_2",
		"state": map[string]any{
			"status": "running",
			"input": map[string]any{
				"command": "go test ./...",
			},
		},
	}))
	fix.writeLine(eventOpenCode("tool", map[string]any{
		"type": "tool",
		"tool": "Grep",
		"id":   "tc_3",
		"state": map[string]any{
			"status": "running",
			"input": map[string]any{
				"pattern": "TODO",
				"path":    "internal/",
			},
		},
	}))

	fix.assertContains(t, "Read   internal/server.go")
	fix.assertContains(t, "Bash   go test ./...")
	fix.assertContains(t, "Grep   TODO  internal/")
}

func TestStreamFormatter_OpenCodeToolDedup(t *testing.T) {
	fix := newFixture(true)
	// Same ID, pending then running then completed
	fix.writeLine(eventOpenCode("tool", map[string]any{
		"type": "tool", "tool": "Read", "id": "tc_1",
		"state": map[string]any{"status": "pending"},
	}))
	fix.writeLine(eventOpenCode("tool", map[string]any{
		"type": "tool", "tool": "Read", "id": "tc_1",
		"state": map[string]any{
			"status": "running",
			"input": map[string]any{
				"file_path": "main.go",
			},
		},
	}))
	fix.writeLine(eventOpenCode("tool", map[string]any{
		"type": "tool", "tool": "Read", "id": "tc_1",
		"state": map[string]any{
			"status": "completed",
			"input": map[string]any{
				"file_path": "main.go",
			},
		},
	}))

	// Should render exactly once (the "running" event)
	fix.assertCount(t, "Read   main.go", 1)
}

func TestStreamFormatter_OpenCodeReasoning(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventOpenCode("reasoning", map[string]any{
		"type": "reasoning",
		"text": "Reviewing error handling changes",
	}))
	fix.assertContains(t, "Reviewing error handling changes")
}

func TestStreamFormatter_OpenCodeSuppressesLifecycle(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventOpenCode("step_start", map[string]any{
		"type": "step-start",
	}))
	fix.writeLine(eventOpenCode("step_finish", map[string]any{
		"type":   "step-finish",
		"reason": "stop",
		"cost":   0.01,
	}))
	fix.assertEmpty(t)
}

func TestStreamFormatter_OpenCodePendingToolSuppressed(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(eventOpenCode("tool", map[string]any{
		"type":  "tool",
		"tool":  "Read",
		"id":    "tc_1",
		"state": map[string]any{"status": "pending"},
	}))
	fix.assertEmpty(t)
}

func TestStreamFormatter_SanitizesGeminiText(t *testing.T) {
	fix := newFixture(true)
	ev := toJson(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "\x1b[1mbold\x1b[0m safe \x1b]0;title\x07",
	})
	fix.writeLine(ev)

	raw := fix.buf.String()
	if strings.Contains(raw, "\x1b]0;") {
		t.Errorf("OSC escape leaked in Gemini text: %q", raw)
	}
	if strings.Contains(raw, "\x1b[1m") {
		t.Errorf("injected bold escape leaked: %q", raw)
	}
	fix.assertContains(t, "bold")
	fix.assertContains(t, "safe")
	fix.assertNotContains(t, "title")
}
