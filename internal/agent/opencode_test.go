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
			_, args, _, err := executeReviewTest(t, reviewTestOpts{
				MockOpts: MockCLIOpts{
					CaptureArgs:  true,
					CaptureStdin: true,
					StdoutLines:  []string{makeTextEvent("ok")},
				},
				Model:  tt.model,
				Prompt: "review this",
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

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
	_, args, stdin, err := executeReviewTest(t, reviewTestOpts{
		MockOpts: MockCLIOpts{
			CaptureArgs:  true,
			CaptureStdin: true,
			StdoutLines:  []string{makeTextEvent("ok")},
		},
		Prompt: prompt,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Prompt must be in stdin
	if strings.TrimSpace(stdin) != prompt {
		t.Errorf("stdin content = %q, want %q", stdin, prompt)
	}

	// Prompt must not be in argv
	assertNotContains(t, args, prompt)
}

func TestOpenCodeReviewBehaviors(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	tests := []struct {
		name         string
		opts         reviewTestOpts
		wantResult   string
		wantContains []string
		wantNotText  []string
		wantErr      string
	}{
		{
			name: "parses JSON stream",
			opts: reviewTestOpts{
				Prompt: "prompt",
				MockOpts: MockCLIOpts{
					CaptureArgs:  true,
					CaptureStdin: true,
					StdoutLines: []string{
						makeOpenCodeEvent("step_start", map[string]any{
							"type": "step-start",
						}),
						makeTextEvent("**Review:** Fix the typo."),
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
						makeTextEvent(" Done."),
						makeOpenCodeEvent("step_finish", map[string]any{
							"type":   "step-finish",
							"reason": "stop",
						}),
					},
				},
			},
			wantContains: []string{"**Review:** Fix the typo.", " Done."},
			wantNotText:  []string{"Read", "file_path"},
		},
		{
			name: "partial on error",
			opts: reviewTestOpts{
				Prompt: "prompt",
				MockOpts: MockCLIOpts{
					CaptureStdin: true,
					StdoutLines:  []string{makeTextEvent("Partial review text")},
					ExitCode:     1,
				},
			},
			wantErr: "Partial review text",
		},
		{
			name: "no output",
			opts: reviewTestOpts{
				Prompt: "prompt",
				MockOpts: MockCLIOpts{
					CaptureArgs:  true,
					CaptureStdin: true,
					StdoutLines: []string{
						makeOpenCodeEvent("step_start", map[string]any{
							"type": "step-start",
						}),
						makeOpenCodeEvent("step_finish", map[string]any{
							"type":   "step-finish",
							"reason": "stop",
						}),
					},
				},
			},
			wantResult: "No review output generated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, _, _, err := executeReviewTest(t, tt.opts)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				assertContains(t, err.Error(), tt.wantErr)
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.wantResult != "" {
					assertEqual(t, result, tt.wantResult)
				}
				for _, substr := range tt.wantContains {
					assertContains(t, result, substr)
				}
				for _, substr := range tt.wantNotText {
					assertNotContains(t, result, substr)
				}
			}
		})
	}
}

func TestOpenCodeReviewStreamsToOutput(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeTextEvent("Hello world"),
	}

	var outputBuf bytes.Buffer
	result, _, _, err := executeReviewTest(t, reviewTestOpts{
		MockOpts: MockCLIOpts{
			CaptureStdin: true,
			StdoutLines:  stdoutLines,
		},
		Prompt: "prompt",
		Writer: &outputBuf,
	})
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	assertEqual(t, result, "Hello world")

	// Raw JSONL should have been written to the output writer
	outStr := outputBuf.String()
	assertContains(t, outStr, `"type":"text"`)
}

func TestParseOpenCodeJSON(t *testing.T) {
	t.Parallel()

	lines := makeJSONLines(
		makeTextEvent("Part one."),
		makeOpenCodeEvent("reasoning", map[string]any{
			"type": "reasoning", "text": "thinking...",
		}),
		makeTextEvent(" Part two."),
	)

	var outputBuf bytes.Buffer
	result, err := parseOpenCodeJSON(
		strings.NewReader(lines), newSyncWriter(&outputBuf),
	)
	if err != nil {
		t.Fatalf("parseOpenCodeJSON: %v", err)
	}

	assertEqual(t, result, "Part one. Part two.")

	// All raw lines should be written to output
	out := outputBuf.String()
	if strings.Count(out, "\n") != 3 {
		t.Errorf("expected 3 lines written to output, got:\n%s", out)
	}
}

