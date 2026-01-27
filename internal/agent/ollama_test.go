package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOllamaNewAgent(t *testing.T) {
	a := NewOllamaAgent("")
	if a.BaseURL != ollamaDefaultBaseURL {
		t.Errorf("empty baseURL: got %q, want %q", a.BaseURL, ollamaDefaultBaseURL)
	}
	if a.Reasoning != ReasoningStandard {
		t.Errorf("default reasoning: got %v", a.Reasoning)
	}

	a2 := NewOllamaAgent("http://custom:11434")
	if a2.BaseURL != "http://custom:11434" {
		t.Errorf("custom baseURL: got %q", a2.BaseURL)
	}
}

func TestOllamaName(t *testing.T) {
	a := NewOllamaAgent("")
	if got := a.Name(); got != "ollama" {
		t.Errorf("Name() = %q, want \"ollama\"", got)
	}
}

func TestOllamaWithModelReasoningAgentic(t *testing.T) {
	a := NewOllamaAgent("http://localhost:11434").WithModel("m1").WithReasoning(ReasoningThorough).WithAgentic(true).(*OllamaAgent)
	if a.Model != "m1" {
		t.Errorf("Model = %q, want \"m1\"", a.Model)
	}
	if a.Reasoning != ReasoningThorough {
		t.Errorf("Reasoning = %v", a.Reasoning)
	}
	if !a.Agentic {
		t.Error("Agentic = false, want true")
	}
}

func TestOllamaParseStreamNDJSON_ValidStream(t *testing.T) {
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":"Hello "},"done":false}
{"model":"x","message":{"role":"assistant","content":"world"},"done":true}
`
	var out bytes.Buffer
	got, err := a.parseStreamNDJSON(strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "Hello world" {
		t.Errorf("result = %q, want \"Hello world\"", got)
	}
	if out.Len() == 0 {
		t.Error("expected raw lines streamed to output")
	}
}

func TestOllamaParseStreamNDJSON_EmptyContentChunks(t *testing.T) {
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":""},"done":false}
{"model":"x","message":{"role":"assistant","content":""},"done":false}
{"model":"x","message":{"role":"assistant","content":"only"},"done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "only" {
		t.Errorf("result = %q, want \"only\"", got)
	}
}

func TestOllamaParseStreamNDJSON_OnlyDoneTrue(t *testing.T) {
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":""},"done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want \"\"", got)
	}
}

func TestOllamaParseStreamNDJSON_MalformedLine(t *testing.T) {
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":"ok"},"done":false}
not json
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_IncompleteStream(t *testing.T) {
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":"partial"},"done":false}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for incomplete stream")
	}
	if !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "done=true") {
		t.Errorf("expected incomplete/done error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_StreamsToOutput(t *testing.T) {
	a := NewOllamaAgent("")
	input := `{"model":"m","message":{"role":"assistant","content":"hi"},"done":true}
`
	var buf bytes.Buffer
	got, err := a.parseStreamNDJSON(strings.NewReader(input), &buf)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "hi" {
		t.Errorf("result = %q, want \"hi\"", got)
	}
	if buf.Len() == 0 {
		t.Error("expected output to be written")
	}
	if !strings.Contains(buf.String(), "hi") {
		t.Errorf("output should contain streamed line with content, got %q", buf.String())
	}
}

func TestOllamaReview_ModelRequired(t *testing.T) {
	a := NewOllamaAgent("")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error when model not set")
	}
	if !strings.Contains(err.Error(), "model not configured") {
		t.Errorf("expected model not configured error, got %v", err)
	}
}

func TestOllamaReview_RequestFormat(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("qwen2.5-coder:32b")
	result, err := a.Review(context.Background(), "/repo", "abc", "Review this code.", nil)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want \"ok\"", result)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/chat" {
		t.Errorf("path = %q, want /api/chat", gotPath)
	}

	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.Model != "qwen2.5-coder:32b" {
		t.Errorf("model = %q, want qwen2.5-coder:32b", req.Model)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "Review this code." {
		t.Errorf("messages = %+v", req.Messages)
	}
	if req.Stream == nil || !*req.Stream {
		t.Errorf("stream = %v, want true", req.Stream)
	}
}

func TestOllamaReview_404ModelNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("missing-model")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "ollama pull") {
		t.Errorf("expected model not found / pull hint, got %v", err)
	}
}

func TestOllamaReview_5xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 5xx")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error, got %v", err)
	}
}

func TestOllamaReview_ContextCanceled(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer ts.Close()
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(ctx, "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error when context canceled")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("expected context canceled, got %v", err)
	}
}

func TestOllamaReview_ConnectionRefused(t *testing.T) {
	// Use a URL that will fail to connect (nothing listening)
	a := NewOllamaAgent("http://127.0.0.1:19999").WithModel("m")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.Review(ctx, "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error when server unreachable")
	}
	if !strings.Contains(err.Error(), "not reachable") && !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected reachable/connection error, got %v", err)
	}
}

func TestOllamaIsAvailable_MockServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL)
	if !a.IsAvailable() {
		t.Error("IsAvailable() = false, want true when server returns 200")
	}
	// Cache hit
	if !a.IsAvailable() {
		t.Error("IsAvailable() (cached) = false, want true")
	}
}

func TestOllamaIsAvailable_ServerDown(t *testing.T) {
	a := NewOllamaAgent("http://127.0.0.1:19998")
	if a.IsAvailable() {
		t.Error("IsAvailable() = true, want false when server unreachable")
	}
}

func TestOllamaIsAvailable_5xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL)
	if a.IsAvailable() {
		t.Error("IsAvailable() = true, want false when /api/tags returns 5xx")
	}
}

func TestOllamaImplementsAvailabilityChecker(t *testing.T) {
	var _ AvailabilityChecker = (*OllamaAgent)(nil)
}
