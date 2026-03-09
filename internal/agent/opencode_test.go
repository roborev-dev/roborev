package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		wantModel    bool
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

	assert.Equal(t, strings.TrimSpace(stdin), prompt, "unexpected condition")

	assertNotContains(t, args, prompt)
}

func TestOpenCodeReviewParsesJSONStream(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
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
	}

	result, _, _ := runMockOpenCodeReview(t, "", "prompt", stdoutLines)

	assertEqual(t, result, " Done.")

	assertNotContains(t, result, "Read")
	assertNotContains(t, result, "file_path")
}

func TestOpenCodeReviewPrefersFinalPostToolSegment(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeTextEvent("## Review Findings\n- **Severity**: Low; **Problem**: Earlier provisional finding."),
		makeOpenCodeEvent("tool", map[string]any{
			"type": "tool",
			"tool": "Read",
		}),
		makeTextEvent("## Review Findings\n- **Severity**: Medium; **Problem**: Final persisted finding."),
	}

	result, _, _ := runMockOpenCodeReview(t, "", "prompt", stdoutLines)

	assertEqual(t, result, "## Review Findings\n- **Severity**: Medium; **Problem**: Final persisted finding.")
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
	require.NoError(t, err, "Review failed: %v")

	assertEqual(t, result, "Hello world")

	outStr := outputBuf.String()
	assertContains(t, outStr, `"type":"text"`)
}

func TestOpenCodeReviewPartialOnError(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	stdoutLines := []string{
		makeTextEvent("Partial review text"),
	}

	_, _, _, err := executeReviewTest(t, reviewTestOpts{
		MockOpts: MockCLIOpts{
			CaptureStdin: true,
			StdoutLines:  stdoutLines,
			ExitCode:     1,
		},
		Prompt: "prompt",
	})
	require.Error(t, err)

	assertContains(t, err.Error(), "Partial review text")
}

func TestOpenCodeReviewNoOutput(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

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

	assertEqual(t, result, "No review output generated")
}

func TestParseOpenCodeJSON(t *testing.T) {
	t.Parallel()

	lines := strings.Join([]string{
		makeTextEvent("Part one."),
		makeOpenCodeEvent("tool", map[string]any{
			"type": "tool", "tool": "Read",
		}),
		makeTextEvent(" Part two."),
	}, "\n") + "\n"

	var outputBuf bytes.Buffer
	result, err := parseOpenCodeJSON(
		strings.NewReader(lines), newSyncWriter(&outputBuf),
	)
	require.NoError(t, err, "parseOpenCodeJSON: %v")

	assertEqual(t, result, " Part two.")

	out := outputBuf.String()
	assert.Equal(t, 3, strings.Count(out, "\n"), "expected 3 lines written to output, got:\n%s", out)

}

func TestParseOpenCodeJSON_NilOutput(t *testing.T) {
	t.Parallel()

	lines := makeTextEvent("ok") + "\n"

	result, err := parseOpenCodeJSON(
		strings.NewReader(lines), nil,
	)
	require.NoError(t, err, "parseOpenCodeJSON: %v")

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
	require.NoError(t, err, "Review failed: %v")

	assertEqual(t, result, "Review done")

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
	require.NoError(t, err, "Review with nil output: %v")

	assertEqual(t, result, "Review content")
}

func TestParseOpenCodeJSON_ReadError(t *testing.T) {
	t.Parallel()

	result, err := parseOpenCodeJSON(
		&failAfterReader{
			data: makeTextEvent("partial") + "\n",
		},
		nil,
	)
	require.Error(t, err)

	assertContains(t, result, "partial")
}

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

type reviewTestOpts struct {
	MockOpts  MockCLIOpts
	Model     string
	Prompt    string
	Writer    io.Writer
	SessionID string
}

func executeReviewTest(t *testing.T, opts reviewTestOpts) (string, string, string, error) {
	t.Helper()

	if opts.Prompt == "" {
		require.NotEmpty(t, opts.Prompt, "executeReviewTest requires an explicit Prompt")
	}

	mock := mockAgentCLI(t, opts.MockOpts)

	a := NewOpenCodeAgent(mock.CmdPath)
	if opts.Model != "" {
		a.Model = opts.Model
	}
	if opts.SessionID != "" {
		a = a.WithSessionID(opts.SessionID).(*OpenCodeAgent)
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

func runMockOpenCodeReview(
	t *testing.T, model, prompt string,
	stdoutLines []string,
) (output, args, stdin string) {
	t.Helper()

	if stdoutLines == nil {
		stdoutLines = []string{
			makeTextEvent("ok"),
		}
	}

	out, argsStr, stdinStr, err := executeReviewTest(t, reviewTestOpts{
		MockOpts: MockCLIOpts{
			CaptureArgs:  true,
			CaptureStdin: true,
			StdoutLines:  stdoutLines,
		},
		Model:  model,
		Prompt: prompt,
	})
	require.NoError(t, err, "Review failed: %v")

	return out, argsStr, stdinStr
}

func TestOpenCodeReviewSessionFlag(t *testing.T) {
	t.Parallel()
	skipIfWindows(t)

	_, args, _ := func() (string, string, string) {
		out, argsStr, stdinStr, err := executeReviewTest(t, reviewTestOpts{
			MockOpts: MockCLIOpts{
				CaptureArgs:  true,
				CaptureStdin: true,
				StdoutLines:  []string{makeTextEvent("ok")},
			},
			Prompt:    "prompt",
			SessionID: "ses_123",
		})
		require.NoError(t, err)
		return out, argsStr, stdinStr
	}()

	assertContains(t, args, "--session")
	assertContains(t, args, "ses_123")
}

func readFileOrFatal(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read file %s: %v", path)

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

	lines := makeTextEvent("\x1b[31mred\x1b[0m and \x1b]0;evil\x07safe") + "\n"

	result, err := parseOpenCodeJSON(
		strings.NewReader(lines), nil,
	)
	require.NoError(t, err, "parseOpenCodeJSON: %v")

	assert.NotContains(t, result, "\x1b", "unexpected condition")
	assert.NotContains(t, result, "\x07", "unexpected condition")
	assertContains(t, result, "red")
	assertContains(t, result, "safe")
	assertNotContains(t, result, "evil")
}

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

func makeTextEvent(text string) string {
	return makeOpenCodeEvent("text", map[string]any{
		"type": "text",
		"text": text,
	})
}
