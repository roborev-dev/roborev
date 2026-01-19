package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	// Should return the sentinel error
	if !errors.Is(err, errNoStreamJSON) {
		t.Fatalf("expected errNoStreamJSON, got %v", err)
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
	sw := newSyncWriter(&output)
	result, _, err := a.parseStreamJSON(strings.NewReader(input), sw)
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
	// Should be the sentinel error (allows fallback)
	if !errors.Is(err, errNoStreamJSON) {
		t.Fatalf("expected errNoStreamJSON, got %v", err)
	}
	// Raw output should be available for fallback
	if !strings.Contains(rawOutput, "This is a plain text review") {
		t.Fatalf("expected raw output to contain plain text, got %q", rawOutput)
	}
}

func TestGeminiReview_PlainTextFallback(t *testing.T) {
	// End-to-end test: create a temp script that emits plain text (no stream-json)
	// and verify Review() falls back to raw output
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-gemini")

	// Create a script that outputs plain text
	script := `#!/bin/sh
echo "Plain text review output"
echo "No issues found."
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	result, err := a.Review(context.Background(), tmpDir, "abc123", "Review this code", &output)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Should return the raw plain text output
	if !strings.Contains(result, "Plain text review output") {
		t.Errorf("expected result to contain plain text, got %q", result)
	}
	if !strings.Contains(result, "No issues found") {
		t.Errorf("expected result to contain 'No issues found', got %q", result)
	}
}

func TestGeminiReview_StreamJSON(t *testing.T) {
	// End-to-end test: create a temp script that emits valid stream-json
	// and verify Review() parses it correctly
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-gemini")

	// Create a script that outputs stream-json
	script := `#!/bin/sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"result","result":"Review complete. All good!"}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	result, err := a.Review(context.Background(), tmpDir, "abc123", "Review this code", &output)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Should return the parsed result
	if result != "Review complete. All good!" {
		t.Errorf("expected parsed result, got %q", result)
	}
}

func TestGeminiReview_IOError(t *testing.T) {
	// End-to-end test: verify that non-sentinel errors (like command failure) are propagated
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-gemini")

	// Create a script that fails
	script := `#!/bin/sh
echo "Error message" >&2
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	_, err := a.Review(context.Background(), tmpDir, "abc123", "Review this code", &output)
	if err == nil {
		t.Fatal("expected error for failed command, got nil")
	}
	// Should contain information about the failure
	if !strings.Contains(err.Error(), "gemini failed") {
		t.Errorf("expected error to mention 'gemini failed', got %v", err)
	}
}

func TestGeminiParseStreamJSON_PreservesIndentation(t *testing.T) {
	a := NewGeminiAgent("gemini")
	// Plain text with indentation (like code blocks)
	input := `Review output:
    def hello():
        print("world")

No issues found.
`
	_, rawOutput, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if !errors.Is(err, errNoStreamJSON) {
		t.Fatalf("expected errNoStreamJSON, got %v", err)
	}
	// Indentation should be preserved
	if !strings.Contains(rawOutput, "    def hello():") {
		t.Errorf("expected indentation to be preserved, got %q", rawOutput)
	}
	if !strings.Contains(rawOutput, "        print") {
		t.Errorf("expected nested indentation to be preserved, got %q", rawOutput)
	}
	// Empty line should be preserved
	if !strings.Contains(rawOutput, ")\n\nNo issues") {
		t.Errorf("expected blank line to be preserved, got %q", rawOutput)
	}
}

func TestGeminiReview_PromptDeliveredViaStdin(t *testing.T) {
	// End-to-end test: verify the prompt is actually delivered via stdin
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-gemini")
	promptFile := filepath.Join(tmpDir, "received-prompt.txt")

	// Create a script that reads stdin and writes it to a file
	script := `#!/bin/sh
cat > "` + promptFile + `"
echo '{"type":"result","result":"Done"}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	expectedPrompt := "Please review this code for security issues"
	_, err := a.Review(context.Background(), tmpDir, "abc123", expectedPrompt, &output)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Verify the prompt was received
	receivedPrompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("failed to read prompt file: %v", err)
	}
	if string(receivedPrompt) != expectedPrompt {
		t.Errorf("prompt not delivered correctly: expected %q, got %q", expectedPrompt, string(receivedPrompt))
	}
}
