package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
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
	}
}

func (a *OpenCodeAgent) Name() string {
	return "opencode"
}

func (a *OpenCodeAgent) CommandName() string {
	return a.Command
}

func (a *OpenCodeAgent) CommandLine() string {
	args := []string{"run", "--format", "json"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	return a.Command + " " + strings.Join(args, " ")
}

func (a *OpenCodeAgent) Review(
	ctx context.Context,
	repoPath, commitSHA, prompt string,
	output io.Writer,
) (string, error) {
	args := []string{"run", "--format", "json"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}

	cmd := exec.CommandContext(ctx, a.Command, args...)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(prompt)

	// Share a single syncWriter so stdout and stderr writes
	// to the output writer are serialized by one mutex.
	sw := newSyncWriter(output)

	var stderrBuf bytes.Buffer
	if sw != nil {
		cmd.Stderr = io.MultiWriter(&stderrBuf, sw)
	} else {
		cmd.Stderr = &stderrBuf
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start opencode: %w", err)
	}
	defer stdoutPipe.Close()

	result, parseErr := parseOpenCodeJSON(stdoutPipe, sw)

	if waitErr := cmd.Wait(); waitErr != nil {
		var detail strings.Builder
		fmt.Fprintf(&detail, "opencode failed")
		if parseErr != nil {
			fmt.Fprintf(&detail, "\nstream: %v", parseErr)
		}
		if s := stderrBuf.String(); s != "" {
			fmt.Fprintf(&detail, "\nstderr: %s", s)
		}
		if result != "" {
			partial := result
			if len(partial) > 500 {
				partial = partial[:500] + "..."
			}
			fmt.Fprintf(&detail, "\npartial output: %s", partial)
		}
		return "", fmt.Errorf("%s: %w", detail.String(), waitErr)
	}

	if parseErr != nil {
		return result, parseErr
	}

	if result == "" {
		return "No review output generated", nil
	}
	return result, nil
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

	var textParts []string

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return strings.Join(textParts, ""), fmt.Errorf(
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
					part.Type == "text" && part.Text != "" {
					textParts = append(
						textParts,
						stripTerminalControls(part.Text),
					)
				}
			}
		}

		if err == io.EOF {
			break
		}
	}

	return strings.Join(textParts, ""), nil
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
