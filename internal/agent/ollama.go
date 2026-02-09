package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ollamaDefaultBaseURL  = "http://localhost:11434"
	ollamaAvailabilityTTL = 30 * time.Second
	ollamaHealthTimeout   = 3 * time.Second
)

// OllamaAgent runs code reviews using the Ollama HTTP API.
type OllamaAgent struct {
	BaseURL   string         // e.g. "http://localhost:11434"
	Model     string         // e.g. "qwen2.5-coder:32b"
	Reasoning ReasoningLevel // Phase 1: no-op
	Agentic   bool           // Phase 1: no-op, always read-only

	mu         sync.Mutex
	lastCheck  time.Time
	available  bool
	clientOnce sync.Once
	client     *http.Client // IPv4-only client; lazily initialized
}

// NewOllamaAgent creates a new Ollama agent. If baseURL is empty, uses http://localhost:11434.
func NewOllamaAgent(baseURL string) *OllamaAgent {
	if baseURL == "" {
		baseURL = ollamaDefaultBaseURL
	}
	return &OllamaAgent{BaseURL: baseURL, Reasoning: ReasoningStandard}
}

// Name returns the agent identifier.
func (a *OllamaAgent) Name() string {
	return "ollama"
}

// WithReasoning returns a copy of the agent with the specified reasoning level (Phase 1: no-op for behavior).
func (a *OllamaAgent) WithReasoning(level ReasoningLevel) Agent {
	return &OllamaAgent{
		BaseURL:   a.BaseURL,
		Model:     a.Model,
		Reasoning: level,
		Agentic:   a.Agentic,
	}
}

// WithAgentic returns a copy of the agent configured for agentic mode (Phase 1: no-op, always read-only).
func (a *OllamaAgent) WithAgentic(agentic bool) Agent {
	return &OllamaAgent{
		BaseURL:   a.BaseURL,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   agentic,
	}
}

// WithModel returns a copy of the agent configured to use the specified model.
func (a *OllamaAgent) WithModel(model string) Agent {
	return &OllamaAgent{
		BaseURL:   a.BaseURL,
		Model:     model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

// WithBaseURL returns a copy of the agent configured to use the specified base URL.
// If baseURL is empty, uses the default "http://localhost:11434".
func (a *OllamaAgent) WithBaseURL(baseURL string) Agent {
	if baseURL == "" {
		baseURL = ollamaDefaultBaseURL
	}
	return &OllamaAgent{
		BaseURL:   baseURL,
		Model:     a.Model,
		Reasoning: a.Reasoning,
		Agentic:   a.Agentic,
	}
}

// httpClient returns an HTTP client that uses IPv4 only (tcp4), to avoid "no route to host"
// when the default dialer prefers or tries IPv6 on dual-stack systems.
func (a *OllamaAgent) httpClient() *http.Client {
	a.clientOnce.Do(func() {
		dialer := &net.Dialer{}
		a.client = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.DialContext(ctx, "tcp4", addr)
				},
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}
	})
	return a.client
}

// IsAvailable reports whether the Ollama server is reachable (HTTP health check to /api/tags, 30s TTL).
func (a *OllamaAgent) IsAvailable() bool {
	a.mu.Lock()
	if time.Since(a.lastCheck) < ollamaAvailabilityTTL {
		avail := a.available
		a.mu.Unlock()
		return avail
	}
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), ollamaHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(a.BaseURL, "/")+"/api/tags", nil)
	if err != nil {
		a.updateAvailability(false)
		return false
	}
	resp, err := a.httpClient().Do(req)
	if err != nil {
		a.updateAvailability(false)
		return false
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("ollama availability check: close response body: %v", err)
		}
	}()
	ok := resp.StatusCode == http.StatusOK
	a.updateAvailability(ok)
	return ok
}

func (a *OllamaAgent) updateAvailability(avail bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastCheck = time.Now()
	a.available = avail
}

const ollamaAgentLoopMaxRounds = 20

// Review runs a code review via Ollama /api/chat (streaming NDJSON).
// When agentic mode is enabled, sends tools and runs an agent loop until the model returns no tool_calls.
func (a *OllamaAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	_ = commitSHA

	if strings.TrimSpace(a.Model) == "" {
		return "", fmt.Errorf("ollama model not configured; set model in config or use --model")
	}

	agenticMode := a.Agentic || AllowUnsafeAgents()
	if !agenticMode {
		return a.reviewSingleRound(ctx, repoPath, prompt, output)
	}
	return a.reviewAgenticLoop(ctx, repoPath, prompt, output)
}

