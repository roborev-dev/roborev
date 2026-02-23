package main

import (
	"bytes"
	"strings"
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
	}
	for _, tt := range tests {
		got := looksLikeJSON(tt.input)
		if got != tt.want {
			t.Errorf("looksLikeJSON(%q) = %v, want %v",
				tt.input, got, tt.want)
		}
	}
}
