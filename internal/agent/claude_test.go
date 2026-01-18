package agent

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestClaudeBuildArgsUnsafeOptIn(t *testing.T) {
	t.Cleanup(func() { SetAllowUnsafeAgents(false) })

	a := NewClaudeAgent("claude")

	SetAllowUnsafeAgents(false)
	args := a.buildArgs(false)
	if containsString(args, claudeDangerousFlag) {
		t.Fatalf("expected no unsafe flag when disabled, got %v", args)
	}
	if !containsString(args, "--print") {
		t.Fatalf("expected --print in non-agentic mode, got %v", args)
	}

	SetAllowUnsafeAgents(true)
	args = a.buildArgs(true)
	if !containsString(args, claudeDangerousFlag) {
		t.Fatalf("expected unsafe flag when enabled, got %v", args)
	}
	if containsString(args, "--print") {
		t.Fatalf("expected no --print in agentic mode, got %v", args)
	}
	if !containsString(args, "--output-format") || !containsString(args, "stream-json") {
		t.Fatalf("expected --output-format stream-json in agentic mode, got %v", args)
	}
	if !containsString(args, "--verbose") {
		t.Fatalf("expected --verbose in agentic mode (required for stream-json), got %v", args)
	}
	if !containsString(args, "-p") {
		t.Fatalf("expected -p flag in agentic mode (for stdin piping), got %v", args)
	}
	if !containsString(args, "--permission-mode") || !containsString(args, "bypassPermissions") {
		t.Fatalf("expected --permission-mode bypassPermissions in agentic mode, got %v", args)
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

func TestClaudeReviewNonTTYErrorsWhenUnsafeDisabled(t *testing.T) {
	prevAllowUnsafe := AllowUnsafeAgents()
	SetAllowUnsafeAgents(false)
	t.Cleanup(func() { SetAllowUnsafeAgents(prevAllowUnsafe) })

	stdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = stdin
		r.Close()
		w.Close()
	})

	a := NewClaudeAgent("claude")
	_, err = a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "requires a TTY") {
		t.Fatalf("expected TTY error, got %v", err)
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
