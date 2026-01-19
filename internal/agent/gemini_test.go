package agent

import (
	"bytes"
	"strings"
	"testing"
)

func TestGeminiBuildArgs(t *testing.T) {
	a := NewGeminiAgent("gemini")

	// Non-agentic mode (review only): read-only tools, no yolo
	args := a.buildArgs(false)
	argsStr := strings.Join(args, " ")
	if containsString(args, "--yolo") {
		t.Fatalf("expected no --yolo in review mode, got %v", args)
	}
	if !containsString(args, "--output-format") || !containsString(args, "stream-json") {
		t.Fatalf("expected --output-format stream-json, got %v", args)
	}
	if !containsString(args, "--allowed-tools") {
		t.Fatalf("expected --allowed-tools, got %v", args)
	}
	// Review mode should have read-only tools (no Edit, Write, Bash, or Shell)
	if strings.Contains(argsStr, "Edit") || strings.Contains(argsStr, "Write") ||
		strings.Contains(argsStr, "Bash") || strings.Contains(argsStr, "Shell") {
		t.Fatalf("expected read-only tools in review mode (no Edit/Write/Bash/Shell), got %v", args)
	}

	// Agentic mode: write tools + yolo
	args = a.buildArgs(true)
	argsStr = strings.Join(args, " ")
	if !containsString(args, "--yolo") {
		t.Fatalf("expected --yolo in agentic mode, got %v", args)
	}
	if !containsString(args, "--allowed-tools") {
		t.Fatalf("expected --allowed-tools in agentic mode, got %v", args)
	}
	// Agentic mode should have write tools
	if !strings.Contains(argsStr, "Edit") || !strings.Contains(argsStr, "Write") {
		t.Fatalf("expected write tools in agentic mode, got %v", args)
	}
}

func TestGeminiParseStreamJSON_ResultEvent(t *testing.T) {
	a := NewGeminiAgent("gemini")
	input := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"Working on it..."}}
{"type":"result","result":"Done! Review complete."}
`
	result, _, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Done! Review complete." {
		t.Fatalf("expected result from result event, got %q", result)
	}
}

func TestGeminiParseStreamJSON_AssistantFallback(t *testing.T) {
	a := NewGeminiAgent("gemini")
	// No result event, should fall back to assistant messages
	input := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"First message"}}
{"type":"assistant","message":{"content":"Second message"}}
`
	result, _, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "First message\nSecond message" {
		t.Fatalf("expected joined assistant messages, got %q", result)
	}
}

func TestGeminiParseStreamJSON_NoValidEvents(t *testing.T) {
	a := NewGeminiAgent("gemini")
	input := `not json at all
still not json
`
	_, rawOutput, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for no valid events, got nil")
	}
	if !strings.Contains(err.Error(), "no valid stream-json events") {
		t.Fatalf("expected 'no valid events' error, got %v", err)
	}
	// Should return raw output for fallback
	if !strings.Contains(rawOutput, "not json at all") {
		t.Fatalf("expected raw output to contain input text, got %q", rawOutput)
	}
}

func TestGeminiParseStreamJSON_StreamsToOutput(t *testing.T) {
	a := NewGeminiAgent("gemini")
	input := `{"type":"system","subtype":"init"}
{"type":"result","result":"Done"}
`
	var output bytes.Buffer
	result, _, err := a.parseStreamJSON(strings.NewReader(input), &output)
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

func TestGeminiParseStreamJSON_EmptyResult(t *testing.T) {
	a := NewGeminiAgent("gemini")
	// Valid JSON but no result or assistant content
	input := `{"type":"system","subtype":"init"}
{"type":"tool","name":"Read"}
`
	result, _, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return empty string (caller handles sentinel)
	if result != "" {
		t.Fatalf("expected empty result, got %q", result)
	}
}

func TestGeminiParseStreamJSON_PlainTextFallback(t *testing.T) {
	a := NewGeminiAgent("gemini")
	// Simulate older Gemini CLI that outputs plain text
	input := `This is a plain text review.
No issues found in the code.
`
	_, rawOutput, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for plain text input")
	}
	// Raw output should be available for fallback
	if !strings.Contains(rawOutput, "This is a plain text review") {
		t.Fatalf("expected raw output to contain plain text, got %q", rawOutput)
	}
}
