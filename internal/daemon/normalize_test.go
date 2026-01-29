package daemon

import (
	"testing"
)

type normalizeTestCase struct {
	name        string
	input       string
	wantNil     bool
	wantText    string
	wantType    string
	notWantType string
}

func runNormalizeTests(t *testing.T, fn func(string) *OutputLine, cases []normalizeTestCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := fn(tc.input)
			if tc.wantNil {
				if result != nil {
					t.Errorf("expected nil, got %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if result.Text != tc.wantText {
				t.Errorf("text = %q, want %q", result.Text, tc.wantText)
			}
			if tc.wantType != "" && result.Type != tc.wantType {
				t.Errorf("type = %q, want %q", result.Type, tc.wantType)
			}
			if tc.notWantType != "" && result.Type == tc.notWantType {
				t.Errorf("type = %q, should not be %q", result.Type, tc.notWantType)
			}
		})
	}
}

func TestNormalizeClaudeOutput(t *testing.T) {
	runNormalizeTests(t, NormalizeClaudeOutput, []normalizeTestCase{
		{
			name:     "AssistantMessage",
			input:    `{"type":"assistant","message":{"content":"Hello, I will review this code."}}`,
			wantText: "Hello, I will review this code.",
			wantType: "text",
		},
		{
			name:     "Result",
			input:    `{"type":"result","result":"Review complete: PASS"}`,
			wantText: "Review complete: PASS",
			wantType: "text",
		},
		{
			name:     "ToolUse",
			input:    `{"type":"tool_use","name":"Read","input":{"file_path":"/foo/bar.go"}}`,
			wantText: "[Tool: Read]",
			wantType: "tool",
		},
		{
			name:     "ToolResult",
			input:    `{"type":"tool_result","content":"file contents here"}`,
			wantText: "[Tool completed]",
			wantType: "tool",
		},
		{
			name:     "SystemInit",
			input:    `{"type":"system","subtype":"init","session_id":"abc123def456"}`,
			wantText: "[Session: abc123de...]",
		},
		{
			name:     "SystemInitShortSessionID",
			input:    `{"type":"system","subtype":"init","session_id":"abc"}`,
			wantText: "[Session: abc...]",
		},
		{
			name:     "InvalidJSON",
			input:    "not json at all",
			wantText: "not json at all",
		},
		{
			name:    "EmptyAssistant",
			input:   `{"type":"assistant","message":{}}`,
			wantNil: true,
		},
		{
			name:    "EmptyLine",
			input:   "",
			wantNil: true,
		},
		{
			name:    "WhitespaceLine",
			input:   "   ",
			wantNil: true,
		},
	})
}

func TestNormalizeClaudeOutput_SkipsLifecycleEvents(t *testing.T) {
	for _, line := range []string{
		`{"type":"message_start"}`,
		`{"type":"message_delta"}`,
		`{"type":"message_stop"}`,
		`{"type":"content_block_start"}`,
		`{"type":"content_block_stop"}`,
	} {
		if result := NormalizeClaudeOutput(line); result != nil {
			t.Errorf("expected nil for %s, got %+v", line, result)
		}
	}
}

func TestNormalizeOpenCodeOutput(t *testing.T) {
	runNormalizeTests(t, NormalizeOpenCodeOutput, []normalizeTestCase{
		{
			name:     "PlainText",
			input:    "Reviewing the code changes...",
			wantText: "Reviewing the code changes...",
			wantType: "text",
		},
		{
			name:     "StripsANSI",
			input:    "\x1b[32mGreen text\x1b[0m",
			wantText: "Green text",
		},
		{
			name:     "FilterToolCall",
			input:    `{"name":"read","arguments":{"path":"/foo"}}`,
			wantText: "[Tool call]",
			wantType: "tool",
		},
		{
			name:        "PreservesJSONWithExtraKeys",
			input:       `{"name":"test","arguments":{},"extra":"key"}`,
			wantText:    `{"name":"test","arguments":{},"extra":"key"}`,
			notWantType: "tool",
		},
		{
			name:    "EmptyLine",
			input:   "",
			wantNil: true,
		},
	})
}

func TestNormalizeGenericOutput(t *testing.T) {
	runNormalizeTests(t, NormalizeGenericOutput, []normalizeTestCase{
		{
			name:     "PlainText",
			input:    "Some agent output",
			wantText: "Some agent output",
		},
		{
			name:     "StripsANSI",
			input:    "\x1b[1;31mBold red\x1b[0m normal",
			wantText: "Bold red normal",
		},
	})
}

func TestGetNormalizer(t *testing.T) {
	cases := []struct {
		agent    string
		wantName string
	}{
		{"claude-code", "NormalizeClaudeOutput"},
		{"opencode", "NormalizeOpenCodeOutput"},
		{"codex", "NormalizeGenericOutput"},
		{"gemini", "NormalizeGenericOutput"},
		{"unknown", "NormalizeGenericOutput"},
	}
	for _, tc := range cases {
		fn := GetNormalizer(tc.agent)
		if fn == nil {
			t.Errorf("GetNormalizer(%q) returned nil", tc.agent)
			continue
		}
		// Verify correct normalizer by testing characteristic behavior.
		// NormalizeClaudeOutput parses Claude JSON; NormalizeOpenCodeOutput
		// detects tool-call JSON; NormalizeGenericOutput passes through text.
		switch tc.wantName {
		case "NormalizeClaudeOutput":
			result := fn(`{"type":"assistant","message":{"content":"hi"}}`)
			if result == nil || result.Text != "hi" {
				t.Errorf("GetNormalizer(%q) returned wrong normalizer: expected NormalizeClaudeOutput", tc.agent)
			}
		case "NormalizeOpenCodeOutput":
			result := fn(`{"name":"read","arguments":{}}`)
			if result == nil || result.Text != "[Tool call]" {
				t.Errorf("GetNormalizer(%q) returned wrong normalizer: expected NormalizeOpenCodeOutput", tc.agent)
			}
		case "NormalizeGenericOutput":
			// Generic normalizer treats Claude JSON as plain text, not parsed messages.
			result := fn(`{"type":"assistant","message":{"content":"hi"}}`)
			if result == nil || result.Text == "hi" {
				t.Errorf("GetNormalizer(%q) returned wrong normalizer: expected NormalizeGenericOutput", tc.agent)
			}
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
		{"\x1b]0;title\x07text", "text"},
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
		{`{"name":"test","arguments":{},"extra":true}`, false},
		{`{"name":"only"}`, false},
		{`{"arguments":{}}`, false},
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
