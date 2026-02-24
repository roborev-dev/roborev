package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestOpenCodeModelFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		model        string
		wantModel    bool // true = --model should appear
		wantContains string
	}{
		{
			name:      "no model omits flag",
			model:     "",
			wantModel: false,
		},
		{
			name:         "explicit model includes flag",
			model:        "anthropic/claude-sonnet-4-20250514",
			wantModel:    true,
			wantContains: "anthropic/claude-sonnet-4-20250514",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewOpenCodeAgent("opencode")
			a.Model = tt.model
			cl := a.CommandLine()
			assertContains(t, cl, "--format json")
			if tt.wantModel {
				assertContains(t, cl, "--model")
				assertContains(t, cl, tt.wantContains)
			} else {
				assertNotContains(t, cl, "--model")
			}
		})
	}
}

func TestOpenCodeReviewModelFlag(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)
	tests := []struct {
		name      string
		model     string
		wantFlag  bool
		wantModel string
	}{
		{
			name:     "no model omits --model from args",
			model:    "",
			wantFlag: false,
		},
		{
			name:      "explicit model passes --model to subprocess",
			model:     "openai/gpt-4o",
			wantFlag:  true,
			wantModel: "openai/gpt-4o",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, args, _ := runMockOpenCodeReview(
				t, tt.model, "review this", nil,
			)
			args = strings.TrimSpace(args)

			assertContains(t, args, "--format json")
			if tt.wantFlag {
				assertContains(t, args, "--model")
				assertContains(t, args, tt.wantModel)
			} else {
				assertNotContains(t, args, "--model")
			}
		})
	}
}

func TestOpenCodeReviewPipesPromptViaStdin(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	prompt := "Review this commit carefully"
	_, args, stdin := runMockOpenCodeReview(t, "", prompt, nil)

	// Prompt must be in stdin
	if strings.TrimSpace(stdin) != prompt {
		t.Errorf("stdin content = %q, want %q", stdin, prompt)
	}

	// Prompt must not be in argv
	assertNotContains(t, args, prompt)
}

func TestOpenCodeReviewParsesJSONStream(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeOpenCodeEvent("step_start", map[string]any{
			"type": "step-start",
		}),
		makeOpenCodeEvent("text", map[string]any{
			"type": "text",
			"text": "**Review:** Fix the typo.",
		}),
		makeOpenCodeEvent("tool", map[string]any{
			"type": "tool",
			"tool": "Read",
			"state": map[string]any{
				"status": "running",
				"input": map[string]any{
					"file_path": "/foo/bar.go",
				},
			},
		}),
		makeOpenCodeEvent("text", map[string]any{
			"type": "text",
			"text": " Done.",
		}),
		makeOpenCodeEvent("step_finish", map[string]any{
			"type":   "step-finish",
			"reason": "stop",
		}),
	}

	result, _, _ := runMockOpenCodeReview(t, "", "prompt", stdoutLines)

	assertContains(t, result, "**Review:** Fix the typo.")
	assertContains(t, result, " Done.")
	// Tool events should not appear in the result text
	assertNotContains(t, result, "Read")
	assertNotContains(t, result, "file_path")
}

func TestOpenCodeReviewStreamsToOutput(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeOpenCodeEvent("text", map[string]any{
			"type": "text",
			"text": "Hello world",
		}),
	}

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureStdin: true,
		StdoutLines:  stdoutLines,
	})

	a := NewOpenCodeAgent(mock.CmdPath)

	var outputBuf bytes.Buffer
	result, err := a.Review(
		context.Background(), t.TempDir(),
		"HEAD", "prompt", &outputBuf,
	)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	if result != "Hello world" {
		t.Errorf("result = %q, want %q", result, "Hello world")
	}

	// Raw JSONL should have been written to the output writer
	outStr := outputBuf.String()
	assertContains(t, outStr, `"type":"text"`)
}

func TestOpenCodeReviewPartialOnError(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeOpenCodeEvent("text", map[string]any{
			"type": "text",
			"text": "Partial review text",
		}),
	}

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureStdin: true,
		StdoutLines:  stdoutLines,
		ExitCode:     1,
	})

	a := NewOpenCodeAgent(mock.CmdPath)

	_, err := a.Review(
		context.Background(), t.TempDir(),
		"HEAD", "prompt", nil,
	)
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	// Error should contain partial output
	assertContains(t, err.Error(), "Partial review text")
}

func TestOpenCodeReviewNoOutput(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	// Events with no text parts should produce fallback message
	stdoutLines := []string{
		makeOpenCodeEvent("step_start", map[string]any{
			"type": "step-start",
		}),
		makeOpenCodeEvent("step_finish", map[string]any{
			"type":   "step-finish",
			"reason": "stop",
		}),
	}

	result, _, _ := runMockOpenCodeReview(t, "", "prompt", stdoutLines)

	if result != "No review output generated" {
		t.Errorf(
			"result = %q, want %q",
			result, "No review output generated",
		)
	}
}

func TestParseOpenCodeJSON(t *testing.T) {
	t.Parallel()

	lines := strings.Join([]string{
		makeOpenCodeEvent("text", map[string]any{
			"type": "text", "text": "Part one.",
		}),
		makeOpenCodeEvent("reasoning", map[string]any{
			"type": "reasoning", "text": "thinking...",
		}),
		makeOpenCodeEvent("text", map[string]any{
			"type": "text", "text": " Part two.",
		}),
	}, "\n") + "\n"

	var outputBuf bytes.Buffer
	result, err := parseOpenCodeJSON(
		strings.NewReader(lines), newSyncWriter(&outputBuf),
	)
	if err != nil {
		t.Fatalf("parseOpenCodeJSON: %v", err)
	}

	if result != "Part one. Part two." {
		t.Errorf("result = %q, want %q", result, "Part one. Part two.")
	}

	// All raw lines should be written to output
	out := outputBuf.String()
	if strings.Count(out, "\n") != 3 {
		t.Errorf("expected 3 lines written to output, got:\n%s", out)
	}
}

