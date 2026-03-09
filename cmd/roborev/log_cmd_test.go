package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/roborev-dev/roborev/internal/streamfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("toJSON: %v", err))
	}
	return string(b)
}

func executeRenderJobLog(t *testing.T, input string, isTTY bool) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, streamfmt.RenderLog(strings.NewReader(input), &buf, isTTY), "RenderLog")
	return buf.String()
}

func assertLogContains(t *testing.T, got, want string) {
	t.Helper()
	assert.Contains(t, got, want)
}

func assertLogNotContains(t *testing.T, got, want string) {
	t.Helper()
	assert.NotContains(t, got, want)
}

func TestLogCleanCmd_NegativeDays(t *testing.T) {
	cmd := logCleanCmd()
	cmd.SetArgs([]string{"--days", "-1"})
	err := cmd.Execute()
	require.Error(t, err, "expected error for negative --days")
}

func TestLogCleanCmd_OverflowDays(t *testing.T) {
	cmd := logCleanCmd()
	cmd.SetArgs([]string{"--days", "999999"})
	err := cmd.Execute()
	require.Error(t, err, "expected error for oversized --days")
}

func TestRenderJobLog_JSONL(t *testing.T) {
	// Simulate Claude Code JSONL output
	input := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"abc123"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Reviewing the code."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`,
		`{"type":"tool_result","content":"file contents"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looks good overall."}]}}`,
	}, "\n")

	out := streamfmt.StripANSI(executeRenderJobLog(t, input, true))

	// Should contain the text messages
	assertLogContains(t, out, "Reviewing the code.")
	assertLogContains(t, out, "Read")
	assertLogContains(t, out, "main.go")

	// Should NOT contain raw JSON
	assertLogNotContains(t, out, `"type"`)
}

func TestRenderJobLog_Behaviors(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		isJSON      bool
		wantContain []string
		wantExclude []string
		wantEmpty   bool
	}{
		{
			name:        "PlainText",
			input:       "line 1\nline 2\nline 3\n",
			isJSON:      true,
			wantContain: []string{"line 1", "line 3"},
		},
		{
			name:      "Empty",
			input:     "",
			isJSON:    true,
			wantEmpty: true,
		},
		{
			name:        "OversizedLine",
			input:       fmt.Sprintf(`{"type":"tool_result","content":"%s"}`, strings.Repeat("x", 2*1024*1024)),
			isJSON:      true,
			wantContain: []string{}, // Just checking it doesn't error
		},
		{
			name:        "PlainTextPreservesBlankLines",
			input:       "line 1\n\nline 3\n",
			isJSON:      true,
			wantContain: []string{"line 1\n\nline 3"},
		},
		{
			name:        "SanitizesControlChars",
			input:       "\x1b[31mred text\x1b[0m\n\x1b]0;evil title\x07\nclean line",
			isJSON:      true,
			wantContain: []string{"red text", "clean line"},
			wantExclude: []string{"\x1b[", "\x1b]", "\x07"},
		},
		{
			name: "SanitizeMixedJSONAndControl",
			input: strings.Join([]string{
				`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`,
				"\x1b[1mbold agent stderr\x1b[0m",
			}, "\n"),
			isJSON:      true,
			wantContain: []string{"ok", "bold agent stderr"},
			wantExclude: []string{"\x1b[1m"},
		},
		{
			name: "PreservesBlankLinesInMixedLog",
			input: strings.Join([]string{
				`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
				"stderr line 1",
				"",
				"stderr line 2",
			}, "\n"),
			isJSON:      true,
			wantContain: []string{"stderr line 1\n\n", "stderr line 2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := executeRenderJobLog(t, tt.input, tt.isJSON)
			if tt.wantEmpty {
				assert.Empty(t, out)
			}
			for _, want := range tt.wantContain {
				assertLogContains(t, out, want)
			}
			for _, exclude := range tt.wantExclude {
				assertLogNotContains(t, out, exclude)
			}
		})
	}
}

func TestIsBrokenPipe(t *testing.T) {
	assert := assert.New(t)
	assert.False(isBrokenPipe(nil), "nil should not be broken pipe")
	assert.False(isBrokenPipe(fmt.Errorf("other error")), "non-EPIPE error should not be broken pipe")
	assert.True(isBrokenPipe(syscall.EPIPE), "bare EPIPE should be broken pipe")
	assert.True(isBrokenPipe(fmt.Errorf("write: %w", syscall.EPIPE)), "wrapped EPIPE should be broken pipe")
}

func TestRenderJobLog_MixedJSONAndPlainText(t *testing.T) {
	assert := assert.New(t)

	// Non-JSON lines after JSON events should be preserved (e.g.
	// agent stderr/diagnostics mixed with JSONL stream output).
	input := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}`,
		`stderr: warning something`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Done"}]}}`,
		`exit status 0`,
	}, "\n")

	out := executeRenderJobLog(t, input, false)

	assertLogContains(t, out, "Hello")
	assertLogContains(t, out, "stderr: warning something")
	assertLogContains(t, out, "Done")
	assertLogContains(t, out, "exit status 0")

	// Verify ordering: "Hello" before "stderr", "stderr" before "Done".
	helloIdx := strings.Index(out, "Hello")
	stderrIdx := strings.Index(out, "stderr: warning")
	doneIdx := strings.Index(out, "Done")
	exitIdx := strings.Index(out, "exit status 0")
	assert.True(helloIdx < stderrIdx && stderrIdx < doneIdx && doneIdx < exitIdx, "output order wrong: Hello@%d stderr@%d Done@%d exit@%d",
		helloIdx, stderrIdx, doneIdx, exitIdx)
}

func TestLogCmd_InvalidJobID(t *testing.T) {
	assert := assert.New(t)

	cmd := logCmd()
	cmd.SetArgs([]string{"abc"})
	cmd.SilenceUsage = true
	err := cmd.Execute()
	require.Error(t, err, "expected error for non-numeric job ID")
	assert.Contains(err.Error(), "invalid job ID")
}

func TestLogCmd_MissingLogFile(t *testing.T) {
	assert := assert.New(t)

	t.Setenv("ROBOREV_DATA_DIR", t.TempDir())
	cmd := logCmd()
	cmd.SetArgs([]string{"99999"})
	cmd.SilenceUsage = true
	err := cmd.Execute()
	require.Error(t, err, "expected error for missing log file")
	assert.Contains(err.Error(), "no log for job")
}

func TestLogCmd_PathFlag(t *testing.T) {
	assert := assert.New(t)

	dir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", dir)

	var buf bytes.Buffer
	cmd := logCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--path", "42"})
	cmd.SilenceUsage = true
	// --path succeeds even if log file doesn't exist.
	err := cmd.Execute()
	require.NoError(t, err, "--path should succeed")

	out := strings.TrimSpace(buf.String())
	want := filepath.Join(dir, "logs", "jobs", "42.log")
	assert.Equal(want, out)
}

func TestLogCmd_RawFlag(t *testing.T) {
	assert := assert.New(t)

	dir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", dir)

	// Create a log file at the expected path.
	logDir := filepath.Join(dir, "logs", "jobs")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "42.log")
	rawContent := `{"type":"assistant"}` + "\n"
	os.WriteFile(logPath, []byte(rawContent), 0644)

	var buf bytes.Buffer
	cmd := logCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--raw", "42"})
	cmd.SilenceUsage = true
	err := cmd.Execute()
	require.NoError(t, err, "--raw should succeed")
	assert.Equal(rawContent, buf.String())
}

