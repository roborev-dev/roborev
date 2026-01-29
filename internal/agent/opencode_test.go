package agent

import (
	"context"
	"testing"
)

func TestFilterOpencodeToolCallLines(t *testing.T) {
	readCall := makeToolCallJSON("read", map[string]interface{}{"path": "/foo"})
	editCall := makeToolCallJSON("edit", map[string]interface{}{})

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "only tool-call lines",
			input:    readCall + "\n" + editCall,
			expected: "",
		},
		{
			name:     "only normal text",
			input:    "**Review:** No issues.\nDone.",
			expected: "**Review:** No issues.\nDone.",
		},
		{
			name:     "mixed",
			input:    makeToolCallJSON("read", map[string]interface{}{}) + "\n" + "Real text\n" + makeToolCallJSON("edit", map[string]interface{}{}),
			expected: "Real text",
		},
		{
			name:     "empty",
			input:    "",
			expected: "",
		},
		{
			name:     "only newlines",
			input:    "\n\n",
			expected: "",
		},
		{
			name:     "JSON without arguments",
			input:    `{"name":"foo"}`,
			expected: `{"name":"foo"}`,
		},
		{
			name:     "JSON without name",
			input:    `{"arguments":{}}`,
			expected: `{"arguments":{}}`,
		},
		{
			name:     "JSON with name and arguments plus extra keys preserved",
			input:    `{"name":"example","arguments":{"foo":"bar"},"description":"This is a JSON example"}`,
			expected: `{"name":"example","arguments":{"foo":"bar"},"description":"This is a JSON example"}`,
		},
		{
			name:     "leading indentation preserved",
			input:    "  indented line\n    more indented",
			expected: "  indented line\n    more indented",
		},
		{
			name:     "code block with JSON example preserved",
			input:    "Here's an example:\n```json\n{\"name\":\"test\",\"arguments\":{},\"extra\":true}\n```",
			expected: "Here's an example:\n```json\n{\"name\":\"test\",\"arguments\":{},\"extra\":true}\n```",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterOpencodeToolCallLines(tt.input)
			if got != tt.expected {
				t.Errorf("filterOpencodeToolCallLines(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestOpenCodeReviewFiltersToolCallLines(t *testing.T) {
	skipIfWindows(t)
	script := NewScriptBuilder().
		AddToolCall("read", map[string]interface{}{"path": "/foo"}).
		AddOutput("**Review:** Fix the typo.").
		AddToolCall("edit", map[string]interface{}{}).
		AddOutput("Done.").
		Build()
	cmdPath := writeTempCommand(t, script)
	a := NewOpenCodeAgent(cmdPath)
	result, err := a.Review(context.Background(), t.TempDir(), "head", "prompt", nil)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	assertContains(t, result, "**Review:**")
	assertContains(t, result, "Done.")
	assertNotContains(t, result, `"name":"read"`)
}
