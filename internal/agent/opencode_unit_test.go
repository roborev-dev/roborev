package agent

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeModelFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		model        string
		wantModel    bool
		wantContains string
	}{
		{name: "no model omits flag", model: ""},
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
			assert.Contains(t, cl, "--format json")
			if tt.wantModel {
				assert.Contains(t, cl, "--model")
				assert.Contains(t, cl, tt.wantContains)
			} else {
				assert.NotContains(t, cl, "--model")
			}
		})
	}
}

func TestParseOpenCodeJSON(t *testing.T) {
	t.Parallel()

	lines := strings.Join([]string{
		unitMakeTextEvent("Part one."),
		unitMakeOpenCodeEvent("tool", map[string]any{
			"type": "tool", "tool": "Read",
		}),
		unitMakeTextEvent(" Part two."),
	}, "\n") + "\n"

	var outputBuf strings.Builder
	result, err := parseOpenCodeJSON(
		strings.NewReader(lines), newSyncWriter(&outputBuf),
	)
	require.NoError(t, err)
	assert.Equal(t, " Part two.", result)
	assert.Equal(t, 3, strings.Count(outputBuf.String(), "\n"))
}

func TestParseOpenCodeJSON_NilOutput(t *testing.T) {
	t.Parallel()
	lines := unitMakeTextEvent("ok") + "\n"
	result, err := parseOpenCodeJSON(strings.NewReader(lines), nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
}

func TestParseOpenCodeJSON_ReadError(t *testing.T) {
	t.Parallel()
	result, err := parseOpenCodeJSON(
		&unitFailAfterReader{data: unitMakeTextEvent("partial") + "\n"},
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, result, "partial")
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
			assert.Equal(t, tt.want, stripTerminalControls(tt.input))
		})
	}
}

func TestParseOpenCodeJSON_SanitizesControlChars(t *testing.T) {
	t.Parallel()
	lines := unitMakeTextEvent("\x1b[31mred\x1b[0m and \x1b]0;evil\x07safe") + "\n"
	result, err := parseOpenCodeJSON(strings.NewReader(lines), nil)
	require.NoError(t, err)
	assert.NotContains(t, result, "\x1b")
	assert.NotContains(t, result, "\x07")
	assert.Contains(t, result, "red")
	assert.Contains(t, result, "safe")
	assert.NotContains(t, result, "evil")
}

// Helpers prefixed with unit to avoid redeclaration with integration file.

func unitMakeOpenCodeEvent(eventType string, part map[string]any) string {
	ev := map[string]any{"type": eventType, "part": part}
	b, err := json.Marshal(ev)
	if err != nil {
		panic("unitMakeOpenCodeEvent: " + err.Error())
	}
	return string(b)
}

func unitMakeTextEvent(text string) string {
	return unitMakeOpenCodeEvent("text", map[string]any{
		"type": "text", "text": text,
	})
}

type unitFailAfterReader struct {
	data string
	done bool
}

func (r *unitFailAfterReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.ErrUnexpectedEOF
	}
	r.done = true
	n := copy(p, r.data)
	return n, nil
}
