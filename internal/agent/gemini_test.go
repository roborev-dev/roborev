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

func TestTruncateStderr(t *testing.T) {
	// Short string - no truncation
	short := "short stderr"
	if got := truncateStderr(short); got != short {
		t.Errorf("expected no truncation for short string, got %q", got)
	}

	// Exactly at limit - no truncation
	exact := strings.Repeat("x", maxStderrLen)
	if got := truncateStderr(exact); got != exact {
		t.Errorf("expected no truncation at exact limit, got len=%d", len(got))
	}

	// Over limit - should truncate
	over := strings.Repeat("x", maxStderrLen+100)
	got := truncateStderr(over)
	if !strings.HasSuffix(got, "... (truncated)") {
		t.Errorf("expected truncation suffix, got %q", got)
	}
	if len(got) != maxStderrLen+len("... (truncated)") {
		t.Errorf("expected truncated length %d, got %d", maxStderrLen+len("... (truncated)"), len(got))
	}
}

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
	parsed, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.result != "Done! Review complete." {
		t.Fatalf("expected result from result event, got %q", parsed.result)
	}
}

func TestGeminiParseStreamJSON_GeminiMessageFormat(t *testing.T) {
	// Test actual Gemini CLI format: type="message", role="assistant", content at top level
	a := NewGeminiAgent("gemini")
	input := `{"type":"message","timestamp":"2026-01-19T17:49:13.445Z","role":"assistant","content":"Changes:\n- Created file.ts","delta":true}
{"type":"message","timestamp":"2026-01-19T17:49:13.447Z","role":"assistant","content":" with filtering logic.","delta":true}
{"type":"result","timestamp":"2026-01-19T17:49:13.519Z","status":"success","stats":{"total_tokens":1000}}
`
	parsed, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should concatenate the assistant message contents
	if !strings.Contains(parsed.result, "Changes:") {
		t.Errorf("expected result to contain 'Changes:', got %q", parsed.result)
	}
	if !strings.Contains(parsed.result, "filtering logic") {
		t.Errorf("expected result to contain 'filtering logic', got %q", parsed.result)
	}
}

func TestGeminiParseStreamJSON_AssistantFallback(t *testing.T) {
	a := NewGeminiAgent("gemini")
	// No result event, should fall back to assistant messages
	input := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":"First message"}}
{"type":"assistant","message":{"content":"Second message"}}
`
	parsed, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.result != "First message\nSecond message" {
		t.Fatalf("expected joined assistant messages, got %q", parsed.result)
	}
}

func TestGeminiParseStreamJSON_NoValidEvents(t *testing.T) {
	a := NewGeminiAgent("gemini")
	input := `not json at all
still not json
`
	_, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for no valid events, got nil")
	}
	// Should return the sentinel error
	if !errors.Is(err, errNoStreamJSON) {
		t.Fatalf("expected errNoStreamJSON, got %v", err)
	}
}

func TestGeminiParseStreamJSON_StreamsToOutput(t *testing.T) {
	a := NewGeminiAgent("gemini")
	input := `{"type":"system","subtype":"init"}
{"type":"result","result":"Done"}
`
	var output bytes.Buffer
	sw := newSyncWriter(&output)
	parsed, err := a.parseStreamJSON(strings.NewReader(input), sw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.result != "Done" {
		t.Fatalf("expected 'Done', got %q", parsed.result)
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
	parsed, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return empty string (caller returns "No review output generated")
	if parsed.result != "" {
		t.Fatalf("expected empty result, got %q", parsed.result)
	}
}

func TestGeminiParseStreamJSON_PlainTextError(t *testing.T) {
	a := NewGeminiAgent("gemini")
	// Simulate older Gemini CLI that outputs plain text - should error
	input := `This is a plain text review.
No issues found in the code.
`
	_, err := a.parseStreamJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for plain text input")
	}
	// Should be the sentinel error
	if !errors.Is(err, errNoStreamJSON) {
		t.Fatalf("expected errNoStreamJSON, got %v", err)
	}
}

func TestGeminiReview_PlainTextError(t *testing.T) {
	skipIfWindows(t)
	// End-to-end test: create a temp script that emits plain text (no stream-json)
	// should return an error since we require stream-json
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
	_, err := a.Review(context.Background(), tmpDir, "abc123", "Review this code", &output)
	if err == nil {
		t.Fatal("expected error for plain text output, got nil")
	}
	// Should return actionable error message
	if !strings.Contains(err.Error(), "stream-json") {
		t.Errorf("expected error to mention stream-json, got %v", err)
	}
	// Should preserve sentinel for errors.Is
	if !errors.Is(err, errNoStreamJSON) {
		t.Errorf("expected errors.Is(err, errNoStreamJSON) to be true, got false")
	}
}

func TestGeminiReview_PlainTextErrorWithStderr(t *testing.T) {
	skipIfWindows(t)
	// End-to-end test: verify stderr is included in the error and truncated when large
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-gemini")

	// Create a script that outputs plain text and stderr
	script := `#!/bin/sh
echo "Plain text review output"
echo "Some stderr message" >&2
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	_, err := a.Review(context.Background(), tmpDir, "abc123", "Review this code", &output)
	if err == nil {
		t.Fatal("expected error for plain text output, got nil")
	}
	// Should include stderr in error message
	if !strings.Contains(err.Error(), "Some stderr message") {
		t.Errorf("expected error to include stderr, got %v", err)
	}
}

func TestGeminiReview_LargeStderrTruncation(t *testing.T) {
	skipIfWindows(t)
	// End-to-end test: verify large stderr is truncated
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-gemini")

	// Create a script that outputs a lot of stderr
	script := `#!/bin/sh
echo "Plain text"
for i in $(seq 1 200); do
	echo "This is a long stderr line number $i that will contribute to the total size" >&2
done
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	a := NewGeminiAgent(scriptPath)
	var output bytes.Buffer
	_, err := a.Review(context.Background(), tmpDir, "abc123", "Review this code", &output)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Should be truncated
	if !strings.Contains(err.Error(), "... (truncated)") {
		t.Errorf("expected error to indicate truncation, got %v", err)
	}
}

func TestGeminiReview_StreamJSON(t *testing.T) {
	skipIfWindows(t)
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

func TestGeminiReview_StreamJSONNoResult(t *testing.T) {
	skipIfWindows(t)
	// End-to-end test: stream-json with only tool events (no result/assistant)
	// should return "No review output generated" (no raw output fallback)
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-gemini")

	// Create a script that outputs stream-json with only tool events
	script := `#!/bin/sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"tool","name":"Read","input":{"path":"foo.go"}}'
echo '{"type":"tool_result","content":"file contents here"}'
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

	// Should return "No review output generated" since no result/assistant content
	if result != "No review output generated" {
		t.Errorf("expected 'No review output generated', got %q", result)
	}
}

func TestGeminiReview_IOError(t *testing.T) {
	skipIfWindows(t)
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

func TestGeminiReview_PromptDeliveredViaStdin(t *testing.T) {
	skipIfWindows(t)
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
