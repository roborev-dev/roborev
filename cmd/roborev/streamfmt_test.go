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
	fix.f.Write([]byte(s + "\n"))
}

func (fix *streamFormatterFixture) output() string {
	return fix.buf.String()
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

// mustMarshal is like json.Marshal but panics on error.
// Safe to use in tests where inputs are always simple map literals.
func mustMarshal(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustMarshal: %v", err))
	}
	return b
}

// Event builders for Anthropic-style JSON.

func eventAssistantToolUse(toolName string, input map[string]interface{}) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":%q,"input":%s}]}}`,
		toolName, mustMarshal(input))
}

func eventAssistantText(text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%s}]}}`, mustMarshal(text))
}

func eventAssistantMulti(blocks ...string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[%s]}}`, strings.Join(blocks, ","))
}

func contentBlockText(text string) string {
	return fmt.Sprintf(`{"type":"text","text":%s}`, mustMarshal(text))
}

func contentBlockToolUse(toolName string, input map[string]interface{}) string {
	return fmt.Sprintf(`{"type":"tool_use","name":%q,"input":%s}`, toolName, mustMarshal(input))
}

func eventAssistantLegacy(content string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":%s}}`, mustMarshal(content))
}

// Event builders for Gemini-style JSON.

func eventGeminiToolUse(toolName, toolID string, params map[string]interface{}) string {
	return fmt.Sprintf(`{"type":"tool_use","tool_name":%q,"tool_id":%q,"parameters":%s}`,
		toolName, toolID, mustMarshal(params))
}