// reviewSingleRound does one POST /api/chat with no tools (current behavior).
func (a *OllamaAgent) reviewSingleRound(ctx context.Context, repoPath, prompt string, output io.Writer) (string, error) {
	baseURL := strings.TrimSuffix(a.BaseURL, "/")
	urlStr := baseURL + "/api/chat"
	reqBody := ollamaChatRequest{
		Model:    a.Model,
		Messages: []ollamaMessage{{Role: "user", Content: prompt}},
		Stream:   true,
		Options: map[string]any{
			"temperature": 0.7,
			"num_predict": 8192,
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	resp, err := a.doChatRequest(ctx, urlStr, body, baseURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	return a.parseStreamNDJSON(resp.Body, output)
}

// reviewAgenticLoop runs the agent loop: send messages+tools, parse stream for content+tool_calls, execute tools, append messages, repeat.
func (a *OllamaAgent) reviewAgenticLoop(ctx context.Context, repoPath, prompt string, output io.Writer) (string, error) {
	baseURL := strings.TrimSuffix(a.BaseURL, "/")
	urlStr := baseURL + "/api/chat"
	messages := []ollamaMessage{{Role: "user", Content: prompt}}
	tools := buildOllamaTools(true)
	// Use Discard when output is nil so parseStreamNDJSONWithToolCalls never gets nil (avoids nil syncWriter).
	out := output
	if out == nil {
		out = io.Discard
	}
	sw := newSyncWriter(out)
	var finalContent strings.Builder

	for round := 0; round < ollamaAgentLoopMaxRounds; round++ {
		reqBody := ollamaChatRequest{
			Model:    a.Model,
			Messages: messages,
			Stream:   true,
			Tools:    tools,
			Options: map[string]any{
				"temperature": 0.7,
				"num_predict": 8192,
			},
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			return "", fmt.Errorf("ollama request: %w", err)
		}
		resp, err := a.doChatRequest(ctx, urlStr, body, baseURL)
		if err != nil {
			return "", err
		}
		content, toolCalls, err := a.parseStreamNDJSONWithToolCalls(resp.Body, sw)
		_ = resp.Body.Close()
		if err != nil {
			return "", err
		}
		finalContent.Reset()
		finalContent.WriteString(content)

		if len(toolCalls) == 0 {
			return finalContent.String(), nil
		}

		// Append assistant message with tool_calls
		assistantMsg := ollamaMessage{Role: "assistant", Content: content, ToolCalls: toolCalls}
		messages = append(messages, assistantMsg)

		// Execute each tool and append tool results
		for _, tc := range toolCalls {
			name := tc.Function.Name
			args := tc.Function.Arguments
			if args == nil {
				args = make(map[string]any)
			}
			result, execErr := ExecuteTool(ctx, repoPath, name, args, true)
			if execErr != nil {
				result = execErr.Error()
			}
			messages = append(messages, ollamaMessage{Role: "tool", ToolName: name, Content: result})
		}
	}

	// Max rounds reached; return what we have
	return finalContent.String(), nil
}

// doChatRequest POSTs to urlStr and returns the response. Caller must close resp.Body.
// On non-200 status returns nil resp and error.
func (a *OllamaAgent) doChatRequest(ctx context.Context, urlStr string, body []byte, baseURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("ollama request timeout: %w", ctx.Err())
			}
			return nil, ctx.Err()
		}
		classifiedErr := classifyNetworkError(err, baseURL)
		if classifiedErr != "" {
			return nil, fmt.Errorf("%s: %w", classifiedErr, err)
		}
		return nil, fmt.Errorf("ollama server not reachable: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		slurp, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("failed to read error response from Ollama: %w", readErr)
		}
		bodyStr := strings.TrimSpace(string(slurp))
		var errResp ollamaErrorResponse
		_ = json.Unmarshal(slurp, &errResp)
		hasErrorMsg := errResp.Error != ""
		if hasErrorMsg {
			bodyStr = errResp.Error
		}
		switch resp.StatusCode {
		case http.StatusBadRequest:
			if hasErrorMsg {
				return nil, fmt.Errorf("invalid model %q: %s (check model name format, e.g., 'model:tag')", a.Model, bodyStr)
			}
			return nil, fmt.Errorf("invalid request for model %q: %s", a.Model, bodyStr)
		case http.StatusNotFound:
			if hasErrorMsg {
				return nil, fmt.Errorf("model %q not found: %s (pull with: ollama pull %s)", a.Model, bodyStr, a.Model)
			}
			return nil, fmt.Errorf("model %q not found: model not available locally (pull with: ollama pull %s)", a.Model, a.Model)
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("ollama authentication failed: %s (check server configuration)", bodyStr)
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
			if hasErrorMsg {
				return nil, fmt.Errorf("ollama server error (%s): %s", resp.Status, bodyStr)
			}
			return nil, fmt.Errorf("ollama server error (%s): %s", resp.Status, bodyStr)
		default:
			if hasErrorMsg {
				return nil, fmt.Errorf("ollama API error (%s): %s", resp.Status, bodyStr)
			}
			return nil, fmt.Errorf("ollama API error (%s): %s", resp.Status, bodyStr)
		}
	}
	return resp, nil
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Options  map[string]any  `json:"options,omitempty"`
}

