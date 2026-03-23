package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// OpenCodeAgent runs code reviews using the OpenCode CLI
type OpenCodeAgent struct {
	Command   string         // The opencode command to run (default: "opencode")
	Model     string         // Model to use (provider/model format, e.g., "anthropic/claude-sonnet-4-20250514")
	Reasoning ReasoningLevel // Reasoning level (for future support)
	Agentic   bool           // Whether agentic mode is enabled (OpenCode auto-approves in non-interactive mode)
	SessionID string         // Existing session ID to resume
}

// NewOpenCodeAgent creates a new OpenCode agent
func NewOpenCodeAgent(command string) *OpenCodeAgent {
	if command == "" {
		command = "opencode"
	}
	return &OpenCodeAgent{Command: command, Reasoning: ReasoningStandard}
}

// WithReasoning returns a copy of the agent with the model preserved (reasoning not yet supported).
func (a *OpenCodeAgent) WithReasoning(level ReasoningLevel) Agent {
	return &OpenCodeAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: level,
		Agentic:   a.Agentic,
		SessionID: a.SessionID,
	}
}

// WithAgentic returns a copy of the agent configured for agentic mode.
// Note: OpenCode's `run` command auto-approves all permissions in non-interactive mode,
// so agentic mode is effectively always enabled when running through roborev.
func (a *OpenCodeAgent) WithAgentic(agentic bool) Agent {
	return &OpenCodeAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
		SessionID: a.SessionID,
	}
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *OpenCodeAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}
	return &OpenCodeAgent{
		Command:   a.Command,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
		SessionID: a.SessionID,
	}
}

// WithSessionID returns a copy of the agent configured to resume a prior session.
func (a *OpenCodeAgent) WithSessionID(sessionID string) Agent {
	return &OpenCodeAgent{
		Command:   a.Command,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
		SessionID: sanitizedResumeSessionID(sessionID),
	}
}

func (a *OpenCodeAgent) Name() string {
	return "opencode"
}

func (a *OpenCodeAgent) CommandName() string {
	return a.Command
}

func (a *OpenCodeAgent) buildArgs() []string {
	sessionID := sanitizedResumeSessionID(a.SessionID)
	args := []string{"run", "--format", "json"}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	return args
}

func (a *OpenCodeAgent) CommandLine() string {
	args := a.buildArgs()
	return a.Command + " " + strings.Join(args, " ")
}

func (a *OpenCodeAgent) Review(
	ctx context.Context,
	repoPath, commitSHA, prompt string,
	output io.Writer,
) (string, error) {
	args := a.buildArgs()

	runResult, runErr := runStreamingCLI(ctx, streamingCLISpec{
		Name:         "opencode",
		Command:      a.Command,
		Args:         args,
		Dir:          repoPath,
		Stdin:        strings.NewReader(prompt),
		Output:       output,
		StreamStderr: true,
		DrainStdout:  true,
		Parse:        parseOpenCodeJSON,
	})
	if runErr != nil {
		return "", runErr
	}

	if runResult.WaitErr != nil {
		var detail strings.Builder
		fmt.Fprintf(&detail, "opencode failed")
		if runResult.ParseErr != nil {
			fmt.Fprintf(&detail, "\nstream: %v", runResult.ParseErr)
		}
		if s := runResult.Stderr; s != "" {
			fmt.Fprintf(&detail, "\nstderr: %s", s)
		}
		if runResult.Result != "" {
			partial := runResult.Result
			if len(partial) > 500 {
				partial = partial[:500] + "..."
			}
			fmt.Fprintf(&detail, "\npartial output: %s", partial)
		}
		return "", fmt.Errorf("%s: %w", detail.String(), runResult.WaitErr)
	}

	if runResult.ParseErr != nil {
		return runResult.Result, runResult.ParseErr
	}

	if runResult.Result == "" {
		return "No review output generated", nil
	}
	return runResult.Result, nil
}

// opencodeEvent represents a top-level JSONL event from opencode --format json.
type opencodeEvent struct {
	Type string          `json:"type"`
	Part json.RawMessage `json:"part,omitempty"`
}

// opencodePart represents the nested part payload in opencode events.
type opencodePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// parseOpenCodeJSON reads JSONL from opencode --format json, streams
// raw lines to the output writer (for live rendering + log capture),
// and extracts the final result text from "text" events.
func parseOpenCodeJSON(
	r io.Reader, sw *syncWriter,
) (string, error) {
	br := bufio.NewReader(r)

	textParts := newTrailingReviewText()

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return textParts.Join(""), fmt.Errorf(
				"read stream: %w", err,
			)
		}

		line = strings.TrimSpace(line)
		if line != "" {
			if sw != nil {
				_, _ = sw.Write([]byte(line + "\n"))
			}

			var ev opencodeEvent
			if json.Unmarshal([]byte(line), &ev) == nil &&
				ev.Part != nil {
				var part opencodePart
				if json.Unmarshal(ev.Part, &part) == nil &&
					part.Type != "" {
					if part.Type == "tool" {
						textParts.ResetAfterTool()
					}
					if part.Type == "text" && part.Text != "" {
						textParts.Add(stripTerminalControls(part.Text))
					}
				}
			}
		}

		if err == io.EOF {
			break
		}
	}

	return textParts.Join(""), nil
}

// ansiPattern matches ANSI CSI and OSC escape sequences.
var ansiPattern = regexp.MustCompile(
	`\x1b\[[0-9;?]*[a-zA-Z]` +
		`|\x1b\]([^\x07\x1b]|\x1b[^\\])*(\x07|\x1b\\)`,
)

// stripTerminalControls removes ANSI escape sequences and
// non-printable control characters from s while preserving
// newlines and tabs. Used to sanitize agent output at ingestion
// before it is stored or rendered.
func stripTerminalControls(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r == '\n' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func init() {
	Register(NewOpenCodeAgent(""))
}