func TestParseOpenCodeJSON_NilOutput(t *testing.T) {
	t.Parallel()

	lines := makeOpenCodeEvent("text", map[string]any{
		"type": "text", "text": "ok",
	}) + "\n"

	result, err := parseOpenCodeJSON(
		strings.NewReader(lines), nil,
	)
	if err != nil {
		t.Fatalf("parseOpenCodeJSON: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

func TestOpenCodeReviewStderrStreamedToOutput(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeOpenCodeEvent("text", map[string]any{
			"type": "text", "text": "Review done",
		}),
	}

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureStdin: true,
		StdoutLines:  stdoutLines,
		StderrLines:  []string{"warning: something"},
	})

	a := NewOpenCodeAgent(mock.CmdPath)

	var outputBuf bytes.Buffer
	result, err := a.Review(
		context.Background(), t.TempDir(),
		"HEAD", "prompt", &outputBuf,
	)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	if result != "Review done" {
		t.Errorf("result = %q, want %q", result, "Review done")
	}

	// Both stdout JSONL and stderr should appear in output
	outStr := outputBuf.String()
	assertContains(t, outStr, `"type":"text"`)
	assertContains(t, outStr, "warning: something")
}

func TestOpenCodeReviewNilOutput(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeOpenCodeEvent("text", map[string]any{
			"type": "text", "text": "Review content",
		}),
	}

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureStdin: true,
		StdoutLines:  stdoutLines,
	})

	a := NewOpenCodeAgent(mock.CmdPath)

	result, err := a.Review(
		context.Background(), t.TempDir(),
		"HEAD", "prompt", nil,
	)
	if err != nil {
		t.Fatalf("Review with nil output: %v", err)
	}

	if result != "Review content" {
		t.Errorf("result = %q, want %q", result, "Review content")
	}
}

func TestParseOpenCodeJSON_ReadError(t *testing.T) {
	t.Parallel()

	result, err := parseOpenCodeJSON(
		&failAfterReader{
			data: makeOpenCodeEvent("text", map[string]any{
				"type": "text", "text": "partial",
			}) + "\n",
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error from broken reader")
	}
	// Should contain partial text
	assertContains(t, result, "partial")
}

// failAfterReader returns data on first read, then an error.
type failAfterReader struct {
	data string
	done bool
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.ErrUnexpectedEOF
	}
	r.done = true
	n := copy(p, r.data)
	return n, nil
}

func runMockOpenCodeReview(
	t *testing.T, model, prompt string,
	stdoutLines []string,
) (output, args, stdin string) {
	t.Helper()

	if stdoutLines == nil {
		stdoutLines = []string{
			makeOpenCodeEvent("text", map[string]any{
				"type": "text", "text": "ok",
			}),
		}
	}

	mock := mockAgentCLI(t, MockCLIOpts{
		CaptureArgs:  true,
		CaptureStdin: true,
		StdoutLines:  stdoutLines,
	})

	a := NewOpenCodeAgent(mock.CmdPath)
	if model != "" {
		a.Model = model
	}

	out, err := a.Review(
		context.Background(), t.TempDir(),
		"HEAD", prompt, nil,
	)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	argsBytes := readFileOrFatal(t, mock.ArgsFile)
	stdinBytes := readFileOrFatal(t, mock.StdinFile)

	return out, string(argsBytes), string(stdinBytes)
}

func readFileOrFatal(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file %s: %v", path, err)
	}
	return data
}

func TestStripTerminalControls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"ANSI color stripped", "\x1b[31mred\x1b[0m text", "red text"},
		{"OSC title stripped", "\x1b]0;evil\x07safe", "safe"},
		{"BEL removed", "bell\x07here", "bellhere"},
		{"newlines preserved", "line1\nline2", "line1\nline2"},
		{"CRLF normalized", "a\r\nb\rc", "a\nb\nc"},
		{"tabs preserved", "col1\tcol2", "col1\tcol2"},
		{"null byte removed", "a\x00b", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripTerminalControls(tt.input)
			if got != tt.want {
				t.Errorf(
					"stripTerminalControls(%q) = %q, want %q",
					tt.input, got, tt.want,
				)
			}
		})
	}
}

func TestParseOpenCodeJSON_SanitizesControlChars(t *testing.T) {
	t.Parallel()

	lines := makeOpenCodeEvent("text", map[string]any{
		"type": "text",
		"text": "\x1b[31mred\x1b[0m and \x1b]0;evil\x07safe",
	}) + "\n"

	result, err := parseOpenCodeJSON(
		strings.NewReader(lines), nil,
	)
	if err != nil {
		t.Fatalf("parseOpenCodeJSON: %v", err)
	}

	if strings.Contains(result, "\x1b") {
		t.Errorf("result contains ESC: %q", result)
	}
	if strings.Contains(result, "\x07") {
		t.Errorf("result contains BEL: %q", result)
	}
	assertContains(t, result, "red")
	assertContains(t, result, "safe")
	assertNotContains(t, result, "evil")
}

// makeOpenCodeEvent builds an opencode JSONL event line.
func makeOpenCodeEvent(eventType string, part map[string]any) string {
	ev := map[string]any{
		"type": eventType,
		"part": part,
	}
	b, err := json.Marshal(ev)
	if err != nil {
		panic("makeOpenCodeEvent: " + err.Error())
	}
	return string(b)
}
