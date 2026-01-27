package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	_ = resp.Body.Close()
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
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("ollama server not reachable (is it running? start with: ollama serve): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slurp, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(slurp))
		if resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("model %q not found; pull with: ollama pull %s", a.Model, a.Model)
		}
		return "", fmt.Errorf("ollama API %s: %s", resp.Status, msg)
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

// parseStreamNDJSON reads NDJSON from r, streams raw lines to output, and returns accumulated message.content.
func (a *OllamaAgent) parseStreamNDJSON(r io.Reader, output io.Writer) (string, error) {
	br := bufio.NewReader(r)
	var acc strings.Builder
	var seenDone bool
	sw := newSyncWriter(output)

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("ollama read stream: %w", err)
		}

		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if sw != nil {
				sw.Write([]byte(trimmed + "\n"))
			}
			var chunk ollamaStreamChunk
			if jsonErr := json.Unmarshal([]byte(trimmed), &chunk); jsonErr != nil {
				return "", fmt.Errorf("ollama NDJSON parse: %w", jsonErr)
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
		return "", fmt.Errorf("ollama stream incomplete: missing done=true")
	}

	return acc.String(), nil
}

func init() {
	Register(NewOllamaAgent(""))
}
