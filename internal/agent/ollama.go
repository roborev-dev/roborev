package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	ollamaDefaultBaseURL = "http://localhost:11434"
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.updateAvailability(false)
		return false
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			// Log if needed, but don't fail availability check
			// Close errors are typically non-critical for health checks
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

// Review runs a code review via Ollama /api/chat (streaming NDJSON).
func (a *OllamaAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	_ = repoPath
	_ = commitSHA

	if strings.TrimSpace(a.Model) == "" {
		return "", fmt.Errorf("ollama model not configured; set model in config or use --model")
	}

	baseURL := strings.TrimSuffix(a.BaseURL, "/")
	url := baseURL + "/api/chat"

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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Preserve context cancellation/timeout errors
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("ollama request timeout: %w", ctx.Err())
			}
			return "", ctx.Err()
		}
		// Classify network errors for better error messages
		classifiedErr := classifyNetworkError(err, baseURL)
		if classifiedErr != "" {
			return "", fmt.Errorf("%s: %w", classifiedErr, err)
		}
		return "", fmt.Errorf("ollama server not reachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slurp, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return "", fmt.Errorf("ollama read error response: %w", err)
		}
		bodyStr := strings.TrimSpace(string(slurp))

		// Try to parse Ollama error response
		var errResp ollamaErrorResponse
		hasErrorMsg := false
		if json.Unmarshal(slurp, &errResp) == nil && errResp.Error != "" {
			hasErrorMsg = true
			bodyStr = errResp.Error
		}

		switch resp.StatusCode {
		case http.StatusBadRequest:
			if hasErrorMsg {
				return "", fmt.Errorf("invalid model %q: %s (check model name format, e.g., 'model:tag')", a.Model, bodyStr)
			}
			return "", fmt.Errorf("invalid request for model %q: %s", a.Model, bodyStr)
		case http.StatusNotFound:
			if hasErrorMsg {
				return "", fmt.Errorf("model %q not found: %s (pull with: ollama pull %s)", a.Model, bodyStr, a.Model)
			}
			return "", fmt.Errorf("model %q not found: model not available locally (pull with: ollama pull %s)", a.Model, a.Model)
		case http.StatusUnauthorized:
			return "", fmt.Errorf("ollama authentication failed: %s (check server configuration)", bodyStr)
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
			if hasErrorMsg {
				return "", fmt.Errorf("ollama server error (%s): %s", resp.Status, bodyStr)
			}
			return "", fmt.Errorf("ollama server error (%s): %s", resp.Status, bodyStr)
		default:
			if hasErrorMsg {
				return "", fmt.Errorf("ollama API error (%s): %s", resp.Status, bodyStr)
			}
			return "", fmt.Errorf("ollama API error (%s): %s", resp.Status, bodyStr)
		}
	}

	return a.parseStreamNDJSON(resp.Body, output)
}

type ollamaChatRequest struct {
	Model    string         `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaStreamChunk struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

type ollamaErrorResponse struct {
	Error string `json:"error"`
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
		// Check for connection refused
		if urlErr.Err != nil {
			if strings.Contains(urlErr.Err.Error(), "connection refused") {
				return fmt.Sprintf("ollama connection refused: server not running at %s (start with: ollama serve)", baseURL)
			}
			if strings.Contains(urlErr.Err.Error(), "no such host") {
				return fmt.Sprintf("ollama DNS error: cannot resolve hostname for %s", baseURL)
			}
			if strings.Contains(urlErr.Err.Error(), "network is unreachable") {
				return fmt.Sprintf("ollama network unreachable: cannot reach %s", baseURL)
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
	br := bufio.NewReader(r)
	var acc strings.Builder
	var seenDone bool
	sw := newSyncWriter(output)
	lineNum := 0

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("ollama read stream: %w", err)
		}

		lineNum++
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if sw != nil {
				sw.Write([]byte(trimmed + "\n"))
			}
			var chunk ollamaStreamChunk
			if jsonErr := json.Unmarshal([]byte(trimmed), &chunk); jsonErr != nil {
				// Enhanced JSON error with context
				var syntaxErr *json.SyntaxError
				if errors.As(jsonErr, &syntaxErr) {
					// Show preview of malformed content (limit to 100 chars)
					preview := trimmed
					if len(preview) > 100 {
						preview = preview[:100] + "..."
					}
					return "", fmt.Errorf("ollama NDJSON parse error at line %d, offset %d: %w (content: %q)", lineNum, syntaxErr.Offset, jsonErr, preview)
				}
				// Non-syntax JSON errors (type mismatch, etc.)
				preview := trimmed
				if len(preview) > 100 {
					preview = preview[:100] + "..."
				}
				return "", fmt.Errorf("ollama NDJSON parse error at line %d: %w (content: %q)", lineNum, jsonErr, preview)
			}
			if chunk.Message.Content != "" {
				acc.WriteString(chunk.Message.Content)
			}
			if chunk.Done {
				seenDone = true
				break
			}
		}

		if err == io.EOF {
			break
		}
	}

	if !seenDone && acc.Len() > 0 {
		return "", fmt.Errorf("ollama stream incomplete: missing done=true after %d lines", lineNum)
	}

	return acc.String(), nil
}

func init() {
	Register(NewOllamaAgent(""))
}
