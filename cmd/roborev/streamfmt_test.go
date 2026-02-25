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

type streamTestCase struct {
	name        string
	events      []string
	contains    []string
	notContains []string
	counts      map[string]int
	empty       bool
	checkOutput func(*testing.T, *streamFormatterFixture)
}

func runStreamTestCase(t *testing.T, tc streamTestCase) {
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
			tc.checkOutput(t, fix)
		}
	})
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

func eventAssistantToolUseResult(filePath string) string {
	return toJson(map[string]any{"type": "user", "tool_use_result": map[string]any{"filePath": filePath}})
}

func eventAssistantResult(result string) string {
	return toJson(map[string]any{"type": "result", "result": result})
}

// Event builders for Gemini-style JSON.

func eventGeminiInit(sessionID string) string {
	return toJson(map[string]any{"type": "init", "session_id": sessionID})
}

func eventGeminiMessage(role, content string, delta bool) string {
	m := map[string]any{"type": "message", "role": role, "content": content}
	if delta {
		m["delta"] = true
	}
	return toJson(m)
}

func eventGeminiToolUse(toolName, toolID string, params map[string]any) string {
	return toJson(map[string]any{
		"type":       "tool_use",
		"tool_name":  toolName,
		"tool_id":    toolID,
		"parameters": params,
	})
}

func eventGeminiToolResult(toolID, status, output string) string {
	m := map[string]any{"type": "tool_result", "tool_id": toolID, "status": status}
	if output != "" {
		m["output"] = output
	}
	return toJson(m)
}

func eventGeminiResult(status string) string {
	return toJson(map[string]any{"type": "result", "status": status})
}

// Event builder for OpenCode-style JSON.

