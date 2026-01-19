package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestClaudeBuildArgs(t *testing.T) {
	a := NewClaudeAgent("claude")

	// Non-agentic mode (review only): read-only tools, no dangerous flag
	args := a.buildArgs(false)
	argsStr := strings.Join(args, " ")
	if containsString(args, claudeDangerousFlag) {
		t.Fatalf("expected no unsafe flag in review mode, got %v", args)
	}
	if !containsString(args, "--output-format") || !containsString(args, "stream-json") {
		t.Fatalf("expected --output-format stream-json, got %v", args)
	}
	if !containsString(args, "--verbose") {
		t.Fatalf("expected --verbose (required for stream-json), got %v", args)
	}
	if !containsString(args, "-p") {
		t.Fatalf("expected -p flag (for stdin piping), got %v", args)
	}
	if !containsString(args, "--allowedTools") {
		t.Fatalf("expected --allowedTools, got %v", args)
	}
	// Review mode should have read-only tools (no Edit, Write, or Bash)
	if strings.Contains(argsStr, "Edit") || strings.Contains(argsStr, "Write") || strings.Contains(argsStr, "Bash") {
		t.Fatalf("expected read-only tools in review mode (no Edit/Write/Bash), got %v", args)
	}

	// Agentic mode: write tools + dangerous flag
	args = a.buildArgs(true)
	argsStr = strings.Join(args, " ")
	if !containsString(args, claudeDangerousFlag) {
		t.Fatalf("expected unsafe flag in agentic mode, got %v", args)
	}
	if !containsString(args, "--allowedTools") {
		t.Fatalf("expected --allowedTools in agentic mode, got %v", args)
	}
	// Agentic mode should have write tools
	if !strings.Contains(argsStr, "Edit") || !strings.Contains(argsStr, "Write") {
		t.Fatalf("expected write tools in agentic mode, got %v", args)
	}
}

func TestClaudeSupportsDangerousFlagAllowsNonZeroHelp(t *testing.T) {
	cmdPath := writeTempCommand(t, "#!/bin/sh\necho \"usage "+claudeDangerousFlag+"\"; exit 1\n")

	supported, err := claudeSupportsDangerousFlag(context.Background(), cmdPath)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !supported {
		t.Fatalf("expected dangerous flag support, got false")
	}
}

func TestClaudeReviewUnsafeMissingFlagErrors(t *testing.T) {
	t.Cleanup(func() { SetAllowUnsafeAgents(false) })

	cmdPath := writeTempCommand(t, "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then echo \"usage\"; exit 0; fi\nexit 0\n")
	SetAllowUnsafeAgents(true)

	a := NewClaudeAgent(cmdPath)
	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("expected unsupported flag error, got %v", err)
	}
}


func TestParseStreamJSON_ResultEvent(t *testing.T) {
	a := NewClaudeAgent("claude")
	input := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"Working on it..."}}
{"type":"result","result":"Done! Created the file."}
`
	result, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Done! Created the file." {
		t.Fatalf("expected result from result event, got %q", result)
	}
}

func TestParseStreamJSON_AssistantFallback(t *testing.T) {
	a := NewClaudeAgent("claude")
	// No result event, should fall back to assistant messages
	input := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"First message"}}
{"type":"assistant","message":{"content":"Second message"}}
`
	result, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "First message\nSecond message" {
		t.Fatalf("expected joined assistant messages, got %q", result)
	}
}

func TestParseStreamJSON_MalformedLines(t *testing.T) {
	a := NewClaudeAgent("claude")
	// Mix of valid JSON and malformed lines
	input := `{"type":"system","subtype":"init"}
not valid json
{"type":"result","result":"Success"}
also not json
`
	result, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Success" {
		t.Fatalf("expected result despite malformed lines, got %q", result)
	}
}

func TestParseStreamJSON_NoValidEvents(t *testing.T) {
	a := NewClaudeAgent("claude")
	input := `not json at all
still not json
`
	_, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for no valid events, got nil")
	}
	if !strings.Contains(err.Error(), "no valid stream-json events") {
		t.Fatalf("expected 'no valid events' error, got %v", err)
	}
}

func TestParseStreamJSON_EmptyInput(t *testing.T) {
	a := NewClaudeAgent("claude")
	_, err := a.parseStreamJSON(strings.NewReader(""), nil)
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
	if !strings.Contains(err.Error(), "no valid stream-json events") {
		t.Fatalf("expected 'no valid events' error, got %v", err)
	}
}

func TestParseStreamJSON_StreamsToOutput(t *testing.T) {
	a := NewClaudeAgent("claude")
	input := `{"type":"system","subtype":"init"}
{"type":"result","result":"Done"}
`
	var output bytes.Buffer
	result, err := a.parseStreamJSON(strings.NewReader(input), &output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Done" {
		t.Fatalf("expected 'Done', got %q", result)
	}
	// Output should contain streamed JSON
	if output.Len() == 0 {
		t.Fatal("expected output to be written")
	}
}

func TestAnthropicAPIKey(t *testing.T) {
	// Clear any existing key
	SetAnthropicAPIKey("")
	t.Cleanup(func() { SetAnthropicAPIKey("") })

	// Initially should be empty
	if key := AnthropicAPIKey(); key != "" {
		t.Fatalf("expected empty API key, got %q", key)
	}

	// Set a key
	SetAnthropicAPIKey("test-api-key")
	if key := AnthropicAPIKey(); key != "test-api-key" {
		t.Fatalf("expected 'test-api-key', got %q", key)
	}

	// Clear the key
	SetAnthropicAPIKey("")
	if key := AnthropicAPIKey(); key != "" {
		t.Fatalf("expected empty API key after clear, got %q", key)
	}
}

func TestFilterEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/home/test",
		"ANTHROPIC_API_KEY=secret-key",
		"OTHER_VAR=value",
	}

	filtered := filterEnv(env, "ANTHROPIC_API_KEY")

	// Should have 3 items (ANTHROPIC_API_KEY removed)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 env vars, got %d: %v", len(filtered), filtered)
	}

	// Verify ANTHROPIC_API_KEY is not present
	for _, e := range filtered {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY=") {
			t.Fatalf("ANTHROPIC_API_KEY should be filtered out, got %v", filtered)
		}
	}

	// Verify other vars are present
	found := make(map[string]bool)
	for _, e := range filtered {
		if strings.HasPrefix(e, "PATH=") {
			found["PATH"] = true
		}
		if strings.HasPrefix(e, "HOME=") {
			found["HOME"] = true
		}
		if strings.HasPrefix(e, "OTHER_VAR=") {
			found["OTHER_VAR"] = true
		}
	}
	if !found["PATH"] || !found["HOME"] || !found["OTHER_VAR"] {
		t.Fatalf("missing expected env vars in filtered result: %v", filtered)
	}
}
