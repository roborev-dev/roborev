package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestOllamaReview_401Unauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "authentication failed") && !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("expected authentication/401/Unauthorized in error, got %v", err)
	}
}

func TestOllamaReview_200UnexpectedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("plain text response"))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error when response is not valid NDJSON")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
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
	// Enhanced error message should mention connection refused
	if !strings.Contains(err.Error(), "connection refused") && !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("expected connection refused error, got %v", err)
	}
	if !strings.Contains(err.Error(), "ollama serve") {
		t.Errorf("expected troubleshooting hint, got %v", err)
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

func TestOllamaIsAvailable_404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL)
	if a.IsAvailable() {
		t.Error("IsAvailable() = true, want false when /api/tags returns 404")
	}
}

func TestOllamaIsAvailable_401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL)
	if a.IsAvailable() {
		t.Error("IsAvailable() = true, want false when /api/tags returns 401")
	}
}

func TestOllamaImplementsAvailabilityChecker(t *testing.T) {
	var _ AvailabilityChecker = (*OllamaAgent)(nil)
}

func TestOllamaReview_400BadRequest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid model format"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("invalid:model:format")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "invalid model") {
		t.Errorf("expected invalid model error, got %v", err)
	}
	if !strings.Contains(err.Error(), "invalid:model:format") {
		t.Errorf("expected model name in error, got %v", err)
	}
}

func TestOllamaReview_404WithErrorResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'missing-model' not found, try pulling it first"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("missing-model")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not found error, got %v", err)
	}
	if !strings.Contains(err.Error(), "ollama pull") {
		t.Errorf("expected pull hint, got %v", err)
	}
	// Should include Ollama's error message
	if !strings.Contains(err.Error(), "try pulling it first") {
		t.Errorf("expected Ollama error message, got %v", err)
	}
}

func TestOllamaReview_500ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error: model loading failed"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "server error") || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected server error, got %v", err)
	}
	if !strings.Contains(err.Error(), "model loading failed") {
		t.Errorf("expected Ollama error message, got %v", err)
	}
}

func TestOllamaReview_ContextTimeout(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer ts.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(ctx, "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error when context timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout in error message, got %v", err)
	}
}

func TestOllamaReview_TimeoutVsCancel(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer ts.Close()
	defer close(block)

	// Test context cancellation (not timeout)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(ctx, "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error when context canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected Canceled, got %v", err)
	}
}

func TestOllamaReview_DNSError(t *testing.T) {
	// Use an invalid hostname that will cause DNS error
	// Note: DNS errors may be caught as timeouts if context expires first
	a := NewOllamaAgent("http://nonexistent-hostname-that-does-not-exist-12345.local:11434").WithModel("m")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.Review(ctx, "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for DNS failure")
	}
	// DNS errors may appear as timeout or DNS error depending on timing
	// Accept either as valid since DNS resolution can timeout
	if !strings.Contains(err.Error(), "DNS") && !strings.Contains(err.Error(), "resolve") && !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected DNS/resolve/timeout error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_SyntaxErrorWithOffset(t *testing.T) {
	a := NewOllamaAgent("")
	// Create JSON with syntax error (missing closing brace)
	input := `{"model":"x","message":{"role":"assistant","content":"ok"},"done":false}
{"model":"x","message":{"role":"assistant","content":"test"
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for syntax error")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
	// Should mention line number
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("expected line number in error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_MalformedWithContext(t *testing.T) {
	a := NewOllamaAgent("")
	// Create malformed JSON
	input := `{"model":"x","message":{"role":"assistant","content":"ok"},"done":false}
not json at all
{"model":"x","message":{"role":"assistant","content":"test"},"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
	// Should mention line number
	if !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "line") {
		t.Errorf("expected line number in error, got %v", err)
	}
	// Should show content preview
	if !strings.Contains(err.Error(), "not json") {
		t.Errorf("expected content preview in error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_InvalidStructure(t *testing.T) {
	a := NewOllamaAgent("")
	// Valid JSON but wrong structure - missing required fields causes unmarshal to succeed
	// but stream will be incomplete (no done=true with content)
	// Actually, this will succeed but return empty content, so test incomplete stream instead
	input := `{"model":"x","message":{"role":"assistant","content":""},"done":false}
{"model":"x","message":{"role":"assistant","content":""},"done":false}
`
	// This should succeed but return empty string (no error, just no content)
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty result for empty content chunks, got %q", got)
	}
	// Test actual invalid structure - malformed JSON that parses but has wrong type
	invalidInput := `{"model":"x","message":"not an object","done":false}
`
	_, err = a.parseStreamNDJSON(strings.NewReader(invalidInput), nil)
	if err == nil {
		t.Fatal("expected error for invalid message structure")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
	// Should mention line number
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("expected line number in error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_IncompleteWithLineCount(t *testing.T) {
	a := NewOllamaAgent("")
	// Stream that ends without done=true
	input := `{"model":"x","message":{"role":"assistant","content":"partial"},"done":false}
{"model":"x","message":{"role":"assistant","content":"more"},"done":false}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for incomplete stream")
	}
	if !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "done=true") {
		t.Errorf("expected incomplete/done error, got %v", err)
	}
	// Should mention line count
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("expected line count in error, got %v", err)
	}
}

func TestOllamaReview_NetworkUnreachable(t *testing.T) {
	// This test is harder to simulate reliably, but we can test the error classification
	// by checking that network errors are properly wrapped
	a := NewOllamaAgent("http://192.0.2.1:11434").WithModel("m") // 192.0.2.1 is TEST-NET-1, should be unreachable
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := a.Review(ctx, "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for unreachable host")
	}
	// Should have a network-related error message
	if !strings.Contains(err.Error(), "network") && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("expected network error, got %v", err)
	}
}