func TestParseOpenCodeJSON_NilOutput(t *testing.T) {
	t.Parallel()

	lines := makeJSONLines(makeTextEvent("ok"))

	result, err := parseOpenCodeJSON(
		strings.NewReader(lines), nil,
	)
	if err != nil {
		t.Fatalf("parseOpenCodeJSON: %v", err)
	}
	assertEqual(t, result, "ok")
}

func TestOpenCodeReviewStderrStreamedToOutput(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeTextEvent("Review done"),
	}

	var outputBuf bytes.Buffer
	result, _, _, err := executeReviewTest(t, reviewTestOpts{
		MockOpts: MockCLIOpts{
			CaptureStdin: true,
			StdoutLines:  stdoutLines,
			StderrLines:  []string{"warning: something"},
		},
		Prompt: "prompt",
		Writer: &outputBuf,
	})
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	assertEqual(t, result, "Review done")

	// Both stdout JSONL and stderr should appear in output
	outStr := outputBuf.String()
	assertContains(t, outStr, `"type":"text"`)
	assertContains(t, outStr, "warning: something")
}

func TestOpenCodeReviewNilOutput(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeTextEvent("Review content"),
	}

	result, _, _, err := executeReviewTest(t, reviewTestOpts{
		MockOpts: MockCLIOpts{
			CaptureStdin: true,
			StdoutLines:  stdoutLines,
		},
		Prompt: "prompt",
	})
	if err != nil {
		t.Fatalf("Review with nil output: %v", err)
	}

	assertEqual(t, result, "Review content")
}

func TestParseOpenCodeJSON_ReadError(t *testing.T) {
	t.Parallel()

	result, err := parseOpenCodeJSON(
		&failAfterReader{
			data: makeJSONLines(makeTextEvent("partial")),
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

// reviewTestOpts configures executeReviewTest.
type reviewTestOpts struct {
	MockOpts MockCLIOpts
	Model    string
	Prompt   string
	Writer   io.Writer
}

// executeReviewTest handles mockAgentCLI setup, instantiation, and Review execution.
func executeReviewTest(t *testing.T, opts reviewTestOpts) (string, string, string, error) {
	t.Helper()

	if opts.Prompt == "" {
		t.Fatal("executeReviewTest requires an explicit Prompt")
	}

	mock := mockAgentCLI(t, opts.MockOpts)

	a := NewOpenCodeAgent(mock.CmdPath)
	if opts.Model != "" {
		a.Model = opts.Model
	}

	out, err := a.Review(
		context.Background(), t.TempDir(),
		"HEAD", opts.Prompt, opts.Writer,
	)

	var argsBytes, stdinBytes []byte
	if opts.MockOpts.CaptureArgs {
		argsBytes = readFileOrFatal(t, mock.ArgsFile)
	}
	if opts.MockOpts.CaptureStdin {
		stdinBytes = readFileOrFatal(t, mock.StdinFile)
	}

	return out, string(argsBytes), string(stdinBytes), err
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
			assertEqual(t, got, tt.want)
		})
	}
}

func TestParseOpenCodeJSON_SanitizesControlChars(t *testing.T) {
	t.Parallel()

	lines := makeJSONLines(makeTextEvent("\x1b[31mred\x1b[0m and \x1b]0;evil\x07safe"))

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

// makeJSONLines joins JSON strings into a properly formatted JSONL payload.
func makeJSONLines(events ...string) string {
	if len(events) == 0 {
		return ""
	}
	return strings.Join(events, "\n") + "\n"
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

// makeTextEvent is a convenience helper for making 'text' events.
func makeTextEvent(text string) string {
	return makeOpenCodeEvent("text", map[string]any{
		"type": "text",
		"text": text,
	})
}