// ollamaTool is one tool definition for /api/chat (Ollama tool-calling format).
type ollamaTool struct {
	Type     string       `json:"type"`
	Function ollamaToolFn `json:"function"`
}

type ollamaToolFn struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ollamaToolCall is one tool call in a message (assistant) or stream chunk.
type ollamaToolCall struct {
	Function struct {
		Index     int            `json:"index,omitempty"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"` // for role "tool"
}

type ollamaStreamChunk struct {
	Message struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done bool `json:"done"`
}

type ollamaErrorResponse struct {
	Error string `json:"error"`
}

// buildOllamaTools returns the list of tools for /api/chat. When agentic is false, only Read, Glob, Grep; when true, adds Edit, Write, Bash.
func buildOllamaTools(agentic bool) []ollamaTool {
	tools := []ollamaTool{
		{
			Type: "function",
			Function: ollamaToolFn{
				Name:        "Read",
				Description: "Read the contents of a file in the repository. Use file_path relative to the repo root.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{"type": "string", "description": "Path to the file relative to the repository root"},
					},
					"required": []string{"file_path"},
				},
			},
		},
		{
			Type: "function",
			Function: ollamaToolFn{
				Name:        "Glob",
				Description: "List files matching a glob pattern in the repository.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{"type": "string", "description": "Glob pattern (e.g. *.go)"},
					},
					"required": []string{"pattern"},
				},
			},
		},
		{
			Type: "function",
			Function: ollamaToolFn{
				Name:        "Grep",
				Description: "Search for a pattern in repository files. Optional path restricts to a directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{"type": "string", "description": "Regex pattern to search"},
						"path":    map[string]any{"type": "string", "description": "Optional path or directory to restrict search"},
					},
					"required": []string{"pattern"},
				},
			},
		},
	}
	if agentic {
		tools = append(tools,
			ollamaTool{
				Type: "function",
				Function: ollamaToolFn{
					Name:        "Edit",
					Description: "Replace the first occurrence of old_string with new_string in a file.",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"file_path":  map[string]any{"type": "string", "description": "Path to the file relative to the repo root"},
							"old_string": map[string]any{"type": "string", "description": "Exact string to replace"},
							"new_string": map[string]any{"type": "string", "description": "Replacement string"},
						},
						"required": []string{"file_path", "old_string", "new_string"},
					},
				},
			},
			ollamaTool{
				Type: "function",
				Function: ollamaToolFn{
					Name:        "Write",
					Description: "Write contents to a file. Creates directories if needed.",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"file_path": map[string]any{"type": "string", "description": "Path to the file relative to the repo root"},
							"contents":  map[string]any{"type": "string", "description": "Contents to write"},
						},
						"required": []string{"file_path", "contents"},
					},
				},
			},
			ollamaTool{
				Type: "function",
				Function: ollamaToolFn{
					Name:        "Bash",
					Description: "Run a shell command in the repository directory.",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"command": map[string]any{"type": "string", "description": "Shell command to run"},
						},
						"required": []string{"command"},
					},
				},
			},
		)
	}
	return tools
}

