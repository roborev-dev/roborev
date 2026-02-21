package agent

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/roborev-dev/roborev/internal/git"
)

// TestAgent is a mock agent for testing that returns predictable output
type TestAgent struct {
	Delay     time.Duration  // Simulated processing delay
	Output    string         // Fixed output to return
	Fail      bool           // If true, returns an error
	Reasoning ReasoningLevel // Reasoning level (for testing)
}

// NewTestAgent creates a new test agent
func NewTestAgent() *TestAgent {
	return &TestAgent{
		Delay:     100 * time.Millisecond,
		Output:    "Test review output: This commit looks good. No issues found.",
		Reasoning: ReasoningStandard,
	}
}

// WithReasoning returns a copy of the agent with the specified reasoning level
func (a *TestAgent) WithReasoning(level ReasoningLevel) Agent {
	return &TestAgent{
		Delay:     a.Delay,
		Output:    a.Output,
		Fail:      a.Fail,
		Reasoning: level,
	}
}

// WithAgentic returns the agent unchanged (agentic mode not applicable for test agent)
func (a *TestAgent) WithAgentic(agentic bool) Agent {
	return a
}

// WithModel returns the agent unchanged (model selection not supported for test agent).
func (a *TestAgent) WithModel(model string) Agent {
	return a
}

func (a *TestAgent) CommandLine() string {
	return "test"
}

func (a *TestAgent) Name() string {
	return "test"
}

func (a *TestAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Respect context cancellation
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(a.Delay):
	}

	if a.Fail {
		return "", fmt.Errorf("test agent configured to fail")
	}

	shortSHA := git.ShortSHA(commitSHA)
	result := fmt.Sprintf("%s\n\nCommit: %s\nRepo: %s", a.Output, shortSHA, repoPath)
	if output != nil {
		if _, err := output.Write([]byte(result)); err != nil {
			return "", fmt.Errorf("write output: %w", err)
		}
	}
	return result, nil
}

func init() {
	Register(NewTestAgent())
}