func TestLooksLikeJSON(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`{"type":"assistant"}`, true},
		{`  {"type":"tool"}`, true},
		{`not json`, false},
		{``, false},
		{`[1,2,3]`, false},
		{`{invalid}`, false},
		// Valid JSON but no "type" field — should NOT match
		{`{"foo":"bar"}`, false},
		// Empty type — should NOT match
		{`{"type":""}`, false},
	}
	for _, tt := range tests {
		assert := assert.New(t)
		got := streamfmt.LooksLikeJSON(tt.input)
		assert.Equal(tt.want, got, "streamfmt.LooksLikeJSON(%q)", tt.input)
	}
}

func TestRenderJobLog_OpenCodeEvents(t *testing.T) {
	input := strings.Join([]string{
		toJSON(map[string]any{
			"type": "step_start",
			"part": map[string]any{"type": "step-start"},
		}),
		toJSON(map[string]any{
			"type": "text",
			"part": map[string]any{
				"type": "text",
				"text": "Reviewing the code.",
			},
		}),
		toJSON(map[string]any{
			"type": "tool",
			"part": map[string]any{
				"type": "tool",
				"tool": "Read",
				"id":   "tc_1",
				"state": map[string]any{
					"status": "running",
					"input": map[string]any{
						"file_path": "main.go",
					},
				},
			},
		}),
		toJSON(map[string]any{
			"type": "text",
			"part": map[string]any{
				"type": "text",
				"text": "Looks good overall.",
			},
		}),
		toJSON(map[string]any{
			"type": "step_finish",
			"part": map[string]any{
				"type":   "step-finish",
				"reason": "stop",
			},
		}),
	}, "\n")

	out := streamfmt.StripANSI(executeRenderJobLog(t, input, true))

	assertLogContains(t, out, "Reviewing the code.")
	assertLogContains(t, out, "Read")
	assertLogContains(t, out, "main.go")
	assertLogContains(t, out, "Looks good overall.")

	// Lifecycle events should be suppressed
	assertLogNotContains(t, out, "step_start")
	assertLogNotContains(t, out, "step_finish")
	// Raw JSON should not appear
	assertLogNotContains(t, out, `"type"`)
}
