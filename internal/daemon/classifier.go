package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/prompt"
	"github.com/roborev-dev/roborev/internal/review/autotype"
)

// classifySchema is embedded in every classify call.
var classifySchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["design_review", "reason"],
  "properties": {
    "design_review": {"type": "boolean"},
    "reason":        {"type": "string"}
  }
}`)

// classifierAdapter bridges autotype.Classifier to a SchemaAgent.
type classifierAdapter struct {
	agent    agent.SchemaAgent
	maxBytes int
}

func newClassifierAdapter(a agent.SchemaAgent, maxBytes int) *classifierAdapter {
	if maxBytes <= 0 {
		maxBytes = 20 * 1024
	}
	return &classifierAdapter{agent: a, maxBytes: maxBytes}
}

type classifyResult struct {
	DesignReview bool   `json:"design_review"`
	Reason       string `json:"reason"`
}

// Decide implements autotype.Classifier.
func (c *classifierAdapter) Decide(ctx context.Context, in autotype.Input) (bool, string, error) {
	subject, body := splitMessage(in.Message)
	p, err := prompt.BuildClassifyPrompt(prompt.ClassifyInput{
		Subject:  subject,
		Body:     body,
		DiffStat: deriveStat(in.Diff, in.ChangedFiles),
		Diff:     in.Diff,
		MaxBytes: c.maxBytes,
	})
	if err != nil {
		return false, "", fmt.Errorf("build prompt: %w", err)
	}

	raw, err := c.agent.ClassifyWithSchema(ctx, in.RepoPath, in.GitRef, p, classifySchema, io.Discard)
	if err != nil {
		return false, "", fmt.Errorf("classifier agent: %w", err)
	}

	var out classifyResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, "", fmt.Errorf("invalid classifier output: %w (%q)", err, string(raw))
	}
	return out.DesignReview, out.Reason, nil
}

// splitMessage returns (subject, body) from a commit message.
func splitMessage(msg string) (string, string) {
	for i, c := range msg {
		if c == '\n' {
			rest := msg[i+1:]
			for len(rest) > 0 && (rest[0] == '\n' || rest[0] == '\r') {
				rest = rest[1:]
			}
			return msg[:i], rest
		}
	}
	return msg, ""
}

// deriveStat renders a minimal stat from changed files when the real
// git --stat output isn't available.
func deriveStat(diff string, files []string) string {
	if len(files) == 0 {
		return ""
	}
	lines := autotype.CountChangedLines(diff)
	return fmt.Sprintf(" %d files changed, ~%d line changes", len(files), lines)
}