// classifyNetworkError categorizes network errors and returns a descriptive error message.
// It distinguishes between connection refused, timeouts, DNS errors, and other network issues.
func classifyNetworkError(err error, baseURL string) string {
	if err == nil {
		return ""
	}

	// Check for url.Error (wraps network errors from http.Client)
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// Check underlying error type
		if urlErr.Timeout() {
			return fmt.Sprintf("ollama connection timeout: server at %s did not respond in time", baseURL)
		}
		// Check for DNS errors
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return fmt.Sprintf("ollama DNS error: cannot resolve hostname %s (%s)", dnsErr.Name, dnsErr.Err)
		}
		// Check for connection refused and other common errors
		if urlErr.Err != nil {
			errStr := urlErr.Err.Error()
			if strings.Contains(errStr, "connection refused") {
				return fmt.Sprintf("ollama connection refused: server not running at %s (start with: ollama serve)", baseURL)
			}
			if strings.Contains(errStr, "no such host") {
				return fmt.Sprintf("ollama DNS error: cannot resolve hostname for %s", baseURL)
			}
			if strings.Contains(errStr, "network is unreachable") {
				return fmt.Sprintf("ollama network unreachable: cannot reach %s", baseURL)
			}
			if strings.Contains(errStr, "no route to host") {
				msg := fmt.Sprintf("ollama no route to host: cannot reach %s", baseURL)
				if runtime.GOOS == "darwin" {
					msg += " (if curl to this URL works from the same machine, check System Settings > Privacy & Security > Local Network and allow your terminal/IDE, or run roborev from Terminal.app; the binary may need outbound local network access)"
				}
				return msg
			}
		}
		// Generic url.Error
		return fmt.Sprintf("ollama network error: %v", urlErr)
	}

	// Check for net.Error interface (timeout, temporary errors)
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return fmt.Sprintf("ollama connection timeout: server at %s did not respond in time", baseURL)
		}
		return fmt.Sprintf("ollama network error: %v", netErr)
	}

	// Check for DNS errors directly
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Sprintf("ollama DNS error: cannot resolve hostname %s (%s)", dnsErr.Name, dnsErr.Err)
	}

	// Generic error
	return fmt.Sprintf("ollama connection error: %v", err)
}

// parseStreamNDJSON reads NDJSON from r, streams raw lines to output, and returns accumulated message.content.
func (a *OllamaAgent) parseStreamNDJSON(r io.Reader, output io.Writer) (string, error) {
	content, _, err := a.parseStreamNDJSONWithToolCalls(r, output)
	return content, err
}

// parseStreamNDJSONWithToolCalls reads NDJSON from r, streams raw lines and tool indicators to output,
// and returns accumulated content and tool_calls (merged by index). Used for agentic loop.
func (a *OllamaAgent) parseStreamNDJSONWithToolCalls(r io.Reader, output io.Writer) (content string, toolCalls []ollamaToolCall, err error) {
	br := bufio.NewReader(r)
	var acc strings.Builder
	var seenDone bool
	sw := newSyncWriter(output)
	lineNum := 0
	// Merge tool_calls by index (Ollama may stream partial chunks)
	byIndex := make(map[int]*ollamaToolCall)

	for {
		line, readErr := br.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return "", nil, fmt.Errorf("ollama read stream: %w", readErr)
		}

		lineNum++
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if sw != nil {
				_, _ = sw.Write([]byte(trimmed + "\n"))
			}
			var chunk ollamaStreamChunk
			if jsonErr := json.Unmarshal([]byte(trimmed), &chunk); jsonErr != nil {
				var syntaxErr *json.SyntaxError
				if errors.As(jsonErr, &syntaxErr) {
					preview := trimmed
					if len(preview) > 100 {
						preview = preview[:100] + "..."
					}
					return "", nil, fmt.Errorf("ollama NDJSON parse error at line %d, offset %d: %w (content: %q)", lineNum, syntaxErr.Offset, jsonErr, preview)
				}
				preview := trimmed
				if len(preview) > 100 {
					preview = preview[:100] + "..."
				}
				return "", nil, fmt.Errorf("ollama NDJSON parse error at line %d: %w (content: %q)", lineNum, jsonErr, preview)
			}
			if chunk.Message.Content != "" {
				acc.WriteString(chunk.Message.Content)
			}
			for i := range chunk.Message.ToolCalls {
				tc := &chunk.Message.ToolCalls[i]
				idx := tc.Function.Index
				existing, ok := byIndex[idx]
				if !ok {
					byIndex[idx] = tc
					if sw != nil && tc.Function.Name != "" {
						_, _ = sw.Write([]byte("[Tool: " + tc.Function.Name + "]\n"))
					}
				} else {
					if tc.Function.Name != "" {
						existing.Function.Name = tc.Function.Name
					}
					if len(tc.Function.Arguments) > 0 {
						existing.Function.Arguments = tc.Function.Arguments
					}
				}
			}
			if chunk.Done {
				seenDone = true
				break
			}
		}

		if readErr == io.EOF {
			break
		}
	}

	if !seenDone && acc.Len() > 0 {
		return "", nil, fmt.Errorf("ollama stream incomplete: missing done=true after %d lines", lineNum)
	}

	// Return tool_calls sorted by index
	indices := make([]int, 0, len(byIndex))
	for i := range byIndex {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		toolCalls = append(toolCalls, *byIndex[i])
	}
	return acc.String(), toolCalls, nil
}

func init() {
	Register(NewOllamaAgent(""))
}
