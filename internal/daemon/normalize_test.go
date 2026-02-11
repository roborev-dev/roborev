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
		{"codex", "NormalizeCodexOutput"},
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
		case "NormalizeCodexOutput":
			// Codex normalizer parses item.completed agent_message events.
			result := fn(`{"type":"item.completed","item":{"type":"agent_message","text":"hello"}}`)
			if result == nil || result.Text != "hello" {
				t.Errorf("GetNormalizer(%q) returned wrong normalizer: expected NormalizeCodexOutput", tc.agent)
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

func TestNormalizeCodexOutput(t *testing.T) {
	runNormalizeTests(t, NormalizeCodexOutput, []normalizeTestCase{
		{
			name:     "AgentMessage",
			input:    `{"type":"item.completed","item":{"type":"agent_message","text":"Review complete."}}`,
			wantText: "Review complete.",
			wantType: "text",
		},
		{
			name:     "AgentMessageMultiline",
			input:    `{"type":"item.completed","item":{"type":"agent_message","text":"Line 1\nLine 2"}}`,
			wantText: "Line 1 Line 2",
			wantType: "text",
		},
		{
			name:     "AgentMessageStripsANSI",
			input:    `{"type":"item.completed","item":{"type":"agent_message","text":"\u001b[31mred\u001b[0m text"}}`,
			wantText: "red text",
			wantType: "text",
		},
		{
			name:     "AgentMessageStripsControlChars",
			input:    `{"type":"item.completed","item":{"type":"agent_message","text":"hello\u0007world"}}`,
			wantText: "helloworld",
			wantType: "text",
		},
		{
			name:     "CommandStartedStripsANSI",
			input:    `{"type":"item.started","item":{"type":"command_execution","command":"bash -lc \u001b[32mls\u001b[0m"}}`,
			wantText: "[Command: bash -lc ls]",
			wantType: "tool",
		},
		{
			name:     "CommandCompletedStripsControlChars",
			input:    `{"type":"item.completed","item":{"type":"command_execution","command":"ls\u0007\u001b[31m -la"}}`,
			wantText: "[Command: ls -la]",
			wantType: "tool",
		},
		{
			name:     "CommandStarted",
			input:    `{"type":"item.started","item":{"type":"command_execution","command":"bash -lc ls"}}`,
			wantText: "[Command: bash -lc ls]",
			wantType: "tool",
		},
		{
			name:     "CommandCompleted",
			input:    `{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc ls"}}`,
			wantText: "[Command: bash -lc ls]",
			wantType: "tool",
		},
		{
			name:     "CommandCompletedNoCommand",
			input:    `{"type":"item.completed","item":{"type":"command_execution"}}`,
			wantText: "[Command completed]",
			wantType: "tool",
		},
		{
			name:    "CommandUpdatedNoCommand",
			input:   `{"type":"item.updated","item":{"type":"command_execution"}}`,
			wantNil: true,
		},
		{
			name:    "CommandUpdatedWithCommand",
			input:   `{"type":"item.updated","item":{"type":"command_execution","command":"bash -lc ls"}}`,
			wantNil: true,
		},
		{
			name:    "FileChangeUpdated",
			input:   `{"type":"item.updated","item":{"type":"file_change"}}`,
			wantNil: true,
		},
		{
			name:     "FileChange",
			input:    `{"type":"item.completed","item":{"type":"file_change"}}`,
			wantText: "[File change]",
			wantType: "tool",
		},
		{
			name:    "ThreadStarted",
			input:   `{"type":"thread.started","thread_id":"abc123"}`,
			wantNil: true,
		},
		{
			name:    "TurnStarted",
			input:   `{"type":"turn.started"}`,
			wantNil: true,
		},
		{
			name:    "TurnCompleted",
			input:   `{"type":"turn.completed","usage":{"input_tokens":100}}`,
			wantNil: true,
		},
		{
			name:     "TurnFailed",
			input:    `{"type":"turn.failed","error":{"message":"something broke"}}`,
			wantText: "[Error in stream]",
			wantType: "error",
		},
		{
			name:     "StreamError",
			input:    `{"type":"error","message":"stream error"}`,
			wantText: "[Error in stream]",
			wantType: "error",
		},
		{
			name:    "EmptyLine",
			input:   "",
			wantNil: true,
		},
		{
			name:     "NonJSON",
			input:    "some plain text output",
			wantText: "some plain text output",
			wantType: "text",
		},
		{
			name:     "NonJSONStripsControlChars",
			input:    "text with \x07bell and \x1b[31mcolor\x1b[0m",
			wantText: "text with bell and color",
			wantType: "text",
		},
	})
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
