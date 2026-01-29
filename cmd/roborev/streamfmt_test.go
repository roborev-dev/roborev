package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestStreamFormatter_ToolUse(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"internal/gmail/ratelimit.go"}}]}}`,
		`{"type":"user","tool_use_result":{"filePath":"internal/gmail/ratelimit.go"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"internal/gmail/ratelimit.go","old_string":"foo","new_string":"bar"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./internal/gmail/ -run TestRateLimiter"}}]}}`,
	}

	for _, line := range lines {
		f.Write([]byte(line + "\n"))
	}

	got := buf.String()

	// Check expected formatted output
	if !strings.Contains(got, "Read   internal/gmail/ratelimit.go") {
		t.Errorf("expected Read line, got:\n%s", got)
	}
	if !strings.Contains(got, "Edit   internal/gmail/ratelimit.go") {
		t.Errorf("expected Edit line, got:\n%s", got)
	}
	if !strings.Contains(got, "Bash   go test ./internal/gmail/ -run TestRateLimiter") {
		t.Errorf("expected Bash line, got:\n%s", got)
	}
	// tool_use_result (user type) should be suppressed
	if strings.Contains(got, "tool_use_result") || strings.Contains(got, "filePath") {
		t.Errorf("tool result should be suppressed, got:\n%s", got)
	}
}

func TestStreamFormatter_TextOutput(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	f.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll fix this now."}]}}` + "\n"))

	got := buf.String()
	if !strings.Contains(got, "I'll fix this now.") {
		t.Errorf("expected text output, got:\n%s", got)
	}
}

func TestStreamFormatter_NonTTY(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, false)

	raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n"
	f.Write([]byte(raw))

	// Non-TTY should pass through raw JSON
	if buf.String() != raw {
		t.Errorf("non-TTY should pass through raw, got:\n%s", buf.String())
	}
}

func TestStreamFormatter_ResultSuppressed(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	f.Write([]byte(`{"type":"result","result":"final summary text"}` + "\n"))

	if buf.String() != "" {
		t.Errorf("result events should be suppressed, got:\n%s", buf.String())
	}
}

func TestStreamFormatter_BashTruncation(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	longCmd := strings.Repeat("x", 100)
	f.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` + longCmd + `"}}]}}` + "\n"))

	got := buf.String()
	if len(got) > 100 {
		// Should be truncated to ~80 chars + "Bash   " prefix + "..." + newline
		if !strings.Contains(got, "...") {
			t.Errorf("long bash command should be truncated with ..., got:\n%s", got)
		}
	}
}

func TestStreamFormatter_GrepWithPath(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	f.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"TODO","path":"internal/"}}]}}` + "\n"))

	got := buf.String()
	if !strings.Contains(got, "Grep   TODO  internal/") {
		t.Errorf("expected Grep with pattern and path, got:\n%s", got)
	}
}

func TestStreamFormatter_LegacyStringContent(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	f.Write([]byte(`{"type":"assistant","message":{"content":"legacy string content"}}` + "\n"))

	got := buf.String()
	if !strings.Contains(got, "legacy string content") {
		t.Errorf("expected legacy string content, got:\n%s", got)
	}
}

func TestStreamFormatter_MultipleContentBlocks(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	f.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking..."},{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}` + "\n"))

	got := buf.String()
	if !strings.Contains(got, "thinking...") {
		t.Errorf("expected text block, got:\n%s", got)
	}
	if !strings.Contains(got, "Read   main.go") {
		t.Errorf("expected Read tool use, got:\n%s", got)
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestStreamFormatter_WriteError(t *testing.T) {
	f := newStreamFormatter(errWriter{}, true)

	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n"
	_, err := f.Write([]byte(line))
	if err == nil {
		t.Fatal("expected write error to propagate")
	}
	if err != io.ErrClosedPipe {
		t.Fatalf("expected ErrClosedPipe, got %v", err)
	}
}

func TestStreamFormatter_GeminiToolUse(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	lines := []string{
		`{"type":"init","session_id":"abc"}`,
		`{"type":"message","role":"user","content":"fix this"}`,
		`{"type":"message","role":"assistant","content":"I'll fix this.","delta":true}`,
		`{"type":"tool_use","tool_name":"read_file","tool_id":"t1","parameters":{"file_path":"main.go"}}`,
		`{"type":"tool_result","tool_id":"t1","status":"success"}`,
		`{"type":"tool_use","tool_name":"replace","tool_id":"t2","parameters":{"file_path":"main.go","old_string":"foo","new_string":"bar"}}`,
		`{"type":"tool_use","tool_name":"run_shell_command","tool_id":"t3","parameters":{"command":"go test ./..."}}`,
		`{"type":"result","status":"success"}`,
	}

	for _, line := range lines {
		f.Write([]byte(line + "\n"))
	}

	got := buf.String()

	if !strings.Contains(got, "I'll fix this.") {
		t.Errorf("expected assistant text, got:\n%s", got)
	}
	if !strings.Contains(got, "Read   main.go") {
		t.Errorf("expected Read line, got:\n%s", got)
	}
	if !strings.Contains(got, "Edit   main.go") {
		t.Errorf("expected Edit line for replace tool, got:\n%s", got)
	}
	if !strings.Contains(got, "Bash   go test ./...") {
		t.Errorf("expected Bash line for run_shell_command, got:\n%s", got)
	}
	// init, user message, tool_result, and result should be suppressed
	if strings.Contains(got, "session_id") || strings.Contains(got, "tool_id") || strings.Contains(got, "status") {
		t.Errorf("should suppress non-assistant events, got:\n%s", got)
	}
}

func TestStreamFormatter_GeminiToolResult_Suppressed(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	f.Write([]byte(`{"type":"tool_result","tool_id":"t1","status":"success","output":"file contents here"}` + "\n"))

	if buf.String() != "" {
		t.Errorf("tool_result should be suppressed, got:\n%s", buf.String())
	}
}

func TestStreamFormatter_PartialWrites(t *testing.T) {
	var buf bytes.Buffer
	f := newStreamFormatter(&buf, true)

	full := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n"
	// Write in two parts
	f.Write([]byte(full[:20]))
	if buf.Len() != 0 {
		t.Errorf("partial write should buffer, got:\n%s", buf.String())
	}
	f.Write([]byte(full[20:]))
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("expected output after complete line, got:\n%s", buf.String())
	}
}
