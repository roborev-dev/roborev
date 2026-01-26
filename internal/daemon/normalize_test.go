package daemon

import (
	"testing"
)

func TestNormalizeClaudeOutput_AssistantMessage(t *testing.T) {
	line := `{"type":"assistant","message":{"content":"Hello, I will review this code."}}`
	result := NormalizeClaudeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "Hello, I will review this code." {
		t.Errorf("expected message content, got %q", result.Text)
	}
	if result.Type != "text" {
		t.Errorf("expected type 'text', got %q", result.Type)
	}
}

func TestNormalizeClaudeOutput_Result(t *testing.T) {
	line := `{"type":"result","result":"Review complete: PASS"}`
	result := NormalizeClaudeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "Review complete: PASS" {
		t.Errorf("expected result text, got %q", result.Text)
	}
}

func TestNormalizeClaudeOutput_ToolUse(t *testing.T) {
	line := `{"type":"tool_use","name":"Read","input":{"file_path":"/foo/bar.go"}}`
	result := NormalizeClaudeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "[Tool: Read]" {
		t.Errorf("expected tool indicator, got %q", result.Text)
	}
	if result.Type != "tool" {
		t.Errorf("expected type 'tool', got %q", result.Type)
	}
}

func TestNormalizeClaudeOutput_ToolResult(t *testing.T) {
	line := `{"type":"tool_result","content":"file contents here"}`
	result := NormalizeClaudeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "[Tool completed]" {
		t.Errorf("expected tool completed indicator, got %q", result.Text)
	}
}

func TestNormalizeClaudeOutput_SkipsLifecycleEvents(t *testing.T) {
	cases := []string{
		`{"type":"message_start"}`,
		`{"type":"message_delta"}`,
		`{"type":"message_stop"}`,
		`{"type":"content_block_start"}`,
		`{"type":"content_block_stop"}`,
	}

	for _, line := range cases {
		result := NormalizeClaudeOutput(line)
		if result != nil {
			t.Errorf("expected nil for %s, got %+v", line, result)
		}
	}
}

func TestNormalizeClaudeOutput_EmptyAssistant(t *testing.T) {
	line := `{"type":"assistant","message":{}}`
	result := NormalizeClaudeOutput(line)

	if result != nil {
		t.Errorf("expected nil for empty assistant message, got %+v", result)
	}
}

func TestNormalizeClaudeOutput_SystemInit(t *testing.T) {
	line := `{"type":"system","subtype":"init","session_id":"abc123def456"}`
	result := NormalizeClaudeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "[Session: abc123de...]" {
		t.Errorf("expected truncated session ID, got %q", result.Text)
	}
}

func TestNormalizeClaudeOutput_SystemInitShortSessionID(t *testing.T) {
	// Test that short session IDs don't panic
	line := `{"type":"system","subtype":"init","session_id":"abc"}`
	result := NormalizeClaudeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "[Session: abc...]" {
		t.Errorf("expected short session ID preserved, got %q", result.Text)
	}
}

func TestNormalizeClaudeOutput_InvalidJSON(t *testing.T) {
	line := "not json at all"
	result := NormalizeClaudeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result for non-JSON")
	}
	if result.Text != "not json at all" {
		t.Errorf("expected raw text, got %q", result.Text)
	}
}

func TestNormalizeClaudeOutput_EmptyLine(t *testing.T) {
	result := NormalizeClaudeOutput("")
	if result != nil {
		t.Errorf("expected nil for empty line, got %+v", result)
	}

	result = NormalizeClaudeOutput("   ")
	if result != nil {
		t.Errorf("expected nil for whitespace line, got %+v", result)
	}
}

func TestNormalizeOpenCodeOutput_PlainText(t *testing.T) {
	line := "Reviewing the code changes..."
	result := NormalizeOpenCodeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "Reviewing the code changes..." {
		t.Errorf("expected plain text, got %q", result.Text)
	}
	if result.Type != "text" {
		t.Errorf("expected type 'text', got %q", result.Type)
	}
}

func TestNormalizeOpenCodeOutput_StripsANSI(t *testing.T) {
	line := "\x1b[32mGreen text\x1b[0m"
	result := NormalizeOpenCodeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "Green text" {
		t.Errorf("expected ANSI stripped, got %q", result.Text)
	}
}

func TestNormalizeOpenCodeOutput_FilterToolCall(t *testing.T) {
	line := `{"name":"read","arguments":{"path":"/foo"}}`
	result := NormalizeOpenCodeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "[Tool call]" {
		t.Errorf("expected tool call indicator, got %q", result.Text)
	}
	if result.Type != "tool" {
		t.Errorf("expected type 'tool', got %q", result.Type)
	}
}

func TestNormalizeOpenCodeOutput_PreservesJSONWithExtraKeys(t *testing.T) {
	// JSON with more than just name/arguments should be preserved
	line := `{"name":"test","arguments":{},"extra":"key"}`
	result := NormalizeOpenCodeOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Should not be filtered as tool call
	if result.Type == "tool" {
		t.Error("expected non-tool type for JSON with extra keys")
	}
}

func TestNormalizeOpenCodeOutput_EmptyLine(t *testing.T) {
	result := NormalizeOpenCodeOutput("")
	if result != nil {
		t.Errorf("expected nil for empty line, got %+v", result)
	}
}

func TestNormalizeGenericOutput_PlainText(t *testing.T) {
	line := "Some agent output"
	result := NormalizeGenericOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "Some agent output" {
		t.Errorf("expected plain text, got %q", result.Text)
	}
}

func TestNormalizeGenericOutput_StripsANSI(t *testing.T) {
	line := "\x1b[1;31mBold red\x1b[0m normal"
	result := NormalizeGenericOutput(line)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Text != "Bold red normal" {
		t.Errorf("expected ANSI stripped, got %q", result.Text)
	}
}

func TestGetNormalizer(t *testing.T) {
	cases := []struct {
		agent    string
		expected string
	}{
		{"claude-code", "claude"},
		{"opencode", "opencode"},
		{"codex", "generic"},
		{"gemini", "generic"},
		{"unknown", "generic"},
	}

	for _, tc := range cases {
		normalizer := GetNormalizer(tc.agent)
		if normalizer == nil {
			t.Errorf("GetNormalizer(%q) returned nil", tc.agent)
		}
	}
}

func TestStripANSI(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"plain text", "plain text"},
		{"\x1b[32mgreen\x1b[0m", "green"},
		{"\x1b[1;31;40mcomplex\x1b[0m", "complex"},
		{"\x1b[?25hcursor\x1b[?25l", "cursor"},
		{"no escape", "no escape"},
		{"\x1b]0;title\x07text", "text"}, // OSC sequence
	}

	for _, tc := range cases {
		result := stripANSI(tc.input)
		if result != tc.expected {
			t.Errorf("stripANSI(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestIsToolCallJSON(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{`{"name":"read","arguments":{}}`, true},
		{`{"name":"write","arguments":{"path":"/foo"}}`, true},
		{`{"name":"test","arguments":{},"extra":true}`, false}, // extra key
		{`{"name":"only"}`, false},                             // missing arguments
		{`{"arguments":{}}`, false},                            // missing name
		{`not json`, false},
		{`{"other":"object"}`, false},
	}

	for _, tc := range cases {
		result := isToolCallJSON(tc.input)
		if result != tc.expected {
			t.Errorf("isToolCallJSON(%q) = %v, want %v", tc.input, result, tc.expected)
		}
	}
}
