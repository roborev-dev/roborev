package streamfmt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("toJSON: %v", err)
	}
	return string(b)
}

func executeRenderJobLog(t *testing.T, input string, isTTY bool) string {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderLog(strings.NewReader(input), &buf, isTTY); err != nil {
		t.Fatalf("RenderLog: %v", err)
	}
	return buf.String()
}

func assertLogContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("expected output to contain %q, got:\n%s", want, got)
	}
}

func assertLogNotContains(t *testing.T, got, want string) {
	t.Helper()
	if strings.Contains(got, want) {
		t.Errorf("expected output to NOT contain %q, got:\n%s", want, got)
	}
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

	out := StripANSI(executeRenderJobLog(t, input, true))

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
				if out != "" {
					t.Errorf("expected empty output, got %q", out)
				}
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

func TestRenderJobLog_MixedJSONAndPlainText(t *testing.T) {
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
	expectedOrder := []string{"Hello", "stderr: warning something", "Done", "exit status 0"}
	lastIdx := 0
	for _, text := range expectedOrder {
		idx := strings.Index(out[lastIdx:], text)
		if idx == -1 {
			t.Fatalf("expected %q to appear after previous elements in output:\n%s", text, out)
		}
		lastIdx += idx + len(text)
	}
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
		got := LooksLikeJSON(tt.input)
		if got != tt.want {
			t.Errorf("LooksLikeJSON(%q) = %v, want %v",
				tt.input, got, tt.want)
		}
	}
}

func TestRenderJobLog_OpenCodeEvents(t *testing.T) {
	input := strings.Join([]string{
		toJSON(t, map[string]any{
			"type": "step_start",
			"part": map[string]any{"type": "step-start"},
		}),
		toJSON(t, map[string]any{
			"type": "text",
			"part": map[string]any{
				"type": "text",
				"text": "Reviewing the code.",
			},
		}),
		toJSON(t, map[string]any{
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
		toJSON(t, map[string]any{
			"type": "text",
			"part": map[string]any{
				"type": "text",
				"text": "Looks good overall.",
			},
		}),
		toJSON(t, map[string]any{
			"type": "step_finish",
			"part": map[string]any{
				"type":   "step-finish",
				"reason": "stop",
			},
		}),
	}, "\n")

	out := StripANSI(executeRenderJobLog(t, input, true))

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
