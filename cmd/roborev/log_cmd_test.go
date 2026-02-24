package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLogCleanCmd_NegativeDays(t *testing.T) {
	cmd := logCleanCmd()
	cmd.SetArgs([]string{"--days", "-1"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for negative --days")
	}
}

func TestLogCleanCmd_OverflowDays(t *testing.T) {
	cmd := logCleanCmd()
	cmd.SetArgs([]string{"--days", "999999"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for oversized --days")
	}
}

func TestRenderJobLog_JSONL(t *testing.T) {
	// Simulate Claude Code JSONL output
	input := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"abc123"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Reviewing the code."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`,
		`{"type":"tool_result","content":"file contents"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looks good overall."}]}}`,
	}, "\n")

	var buf bytes.Buffer
	err := renderJobLog(strings.NewReader(input), &buf, true)
	if err != nil {
		t.Fatalf("renderJobLog: %v", err)
	}

	out := ansiEscapePattern.ReplaceAllString(buf.String(), "")
	// Should contain the text messages
	if !strings.Contains(out, "Reviewing the code.") {
		t.Errorf("output should contain agent text, got:\n%s", out)
	}
	if !strings.Contains(out, "Read") {
		t.Errorf("output should contain tool name, got:\n%s", out)
	}
	if !strings.Contains(out, "main.go") {
		t.Errorf("output should contain file path, got:\n%s", out)
	}

	// Should NOT contain raw JSON
	if strings.Contains(out, `"type"`) {
		t.Errorf("output should not contain raw JSON, got:\n%s", out)
	}
}

func TestRenderJobLog_PlainText(t *testing.T) {
	input := "line 1\nline 2\nline 3\n"

	var buf bytes.Buffer
	err := renderJobLog(strings.NewReader(input), &buf, true)
	if err != nil {
		t.Fatalf("renderJobLog: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "line 1") {
		t.Errorf("plain text should pass through, got:\n%s", out)
	}
	if !strings.Contains(out, "line 3") {
		t.Errorf("plain text should pass through, got:\n%s", out)
	}
}

func TestRenderJobLog_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := renderJobLog(strings.NewReader(""), &buf, true)
	if err != nil {
		t.Fatalf("renderJobLog: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output, got %q", buf.String())
	}
}

func TestRenderJobLog_OversizedLine(t *testing.T) {
	// Lines larger than 1MB should not cause errors (no Scanner cap).
	bigPayload := strings.Repeat("x", 2*1024*1024)
	input := fmt.Sprintf(
		`{"type":"tool_result","content":"%s"}`, bigPayload,
	)

	var buf bytes.Buffer
	err := renderJobLog(strings.NewReader(input), &buf, true)
	if err != nil {
		t.Fatalf("renderJobLog should handle large lines: %v", err)
	}
}

func TestRenderJobLog_PlainTextPreservesBlankLines(t *testing.T) {
	input := "line 1\n\nline 3\n"

	var buf bytes.Buffer
	err := renderJobLog(strings.NewReader(input), &buf, true)
	if err != nil {
		t.Fatalf("renderJobLog: %v", err)
	}

	out := buf.String()
	// Blank line should be preserved between line 1 and line 3.
	if !strings.Contains(out, "line 1\n\nline 3") {
		t.Errorf("blank line should be preserved, got:\n%q", out)
	}
}

func TestIsBrokenPipe(t *testing.T) {
	if isBrokenPipe(nil) {
		t.Error("nil should not be broken pipe")
	}
	if isBrokenPipe(fmt.Errorf("other error")) {
		t.Error("non-EPIPE error should not be broken pipe")
	}
	if !isBrokenPipe(syscall.EPIPE) {
		t.Error("bare EPIPE should be broken pipe")
	}
	if !isBrokenPipe(fmt.Errorf("write: %w", syscall.EPIPE)) {
		t.Error("wrapped EPIPE should be broken pipe")
	}
}

func TestRenderJobLog_MixedJSONAndPlainText(t *testing.T) {
	// Non-JSON lines after JSON events should be preserved (e.g.
	// agent stderr/diagnostics mixed with JSONL stream output).
	input := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}`,
		`stderr: warning something`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Done"}]}}`,
		`exit status 0`,
	}, "\n")

	var buf bytes.Buffer
	err := renderJobLog(strings.NewReader(input), &buf, false)
	if err != nil {
		t.Fatalf("renderJobLog: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Hello") {
		t.Errorf("expected first text, got:\n%s", out)
	}
	if !strings.Contains(out, "stderr: warning something") {
		t.Errorf("non-JSON after JSON should be preserved, got:\n%s", out)
	}
	if !strings.Contains(out, "Done") {
		t.Errorf("expected second text, got:\n%s", out)
	}
	if !strings.Contains(out, "exit status 0") {
		t.Errorf("trailing non-JSON should be preserved, got:\n%s", out)
	}

	// Verify ordering: "Hello" before "stderr", "stderr" before "Done".
	helloIdx := strings.Index(out, "Hello")
	stderrIdx := strings.Index(out, "stderr: warning")
	doneIdx := strings.Index(out, "Done")
	exitIdx := strings.Index(out, "exit status 0")
	if helloIdx >= stderrIdx || stderrIdx >= doneIdx || doneIdx >= exitIdx {
		t.Errorf("output order wrong: Hello@%d stderr@%d Done@%d exit@%d",
			helloIdx, stderrIdx, doneIdx, exitIdx)
	}
}

