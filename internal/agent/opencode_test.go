package agent

import (
	"context"
	"strings"
	"testing"
)

func TestFilterOpencodeToolCallLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "only tool-call lines",
			input:    `{"name":"read","arguments":{"path":"/foo"}}` + "\n" + `{"name":"edit","arguments":{}}`,
			expected: "",
		},
		{
			name:     "only normal text",
			input:    "**Review:** No issues.\nDone.",
			expected: "**Review:** No issues.\nDone.",
		},
		{
			name:     "mixed",
			input:    `{"name":"read","arguments":{}}` + "\n" + "Real text\n" + `{"name":"edit","arguments":{}}`,
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
	script := `#!/bin/sh
printf '%s\n' '{"name":"read","arguments":{"path":"/foo"}}'
echo "**Review:** Fix the typo."
printf '%s\n' '{"name":"edit","arguments":{}}'
echo "Done."
`
	cmdPath := writeTempCommand(t, script)
	a := NewOpenCodeAgent(cmdPath)
	result, err := a.Review(context.Background(), t.TempDir(), "head", "prompt", nil)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !strings.Contains(result, "**Review:**") {
		t.Errorf("result missing **Review:**: %q", result)
	}
	if !strings.Contains(result, "Done.") {
		t.Errorf("result missing Done.: %q", result)
	}
	if strings.Contains(result, `"name":"read"`) {
		t.Errorf("result should not contain tool-call JSON: %q", result)
	}
}