func TestStreamFormatter_ToolUse(t *testing.T) {
	fix := newFixture(true)

	fix.writeLine(eventAssistantToolUse("Read", map[string]interface{}{"file_path": "internal/gmail/ratelimit.go"}))
	fix.writeLine(`{"type":"user","tool_use_result":{"filePath":"internal/gmail/ratelimit.go"}}`)
	fix.writeLine(eventAssistantToolUse("Edit", map[string]interface{}{"file_path": "internal/gmail/ratelimit.go", "old_string": "foo", "new_string": "bar"}))
	fix.writeLine(eventAssistantToolUse("Bash", map[string]interface{}{"command": "go test ./internal/gmail/ -run TestRateLimiter"}))

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
	fix.writeLine(eventAssistantToolUse("Bash", map[string]interface{}{"command": longCmd}))

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
	fix.writeLine(eventAssistantToolUse("Grep", map[string]interface{}{"pattern": "TODO", "path": "internal/"}))
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
		contentBlockToolUse("Read", map[string]interface{}{"file_path": "main.go"}),
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
		eventGeminiToolUse("read_file", "t1", map[string]interface{}{"file_path": "main.go"}),
		`{"type":"tool_result","tool_id":"t1","status":"success"}`,
		eventGeminiToolUse("replace", "t2", map[string]interface{}{"file_path": "main.go", "old_string": "foo", "new_string": "bar"}),
		eventGeminiToolUse("run_shell_command", "t3", map[string]interface{}{"command": "go test ./..."}),
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

func TestStreamFormatter_CodexEvents(t *testing.T) {
	fix := newFixture(true)

	lines := []string{
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
	}

	for _, line := range lines {
		fix.writeLine(line)
	}

	fix.assertContains(t, "Bash   bash -lc ls")
	fix.assertContains(t, "Edit")
	fix.assertContains(t, "I fixed the issue.")
	fix.assertCount(t, "Bash   bash -lc ls", 1)
	fix.assertCount(t, "Edit", 1)
	// Lifecycle events should be suppressed
	fix.assertNotContains(t, "thread_id")
	fix.assertNotContains(t, "turn.started")
	fix.assertNotContains(t, "input_tokens")
	fix.assertNotContains(t, "draft")
}

func TestStreamFormatter_CodexUpdatedSuppressed(t *testing.T) {
	fix := newFixture(true)

	lines := []string{
		`{"type":"item.updated","item":{"type":"command_execution","command":"bash -lc ls"}}`,
		`{"type":"item.updated","item":{"type":"file_change","changes":[{"path":"main.go","kind":"update"}]}}`,
		`{"type":"item.updated","item":{"type":"agent_message","text":"still drafting"}}`,
	}

	for _, line := range lines {
		fix.writeLine(line)
	}

	fix.assertEmpty(t)
}

func TestStreamFormatter_CodexSanitizesControlChars(t *testing.T) {
	fix := newFixture(true)

	// Agent message with ANSI escape and control chars
	fix.writeLine(`{"type":"item.completed","item":{"type":"agent_message","text":"\u001b[31mred\u001b[0m and \u0007bell"}}`)
	fix.assertContains(t, "red and bell")
	fix.assertNotContains(t, "\x1b")
	fix.assertNotContains(t, "\x07")

	// Command with ANSI escape
	fix.writeLine(`{"type":"item.started","item":{"type":"command_execution","command":"bash -lc \u001b[32mls\u001b[0m"}}`)
	fix.assertContains(t, "Bash   bash -lc ls")
	fix.assertNotContains(t, "\x1b")
}

func TestStreamFormatter_CodexLifecycleSuppressed(t *testing.T) {
	fix := newFixture(true)
	fix.writeLine(`{"type":"thread.started","thread_id":"abc"}`)
	fix.writeLine(`{"type":"turn.started"}`)
	fix.writeLine(`{"type":"turn.completed","usage":{"input_tokens":100}}`)
	fix.assertEmpty(t)
}

func TestStreamFormatter_CodexCommandTruncation(t *testing.T) {
	fix := newFixture(true)
	longCmd := "bash -lc " + strings.Repeat("x", 100)
	fix.writeLine(fmt.Sprintf(`{"type":"item.started","item":{"type":"command_execution","command":%q}}`, longCmd))
	got := fix.output()
	if !strings.Contains(got, "...") {
		t.Errorf("long codex command should be truncated with ..., got:\n%s", got)
	}
}

func TestStreamFormatter_CodexCommandCompletedFallback(t *testing.T) {
	fix := newFixture(true)

	lines := []string{
		`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution"}}`,
		`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
		`{"type":"item.completed","item":{"id":"cmd_2","type":"command_execution","command":"bash -lc pwd"}}`,
	}

	for _, line := range lines {
		fix.writeLine(line)
	}

	fix.assertContains(t, "Bash   bash -lc ls")
	fix.assertContains(t, "Bash   bash -lc pwd")
	fix.assertCount(t, "Bash   bash -lc ls", 1)
	fix.assertCount(t, "Bash   bash -lc pwd", 1)
}

func TestStreamFormatter_CodexCommandMixedIDStartedWithoutIDCompletedWithID(t *testing.T) {
	fix := newFixture(true)

	lines := []string{
		`{"type":"item.started","item":{"type":"command_execution","command":"bash -lc ls"}}`,
		`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
	}

	for _, line := range lines {
		fix.writeLine(line)
	}

	fix.assertContains(t, "Bash   bash -lc ls")
	fix.assertCount(t, "Bash   bash -lc ls", 1)
}

func TestStreamFormatter_CodexCommandMixedIDStartedWithIDCompletedWithoutID(t *testing.T) {
	fix := newFixture(true)

	lines := []string{
		`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
	}

	for _, line := range lines {
		fix.writeLine(line)
	}

	fix.assertContains(t, "Bash   bash -lc ls")
	fix.assertCount(t, "Bash   bash -lc ls", 1)
}

func TestStreamFormatter_CodexCommandStartedWithIDCompletedWithoutCommandClearsCounter(t *testing.T) {
	fix := newFixture(true)

	lines := []string{
		`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
		`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution"}}`,
		`{"type":"item.completed","item":{"id":"cmd_2","type":"command_execution","command":"bash -lc ls"}}`,
	}

	for _, line := range lines {
		fix.writeLine(line)
	}

	fix.assertContains(t, "Bash   bash -lc ls")
	fix.assertCount(t, "Bash   bash -lc ls", 2)
}

func TestStreamFormatter_CodexCommandMixedIDFallbackDoesNotLeaveStaleID(t *testing.T) {
	fix := newFixture(true)

	lines := []string{
		`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"bash -lc ls"}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
		`{"type":"item.started","item":{"id":"cmd_2","type":"command_execution","command":"bash -lc ls"}}`,
		`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution"}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
	}

	for _, line := range lines {
		fix.writeLine(line)
	}

	fix.assertContains(t, "Bash   bash -lc ls")
	fix.assertCount(t, "Bash   bash -lc ls", 2)
}

func TestStreamFormatter_CodexMultiIDSameCommandDeterministicPairing(t *testing.T) {
	fix := newFixture(true)

	// Two started events with same command text but different IDs.
	// A command-only completion should consume the oldest ID (FIFO cmd_A),
	// leaving cmd_B valid for its own ID-only completion.
	// A final command-only completion with no remaining started counter
	// must render, proving state was fully drained.
	lines := []string{
		`{"type":"item.started","item":{"id":"cmd_A","type":"command_execution","command":"bash -lc ls"}}`,
		`{"type":"item.started","item":{"id":"cmd_B","type":"command_execution","command":"bash -lc ls"}}`,
		// Command-only completion consumes cmd_A (FIFO), counter 2→1.
		`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
		// ID-only completion for cmd_B clears remaining state, counter 1→0.
		`{"type":"item.completed","item":{"id":"cmd_B","type":"command_execution"}}`,
		// With all started state drained, this completion must render.
		// If the wrong ID was consumed above, a stale counter remains and
		// this would be suppressed instead.
		`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
	}

	for _, line := range lines {
		fix.writeLine(line)
	}

	// 2 started renders + 1 unpaired completion render = 3.
	fix.assertCount(t, "Bash   bash -lc ls", 3)
}

func TestStreamFormatter_PartialWrites(t *testing.T) {
	fix := newFixture(true)

	full := eventAssistantText("hello") + "\n"
	// Write in two parts
	fix.f.Write([]byte(full[:20]))
	if fix.buf.Len() != 0 {
		t.Errorf("partial write should buffer, got:\n%s", fix.output())
	}
	fix.f.Write([]byte(full[20:]))
	fix.assertContains(t, "hello")
}