func TestLogCmd_InvalidJobID(t *testing.T) {
	cmd := logCmd()
	cmd.SetArgs([]string{"abc"})
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-numeric job ID")
	}
	if !strings.Contains(err.Error(), "invalid job ID") {
		t.Fatalf("expected 'invalid job ID' error, got: %s", err)
	}
}

func TestLogCmd_MissingLogFile(t *testing.T) {
	t.Setenv("ROBOREV_DATA_DIR", t.TempDir())
	cmd := logCmd()
	cmd.SetArgs([]string{"99999"})
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing log file")
	}
	if !strings.Contains(err.Error(), "no log for job") {
		t.Fatalf("expected 'no log for job' error, got: %s", err)
	}
}

func TestLogCmd_PathFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", dir)

	var buf bytes.Buffer
	cmd := logCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--path", "42"})
	cmd.SilenceUsage = true
	// --path succeeds even if log file doesn't exist.
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("--path should succeed: %v", err)
	}

	out := strings.TrimSpace(buf.String())
	want := filepath.Join(dir, "logs", "jobs", "42.log")
	if out != want {
		t.Errorf("--path output = %q, want %q", out, want)
	}
}

func TestLogCmd_RawFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", dir)

	// Create a log file at the expected path.
	logDir := filepath.Join(dir, "logs", "jobs")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "42.log")
	rawContent := `{"type":"assistant"}` + "\n"
	os.WriteFile(logPath, []byte(rawContent), 0644)

	var buf bytes.Buffer
	cmd := logCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--raw", "42"})
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("--raw should succeed: %v", err)
	}

	if buf.String() != rawContent {
		t.Errorf(
			"--raw output = %q, want %q",
			buf.String(), rawContent,
		)
	}
}

func TestLooksLikeJSON(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`{"type":"assistant"}`, true},
		{`  {"type":"tool"}`, true},
		{`not json`, false},
		{``, false},
		{`[1,2,3]`, false},
		{`{invalid}`, false},
		// Valid JSON but no "type" field — should NOT match
		{`{"foo":"bar"}`, false},
		// Empty type — should NOT match
		{`{"type":""}`, false},
	}
	for _, tt := range tests {
		got := looksLikeJSON(tt.input)
		if got != tt.want {
			t.Errorf("looksLikeJSON(%q) = %v, want %v",
				tt.input, got, tt.want)
		}
	}
}

func TestRenderJobLog_SanitizesControlChars(t *testing.T) {
	// Non-JSON lines with ANSI escapes and OSC sequences
	// should be sanitized to prevent terminal spoofing.
	input := strings.Join([]string{
		"\x1b[31mred text\x1b[0m",
		"\x1b]0;evil title\x07",
		"clean line",
	}, "\n")

	var buf bytes.Buffer
	err := renderJobLog(strings.NewReader(input), &buf, true)
	if err != nil {
		t.Fatalf("renderJobLog: %v", err)
	}

	out := buf.String()

	// ANSI escape sequences should be stripped.
	if strings.Contains(out, "\x1b[") {
		t.Errorf("ANSI escape not stripped: %q", out)
	}
	if strings.Contains(out, "\x1b]") {
		t.Errorf("OSC escape not stripped: %q", out)
	}
	if strings.Contains(out, "\x07") {
		t.Errorf("BEL char not stripped: %q", out)
	}

	// Clean content should still be present.
	if !strings.Contains(out, "red text") {
		t.Errorf("expected 'red text' in output: %q", out)
	}
	if !strings.Contains(out, "clean line") {
		t.Errorf("expected 'clean line' in output: %q", out)
	}
}

func TestRenderJobLog_SanitizeMixedJSONAndControl(t *testing.T) {
	// JSON lines should NOT be sanitized (they go through
	// streamFormatter), only non-JSON lines.
	input := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`,
		"\x1b[1mbold agent stderr\x1b[0m",
	}, "\n")

	var buf bytes.Buffer
	err := renderJobLog(strings.NewReader(input), &buf, true)
	if err != nil {
		t.Fatalf("renderJobLog: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "ok") {
		t.Errorf("JSON text should be rendered: %q", out)
	}
	if !strings.Contains(out, "bold agent stderr") {
		t.Errorf("non-JSON text should be present: %q", out)
	}
	// The non-JSON line's ANSI should be stripped.
	if strings.Contains(out, "\x1b[1m") {
		t.Errorf("ANSI in non-JSON line not stripped: %q", out)
	}
}

func TestRenderJobLog_PreservesBlankLinesInMixedLog(t *testing.T) {
	// Blank lines between non-JSON stderr lines should be preserved
	// even after JSON events have been seen.
	input := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
		"stderr line 1",
		"",
		"stderr line 2",
	}, "\n")

	var buf bytes.Buffer
	err := renderJobLog(strings.NewReader(input), &buf, true)
	if err != nil {
		t.Fatalf("renderJobLog: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "stderr line 1") {
		t.Errorf("missing stderr line 1: %q", out)
	}
	if !strings.Contains(out, "stderr line 2") {
		t.Errorf("missing stderr line 2: %q", out)
	}
	// The blank line between the two stderr lines should be
	// preserved (two consecutive newlines in the output).
	if !strings.Contains(out, "stderr line 1\n\n") {
		t.Errorf(
			"blank line between stderr lines dropped: %q",
			out,
		)
	}
}