func eventOpenCode(eventType string, part map[string]any) string {
	return toJson(map[string]any{
		"type": eventType,
		"part": part,
	})
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

func TestStreamFormatter_Anthropic(t *testing.T) {
	tests := []streamTestCase{
		{
			name: "ToolUse",
			events: []string{
				eventAssistantToolUse("Read", map[string]any{"file_path": "internal/gmail/ratelimit.go"}),
				eventAssistantToolUseResult("internal/gmail/ratelimit.go"),
				eventAssistantToolUse("Edit", map[string]any{"file_path": "internal/gmail/ratelimit.go", "old_string": "foo", "new_string": "bar"}),
				eventAssistantToolUse("Bash", map[string]any{"command": "go test ./internal/gmail/ -run TestRateLimiter"}),
			},
			contains: []string{
				"Read   internal/gmail/ratelimit.go",
				"Edit   internal/gmail/ratelimit.go",
				"Bash   go test ./internal/gmail/ -run TestRateLimiter",
			},
			notContains: []string{
				"tool_use_result",
				"filePath",
			},
		},
		{
			name: "TextOutput",
			events: []string{
				eventAssistantText("I'll fix this now."),
			},
			contains: []string{"I'll fix this now."},
		},
		{
			name: "ResultSuppressed",
			events: []string{
				eventAssistantResult("final summary text"),
			},
			empty: true,
		},
		{
			name: "BashTruncation",
			events: []string{
				eventAssistantToolUse("Bash", map[string]any{"command": strings.Repeat("x", 100)}),
			},
			checkOutput: func(t *testing.T, fix *streamFormatterFixture) {
				got := fix.output()
				if len(got) > 100 {
					if !strings.Contains(got, "...") {
						t.Errorf("long bash command should be truncated with ..., got:\n%s", got)
					}
				}
			},
		},
		{
			name: "GrepWithPath",
			events: []string{
				eventAssistantToolUse("Grep", map[string]any{"pattern": "TODO", "path": "internal/"}),
			},
			contains: []string{"Grep   TODO  internal/"},
		},
		{
			name: "LegacyStringContent",
			events: []string{
				eventAssistantLegacy("legacy string content"),
			},
			contains: []string{"legacy string content"},
		},
		{
			name: "MultipleContentBlocks",
			events: []string{
				eventAssistantMulti(
					contentBlockText("thinking..."),
					contentBlockToolUse("Read", map[string]any{"file_path": "main.go"}),
				),
			},
			contains: []string{
				"thinking...",
				"Read   main.go",
			},
		},
		{
			name: "TextToToolTransition",
			events: []string{
				eventAssistantText("Reviewing code."),
				eventAssistantToolUse("Read", map[string]any{
					"file_path": "main.go",
				}),
				eventAssistantToolUse("Edit", map[string]any{
					"file_path":  "main.go",
					"old_string": "old", "new_string": "new",
				}),
				eventAssistantText("Done fixing."),
			},
			contains: []string{
				"Read   main.go",
				"Edit   main.go",
				"Done fixing.",
			},
			checkOutput: func(t *testing.T, fix *streamFormatterFixture) {
				out := fix.output()
				lines := strings.Split(out, "\n")
				var emptyCount int
				for _, l := range lines {
					if strings.TrimSpace(l) == "" {
						emptyCount++
					}
				}
				if emptyCount < 2 {
					t.Errorf("expected at least 2 blank lines for transitions, got %d in:\n%s",
						emptyCount, out)
				}
				for _, l := range lines {
					if strings.Contains(l, "Read") && strings.Contains(l, "main.go") {
						if !strings.Contains(l, "|") && !strings.Contains(l, "│") {
							t.Errorf("tool line should have gutter prefix, got: %q", l)
						}
					}
				}
			},
		},
		{
			name: "SanitizesAssistantText",
			events: []string{
				eventAssistantText("\x1b[31mred\x1b[0m normal \x1b]0;evil\x07"),
			},
			contains:    []string{"red", "normal"},
			notContains: []string{"evil"},
			checkOutput: func(t *testing.T, fix *streamFormatterFixture) {
				raw := fix.buf.String()
				if strings.Contains(raw, "\x1b]0;") {
					t.Errorf("OSC title escape leaked: %q", raw)
				}
				if strings.Contains(raw, "\x07") {
					t.Errorf("BEL char leaked: %q", raw)
				}
				if strings.Contains(raw, "\x1b[31m") {
					t.Errorf("injected SGR escape leaked: %q", raw)
				}
			},
		},
		{
			name: "SanitizesToolArgs",
			events: []string{
				eventAssistantToolUse("Read", map[string]any{
					"file_path": "/tmp/\x1b]0;evil\x07\x1b[31mred\x1b[0m.go",
				}),
			},
			contains: []string{"Read", ".go"},
			checkOutput: func(t *testing.T, fix *streamFormatterFixture) {
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
			},
		},
		{
			name: "SanitizesToolName",
			events: []string{
				eventAssistantToolUse(
					"Read\x1b[31m", map[string]any{
						"file_path": "clean.go",
					},
				),
			},
			contains: []string{"clean.go"},
			checkOutput: func(t *testing.T, fix *streamFormatterFixture) {
				raw := fix.buf.String()
				if strings.Contains(raw, "\x1b[31m") {
					t.Errorf("injected SGR in tool name leaked: %q", raw)
				}
			},
		},
	}

	for _, tc := range tests {
		runStreamTestCase(t, tc)
	}
}

func TestStreamFormatter_Gemini(t *testing.T) {
	tests := []streamTestCase{
		{
			name: "ToolUse",
			events: []string{
				eventGeminiInit("abc"),
				eventGeminiMessage("user", "fix this", false),
				eventGeminiMessage("assistant", "I'll fix this.", true),
				eventGeminiToolUse("read_file", "t1", map[string]any{"file_path": "main.go"}),
				eventGeminiToolResult("t1", "success", ""),
				eventGeminiToolUse("replace", "t2", map[string]any{"file_path": "main.go", "old_string": "foo", "new_string": "bar"}),
				eventGeminiToolUse("run_shell_command", "t3", map[string]any{"command": "go test ./..."}),
				eventGeminiResult("success"),
			},
			contains: []string{
				"I'll fix this.",
				"Read   main.go",
				"Edit   main.go",
				"Bash   go test ./...",
			},
			notContains: []string{
				"session_id",
				"tool_id",
				"status",
			},
		},
		{
			name: "ToolResultSuppressed",
			events: []string{
				eventGeminiToolResult("t1", "success", "file contents here"),
			},
			empty: true,
		},
		{
			name: "SanitizesGeminiText",
			events: []string{
				toJson(map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": "\x1b[1mbold\x1b[0m safe \x1b]0;title\x07",
				}),
			},
			contains:    []string{"bold", "safe"},
			notContains: []string{"title"},
			checkOutput: func(t *testing.T, fix *streamFormatterFixture) {
				raw := fix.buf.String()
				if strings.Contains(raw, "\x1b]0;") {
					t.Errorf("OSC escape leaked in Gemini text: %q", raw)
				}
				if strings.Contains(raw, "\x1b[1m") {
					t.Errorf("injected bold escape leaked: %q", raw)
				}
			},
		},
	}

	for _, tc := range tests {
		runStreamTestCase(t, tc)
	}
}

func TestStreamFormatter_OpenCode(t *testing.T) {
	tests := []streamTestCase{
		{
			name: "Text",
			events: []string{
				eventOpenCode("text", map[string]any{
					"type": "text",
					"text": "I found a bug in the code.",
				}),
			},
			contains: []string{"I found a bug in the code."},
		},
		{
			name: "Tool",
			events: []string{
				eventOpenCode("tool", map[string]any{
					"type": "tool",
					"tool": "Read",
					"id":   "tc_1",
					"state": map[string]any{
						"status": "running",
						"input": map[string]any{
							"file_path": "internal/server.go",
						},
					},
				}),
				eventOpenCode("tool", map[string]any{
					"type": "tool",
					"tool": "Bash",
					"id":   "tc_2",
					"state": map[string]any{
						"status": "running",
						"input": map[string]any{
							"command": "go test ./...",
						},
					},
				}),
				eventOpenCode("tool", map[string]any{
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
				}),
			},
			contains: []string{
				"Read   internal/server.go",
				"Bash   go test ./...",
				"Grep   TODO  internal/",
			},
		},
		{
			name: "ToolDedup",
			events: []string{
				eventOpenCode("tool", map[string]any{
					"type": "tool", "tool": "Read", "id": "tc_1",
					"state": map[string]any{"status": "pending"},
				}),
				eventOpenCode("tool", map[string]any{
					"type": "tool", "tool": "Read", "id": "tc_1",
					"state": map[string]any{
						"status": "running",
						"input": map[string]any{
							"file_path": "main.go",
						},
					},
				}),
				eventOpenCode("tool", map[string]any{
					"type": "tool", "tool": "Read", "id": "tc_1",
					"state": map[string]any{
						"status": "completed",
						"input": map[string]any{
							"file_path": "main.go",
						},
					},
				}),
			},
			counts: map[string]int{"Read   main.go": 1},
		},
		{
			name: "Reasoning",
			events: []string{
				eventOpenCode("reasoning", map[string]any{
					"type": "reasoning",
					"text": "Reviewing error handling changes",
				}),
			},
			contains: []string{"Reviewing error handling changes"},
		},
		{
			name: "SuppressesLifecycle",
			events: []string{
				eventOpenCode("step_start", map[string]any{
					"type": "step-start",
				}),
				eventOpenCode("step_finish", map[string]any{
					"type":   "step-finish",
					"reason": "stop",
					"cost":   0.01,
				}),
			},
			empty: true,
		},
		{
			name: "PendingToolSuppressed",
			events: []string{
				eventOpenCode("tool", map[string]any{
					"type":  "tool",
					"tool":  "Read",
					"id":    "tc_1",
					"state": map[string]any{"status": "pending"},
				}),
			},
			empty: true,
		},
	}

	for _, tc := range tests {
		runStreamTestCase(t, tc)
	}
}

func TestStreamFormatter_Codex_Scenarios(t *testing.T) {
	longCmd := "bash -lc " + strings.Repeat("x", 100)
	tests := []streamTestCase{
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
			checkOutput: func(t *testing.T, fix *streamFormatterFixture) {
				output := fix.output()
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
		runStreamTestCase(t, tc)
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
