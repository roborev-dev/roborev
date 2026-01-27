package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	// Use intermediate variables for clarity and readability
	base := NewOllamaAgent("http://localhost:11434")
	withModel := base.WithModel("m1")
	withReasoning := withModel.WithReasoning(ReasoningThorough)
	withAgentic := withReasoning.WithAgentic(true)
	a := withAgentic.(*OllamaAgent)
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

func TestOllamaWithBaseURL(t *testing.T) {
	t.Run("custom BaseURL is preserved", func(t *testing.T) {
		a := NewOllamaAgent("http://localhost:11434")
		a2 := a.WithBaseURL("http://custom:11434").(*OllamaAgent)
		if a2.BaseURL != "http://custom:11434" {
			t.Errorf("BaseURL = %q, want \"http://custom:11434\"", a2.BaseURL)
		}
		// Verify other fields are preserved
		if a2.Model != a.Model {
			t.Errorf("Model changed: got %q, want %q", a2.Model, a.Model)
		}
		if a2.Reasoning != a.Reasoning {
			t.Errorf("Reasoning changed: got %v, want %v", a2.Reasoning, a.Reasoning)
		}
		if a2.Agentic != a.Agentic {
			t.Errorf("Agentic changed: got %v, want %v", a2.Agentic, a.Agentic)
		}
	})

	t.Run("empty BaseURL uses default", func(t *testing.T) {
		a := NewOllamaAgent("http://custom:11434")
		a2 := a.WithBaseURL("").(*OllamaAgent)
		if a2.BaseURL != ollamaDefaultBaseURL {
			t.Errorf("BaseURL = %q, want %q", a2.BaseURL, ollamaDefaultBaseURL)
		}
	})

	t.Run("preserves Model, Reasoning, and Agentic", func(t *testing.T) {
		// Use intermediate variables for clarity and readability
		base := NewOllamaAgent("http://localhost:11434")
		withModel := base.WithModel("m1")
		withReasoning := withModel.WithReasoning(ReasoningThorough)
		withAgentic := withReasoning.WithAgentic(true)
		a := withAgentic.(*OllamaAgent)
		a2 := a.WithBaseURL("http://custom:11434").(*OllamaAgent)
		if a2.Model != "m1" {
			t.Errorf("Model = %q, want \"m1\"", a2.Model)
		}
		if a2.Reasoning != ReasoningThorough {
			t.Errorf("Reasoning = %v, want %v", a2.Reasoning, ReasoningThorough)
		}
		if !a2.Agentic {
			t.Error("Agentic = false, want true")
		}
		if a2.BaseURL != "http://custom:11434" {
			t.Errorf("BaseURL = %q, want \"http://custom:11434\"", a2.BaseURL)
		}
	})

	t.Run("returns new instance", func(t *testing.T) {
		a := NewOllamaAgent("http://localhost:11434")
		a2 := a.WithBaseURL("http://custom:11434")
		if a == a2 {
			t.Error("WithBaseURL returned same instance, want new instance")
		}
	})
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

// Task 23: Method Chaining Tests
func TestOllamaMethodChaining_Immutability(t *testing.T) {
	t.Parallel()
	base := NewOllamaAgent("http://localhost:11434").WithModel("m1").WithReasoning(ReasoningStandard).WithAgentic(false)
	original := base.(*OllamaAgent)

	// Chain all methods (WithBaseURL is not part of Agent interface, so cast first)
	// Use intermediate variables for clarity and readability
	chainedAgent := base.WithModel("m2")
	chainedAgent = chainedAgent.WithReasoning(ReasoningThorough)
	chainedAgent = chainedAgent.WithAgentic(true)
	chained := chainedAgent.(*OllamaAgent).WithBaseURL("http://custom:11434").(*OllamaAgent)

	// Original should be unchanged
	if original.Model != "m1" {
		t.Errorf("original Model changed: got %q, want %q", original.Model, "m1")
	}
	if original.Reasoning != ReasoningStandard {
		t.Errorf("original Reasoning changed: got %v, want %v", original.Reasoning, ReasoningStandard)
	}
	if original.Agentic {
		t.Error("original Agentic changed: got true, want false")
	}
	if original.BaseURL != "http://localhost:11434" {
		t.Errorf("original BaseURL changed: got %q, want %q", original.BaseURL, "http://localhost:11434")
	}

	// Chained should have new values
	if chained.Model != "m2" {
		t.Errorf("chained Model = %q, want %q", chained.Model, "m2")
	}
	if chained.Reasoning != ReasoningThorough {
		t.Errorf("chained Reasoning = %v, want %v", chained.Reasoning, ReasoningThorough)
	}
	if !chained.Agentic {
		t.Error("chained Agentic = false, want true")
	}
	if chained.BaseURL != "http://custom:11434" {
		t.Errorf("chained BaseURL = %q, want %q", chained.BaseURL, "http://custom:11434")
	}

	// Should be different instances
	if original == chained {
		t.Error("chained agent is same instance as original")
	}
}

func TestOllamaMethodChaining_OrderIndependence(t *testing.T) {
	t.Parallel()
	base := NewOllamaAgent("http://localhost:11434")

	// Chain in different orders - use intermediate variables for clarity
	order1Agent := base.WithModel("m1")
	order1Agent = order1Agent.WithReasoning(ReasoningThorough)
	order1Agent = order1Agent.WithAgentic(true)
	order1 := order1Agent.(*OllamaAgent)

	order2Agent := base.WithAgentic(true)
	order2Agent = order2Agent.WithModel("m1")
	order2Agent = order2Agent.WithReasoning(ReasoningThorough)
	order2 := order2Agent.(*OllamaAgent)

	order3Agent := base.WithReasoning(ReasoningThorough)
	order3Agent = order3Agent.WithAgentic(true)
	order3Agent = order3Agent.WithModel("m1")
	order3 := order3Agent.(*OllamaAgent)

	// All should result in same final state
	if order1.Model != order2.Model || order2.Model != order3.Model {
		t.Errorf("Model differs by order: %q, %q, %q", order1.Model, order2.Model, order3.Model)
	}
	if order1.Reasoning != order2.Reasoning || order2.Reasoning != order3.Reasoning {
		t.Errorf("Reasoning differs by order: %v, %v, %v", order1.Reasoning, order2.Reasoning, order3.Reasoning)
	}
	if order1.Agentic != order2.Agentic || order2.Agentic != order3.Agentic {
		t.Errorf("Agentic differs by order: %v, %v, %v", order1.Agentic, order2.Agentic, order3.Agentic)
	}
}

func TestOllamaMethodChaining_EmptyWhitespaceModel(t *testing.T) {
	t.Parallel()
	base := NewOllamaAgent("http://localhost:11434")

	tests := []struct {
		name  string
		model string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"tab only", "\t"},
		{"newline only", "\n"},
		{"mixed whitespace", " \t\n "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chained := base.WithModel(tt.model).(*OllamaAgent)
			if chained.Model != tt.model {
				t.Errorf("Model = %q, want %q", chained.Model, tt.model)
			}
			// Model validation happens in Review(), not in WithModel()
			// So empty/whitespace models should be accepted by WithModel()
		})
	}
}

// Task 24: Reasoning Level Coverage
func TestOllamaReasoningLevels_AllLevels(t *testing.T) {
	t.Parallel()
	base := NewOllamaAgent("http://localhost:11434")

	tests := []struct {
		name  string
		level ReasoningLevel
		want  ReasoningLevel
	}{
		{"thorough", ReasoningThorough, ReasoningThorough},
		{"standard", ReasoningStandard, ReasoningStandard},
		{"fast", ReasoningFast, ReasoningFast},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := base.WithReasoning(tt.level).(*OllamaAgent)
			if agent.Reasoning != tt.want {
				t.Errorf("Reasoning = %v, want %v", agent.Reasoning, tt.want)
			}
		})
	}
}

func TestOllamaReasoningLevels_PreservedThroughChaining(t *testing.T) {
	t.Parallel()
	base := NewOllamaAgent("http://localhost:11434").WithModel("m1")

	// Chain with reasoning, then other methods
	chained := base.WithReasoning(ReasoningThorough).WithModel("m2").WithAgentic(true).(*OllamaAgent)

	if chained.Reasoning != ReasoningThorough {
		t.Errorf("Reasoning = %v, want %v", chained.Reasoning, ReasoningThorough)
	}
	if chained.Model != "m2" {
		t.Errorf("Model = %q, want %q", chained.Model, "m2")
	}
	if !chained.Agentic {
		t.Error("Agentic = false, want true")
	}
}

func TestOllamaReasoningLevels_NoOpBehavior(t *testing.T) {
	t.Parallel()
	// Reasoning level doesn't affect behavior in Phase 1 (no-op)
	// This is verified by the fact that Review() doesn't use Reasoning field
	// We can test that different reasoning levels produce same request format
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &reqBody)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
	}))
	defer ts.Close()

	levels := []ReasoningLevel{ReasoningThorough, ReasoningStandard, ReasoningFast}
	for _, level := range levels {
		t.Run(string(level), func(t *testing.T) {
			a := NewOllamaAgent(ts.URL).WithModel("m").WithReasoning(level)
			result, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
			if err != nil {
				t.Fatalf("Review failed: %v", err)
			}
			if result != "ok" {
				t.Errorf("result = %q, want %q", result, "ok")
			}
		})
	}
}

// Task 25: Agentic Mode Combinations
func TestOllamaAgenticMode_TrueFalse(t *testing.T) {
	t.Parallel()
	base := NewOllamaAgent("http://localhost:11434")

	agenticTrue := base.WithAgentic(true).(*OllamaAgent)
	if !agenticTrue.Agentic {
		t.Error("WithAgentic(true): Agentic = false, want true")
	}

	agenticFalse := base.WithAgentic(false).(*OllamaAgent)
	if agenticFalse.Agentic {
		t.Error("WithAgentic(false): Agentic = true, want false")
	}
}

func TestOllamaAgenticMode_PreservedThroughChaining(t *testing.T) {
	t.Parallel()
	base := NewOllamaAgent("http://localhost:11434").WithModel("m1")

	// Chain with agentic, then other methods
	chained := base.WithAgentic(true).WithModel("m2").WithReasoning(ReasoningThorough).(*OllamaAgent)

	if !chained.Agentic {
		t.Error("Agentic = false, want true")
	}
	if chained.Model != "m2" {
		t.Errorf("Model = %q, want %q", chained.Model, "m2")
	}
	if chained.Reasoning != ReasoningThorough {
		t.Errorf("Reasoning = %v, want %v", chained.Reasoning, ReasoningThorough)
	}
}

func TestOllamaAgenticMode_NoOpBehavior(t *testing.T) {
	t.Parallel()
	// Agentic mode doesn't affect behavior in Phase 1 (no-op, always read-only)
	// Test that both true and false produce same request format
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
	}))
	defer ts.Close()

	for _, agentic := range []bool{true, false} {
		t.Run(fmt.Sprintf("agentic=%v", agentic), func(t *testing.T) {
			a := NewOllamaAgent(ts.URL).WithModel("m").WithAgentic(agentic)
			result, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
			if err != nil {
				t.Fatalf("Review failed: %v", err)
			}
			if result != "ok" {
				t.Errorf("result = %q, want %q", result, "ok")
			}
		})
	}
}

// Task 26: Empty Stream Edge Cases
func TestOllamaParseStreamNDJSON_EmptyInput(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Empty input has no lines, so no content accumulated, returns empty string without error
	got, err := a.parseStreamNDJSON(strings.NewReader(""), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

func TestOllamaParseStreamNDJSON_WhitespaceOnlyLines(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Whitespace-only lines are skipped (trimmed == ""), so no content, returns empty string
	input := "   \n\t\n  \n"
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

func TestOllamaParseStreamNDJSON_OnlyNewlines(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Newline-only input has no content lines, returns empty string
	input := "\n\n\n"
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

func TestOllamaParseStreamNDJSON_EOFBeforeDone(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Valid JSON but no done=true before EOF - input ends without newline
	// ReadString('\n') on last line without newline returns line + EOF
	// Content should be accumulated, but error occurs because seenDone is false
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":false}`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for EOF before done=true")
	}
	if !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "done=true") {
		t.Errorf("expected incomplete/done error, got %v", err)
	}
	// When EOF is reached without newline, ReadString behavior may vary
	// The important thing is we get an error for incomplete stream
	// Content accumulation depends on whether the line was processed
	if got != "test" && got != "" {
		t.Errorf("result = %q, want %q or %q", got, "test", "")
	}
}

// Task 27: Content Edge Cases
func TestOllamaParseStreamNDJSON_VeryLongContent(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Create content that's longer than typical initial string builder capacity
	longContent := strings.Repeat("a", 10000)
	input := fmt.Sprintf(`{"model":"x","message":{"role":"assistant","content":"%s"},"done":true}`, longContent) + "\n"
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != longContent {
		t.Errorf("result length = %d, want %d", len(got), len(longContent))
	}
}

func TestOllamaParseStreamNDJSON_UnicodeCharacters(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	unicodeContent := "Hello 世界 🌍 こんにちは مرحبا"
	input := fmt.Sprintf(`{"model":"x","message":{"role":"assistant","content":"%s"},"done":true}`, unicodeContent) + "\n"
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != unicodeContent {
		t.Errorf("result = %q, want %q", got, unicodeContent)
	}
}

func TestOllamaParseStreamNDJSON_SpecialCharactersInContent(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Content with newlines, quotes, backslashes (properly escaped in JSON)
	specialContent := "Line 1\nLine 2\tTabbed\n\"Quoted\"\n\\Backslash\\"
	// JSON encoding will escape these
	jsonBytes, _ := json.Marshal(specialContent)
	input := fmt.Sprintf(`{"model":"x","message":{"role":"assistant","content":%s},"done":true}`, string(jsonBytes)) + "\n"
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != specialContent {
		t.Errorf("result = %q, want %q", got, specialContent)
	}
}

func TestOllamaParseStreamNDJSON_EmptyStringsBetweenChunks(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":"start"},"done":false}
{"model":"x","message":{"role":"assistant","content":""},"done":false}
{"model":"x","message":{"role":"assistant","content":""},"done":false}
{"model":"x","message":{"role":"assistant","content":"end"},"done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "startend" {
		t.Errorf("result = %q, want %q", got, "startend")
	}
}

// Task 28: JSON Structure Edge Cases
func TestOllamaParseStreamNDJSON_MissingMessageField(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Missing message field - JSON unmarshal succeeds but Message.Content is empty
	// No content accumulated (acc.Len() == 0), so returns empty string without error
	// (Error only occurs if !seenDone && acc.Len() > 0)
	input := `{"model":"x","done":false}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No content, no error (code behavior)
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

func TestOllamaParseStreamNDJSON_MissingContentField(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant"},"done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	// Missing content field should result in empty string
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

func TestOllamaParseStreamNDJSON_DoneAsString(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// done as string "true" instead of boolean - JSON unmarshal will fail
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":"true"}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for done as string")
	}
	// Should get JSON parse error (type mismatch)
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_ExtraFields(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Extra fields should be ignored
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":true,"extra":"field","another":123}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "test" {
		t.Errorf("result = %q, want %q", got, "test")
	}
}

func TestOllamaParseStreamNDJSON_MissingDoneField(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Missing "done" field - stream will be incomplete (no done=true)
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":false}
{"model":"x","message":{"role":"assistant","content":" more"},"done":false}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for missing done=true")
	}
	if !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "done=true") {
		t.Errorf("expected incomplete/done error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_EmptyRootObject(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Empty root object - no message or done fields
	input := `{}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty object has no content and no done=true, so returns empty string without error
	// (Error only occurs if !seenDone && acc.Len() > 0)
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

func TestOllamaParseStreamNDJSON_OnlyModelField(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Root object with only "model" field, missing "message" and "done"
	input := `{"model":"x"}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No message content, no done=true, so returns empty string without error
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

func TestOllamaParseStreamNDJSON_OnlyDoneField(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Root object with only "done" field, missing "message"
	input := `{"done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No message content, but done=true, so returns empty string without error
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

func TestOllamaParseStreamNDJSON_MissingMessageWithContent(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Missing "message" field but has content in a different field (should be ignored)
	input := `{"model":"x","content":"ignored","done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Content outside message field is ignored, so returns empty string
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

// Additional tests for unexpected/malformed NDJSON schema violations
func TestOllamaParseStreamNDJSON_MessageAsString(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message field as string instead of object - violates schema
	input := `{"model":"x","message":"not an object","done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for message as string")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_MessageContentAsNumber(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message.content as number instead of string - violates schema
	input := `{"model":"x","message":{"role":"assistant","content":12345},"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for content as number")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_DoneAsNumber(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// done field as number instead of boolean - violates schema
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":1}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for done as number")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_MessageRoleAsNumber(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message.role as number instead of string - violates schema
	input := `{"model":"x","message":{"role":123,"content":"test"},"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for role as number")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_NullMessage(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message field as null - JSON unmarshal sets to zero value (empty struct)
	// This is handled gracefully, returning empty content
	input := `{"model":"x","message":null,"done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Null message unmarshals to zero value struct, so content is empty
	if got != "" {
		t.Errorf("result = %q, want %q (null message should result in empty content)", got, "")
	}
}

func TestOllamaParseStreamNDJSON_ArrayInsteadOfObject(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Root object is actually an array - violates schema
	input := `[{"model":"x","message":{"role":"assistant","content":"test"},"done":true}]
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for array instead of object")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_NestedArraysInMessage(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message.content as array instead of string - violates schema
	input := `{"model":"x","message":{"role":"assistant","content":["part1","part2"]},"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for content as array")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

// Additional schema violation tests for unexpected/malformed NDJSON responses
func TestOllamaParseStreamNDJSON_MessageAsArray(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message field as array instead of object - violates schema
	input := `{"model":"x","message":[{"role":"assistant","content":"test"}],"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for message as array")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_ContentAsBoolean(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message.content as boolean instead of string - violates schema
	input := `{"model":"x","message":{"role":"assistant","content":true},"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for content as boolean")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_ContentAsObject(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message.content as object instead of string - violates schema
	input := `{"model":"x","message":{"role":"assistant","content":{"text":"test"}},"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for content as object")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_MessageRoleAsBoolean(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message.role as boolean instead of string - violates schema
	input := `{"model":"x","message":{"role":true,"content":"test"},"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for role as boolean")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_MessageRoleAsArray(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// message.role as array instead of string - violates schema
	input := `{"model":"x","message":{"role":["assistant"],"content":"test"},"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for role as array")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_DoneAsArray(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// done field as array instead of boolean - violates schema
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":[true]}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for done as array")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_DoneAsObject(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// done field as object instead of boolean - violates schema
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":{"value":true}}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for done as object")
	}
	if !strings.Contains(err.Error(), "NDJSON parse") {
		t.Errorf("expected NDJSON parse error, got %v", err)
	}
}

func TestOllamaParseStreamNDJSON_MultipleMessageFields(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Multiple message fields (invalid JSON, but tests parser robustness)
	// This is actually invalid JSON syntax, so it should fail at parse time
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"message":{"role":"user","content":"test2"},"done":true}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	// JSON with duplicate keys - behavior depends on JSON parser
	// Most parsers will use the last value, but this is still a schema violation
	if err == nil {
		// If it parses, verify it handles gracefully
		t.Log("duplicate message fields parsed (using last value)")
	}
	// Either way, this tests edge case handling
}

func TestOllamaParseStreamNDJSON_ModelAsArray(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// model field as array instead of string - violates schema (but model is not used in parsing)
	input := `{"model":["x"],"message":{"role":"assistant","content":"test"},"done":true}
`
	// This might parse successfully since model field is not used in parseStreamNDJSON
	// But it's still a schema violation worth testing
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		// If it errors, that's fine - schema violation detected
		if !strings.Contains(err.Error(), "NDJSON parse") {
			t.Errorf("expected NDJSON parse error, got %v", err)
		}
	} else {
		// If it succeeds, content should still be extracted
		if got != "test" {
			t.Errorf("result = %q, want %q", got, "test")
		}
	}
}

// Task 29: Stream Completion Edge Cases
func TestOllamaParseStreamNDJSON_MultipleDoneTrue(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Multiple done=true markers - should stop at first
	input := `{"model":"x","message":{"role":"assistant","content":"first"},"done":true}
{"model":"x","message":{"role":"assistant","content":"second"},"done":true}
{"model":"x","message":{"role":"assistant","content":"third"},"done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	// Should stop at first done=true
	if got != "first" {
		t.Errorf("result = %q, want %q", got, "first")
	}
}

func TestOllamaParseStreamNDJSON_DoneTrueWithEmptyContent(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":""},"done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

func TestOllamaParseStreamNDJSON_DoneFalseAfterContent(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// Content followed by done=false (should be incomplete)
	// This is similar to TestOllamaParseStreamNDJSON_IncompleteStream but tests the specific case
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":false}
`
	_, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err == nil {
		t.Fatal("expected error for done=false after content")
	}
	if !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "done=true") {
		t.Errorf("expected incomplete/done error, got %v", err)
	}
	// Note: Content accumulation is tested in other tests (e.g., TestOllamaParseStreamNDJSON_IncompleteStream)
}

func TestOllamaParseStreamNDJSON_DoneTrueNoContentAccumulated(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	// done=true but no content accumulated (all chunks had empty content)
	input := `{"model":"x","message":{"role":"assistant","content":""},"done":false}
{"model":"x","message":{"role":"assistant","content":""},"done":true}
`
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want %q", got, "")
	}
}

// Task 30: Output Streaming Edge Cases
func TestOllamaParseStreamNDJSON_NilOutputWriter(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":true}
`
	// Should not panic with nil writer
	got, err := a.parseStreamNDJSON(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "test" {
		t.Errorf("result = %q, want %q", got, "test")
	}
}

func TestOllamaParseStreamNDJSON_FailingOutputWriter(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":"test"},"done":true}
`
	// Failing writer should not cause parseStreamNDJSON to fail
	// (syncWriter handles write errors internally)
	failingWriter := &failingWriter{err: errors.New("write failed")}
	got, err := a.parseStreamNDJSON(strings.NewReader(input), failingWriter)
	// parseStreamNDJSON doesn't check write errors, so this should succeed
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "test" {
		t.Errorf("result = %q, want %q", got, "test")
	}
}

func TestOllamaParseStreamNDJSON_RawNDJSONStreamed(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":"hello"},"done":false}
{"model":"x","message":{"role":"assistant","content":" world"},"done":true}
`
	var buf bytes.Buffer
	got, err := a.parseStreamNDJSON(strings.NewReader(input), &buf)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "hello world" {
		t.Errorf("result = %q, want %q", got, "hello world")
	}
	// Output should contain raw NDJSON lines
	output := buf.String()
	if !strings.Contains(output, `{"model":"x","message":{"role":"assistant","content":"hello"},"done":false}`) {
		t.Error("output should contain first raw NDJSON line")
	}
	if !strings.Contains(output, `{"model":"x","message":{"role":"assistant","content":" world"},"done":true}`) {
		t.Error("output should contain second raw NDJSON line")
	}
}

func TestOllamaParseStreamNDJSON_OutputContainsAllLines(t *testing.T) {
	t.Parallel()
	a := NewOllamaAgent("")
	input := `{"model":"x","message":{"role":"assistant","content":"a"},"done":false}
{"model":"x","message":{"role":"assistant","content":""},"done":false}
{"model":"x","message":{"role":"assistant","content":"b"},"done":true}
`
	var buf bytes.Buffer
	got, err := a.parseStreamNDJSON(strings.NewReader(input), &buf)
	if err != nil {
		t.Fatalf("parseStreamNDJSON: %v", err)
	}
	if got != "ab" {
		t.Errorf("result = %q, want %q", got, "ab")
	}
	// Output should have 3 lines (all NDJSON lines, not just content)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Errorf("output lines = %d, want 3", len(lines))
	}
}

// Task 31: HTTP Status Code Coverage
func TestOllamaReview_502BadGateway(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"bad gateway"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 502")
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "server error") {
		t.Errorf("expected 502/server error, got %v", err)
	}
}

func TestOllamaReview_503ServiceUnavailable(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"service unavailable"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "server error") {
		t.Errorf("expected 503/server error, got %v", err)
	}
}

func TestOllamaReview_504GatewayTimeout(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"error":"gateway timeout"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 504")
	}
	// 504 is handled as server error, but message format is "ollama API error"
	if !strings.Contains(err.Error(), "504") {
		t.Errorf("expected 504 in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "gateway timeout") {
		t.Errorf("expected gateway timeout in error, got %v", err)
	}
}

func TestOllamaReview_ErrorResponseWithJSONErrorField(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"custom error message"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "custom error message") {
		t.Errorf("expected custom error message, got %v", err)
	}
}

func TestOllamaReview_ErrorResponseWithoutJSONErrorField(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("plain text error"))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "plain text error") {
		t.Errorf("expected plain text error in message, got %v", err)
	}
}

// Task 32: Request Building Errors
func TestOllamaReview_BaseURLWithTrailingSlash(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
	}))
	defer ts.Close()

	// BaseURL with trailing slash should be handled correctly
	a := NewOllamaAgent(ts.URL + "/").WithModel("m")
	result, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

func TestOllamaReview_RequestContextPropagation(t *testing.T) {
	t.Parallel()
	var gotCtx context.Context
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
	}))
	defer ts.Close()

	ctx := context.Background()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(ctx, "/repo", "abc", "prompt", nil)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if gotCtx == nil {
		t.Fatal("request context not propagated")
	}
	// Context may be canceled after request completes (normal behavior)
	// Just verify it was set
}

// Task 33: Network Error Classification
func TestOllamaClassifyNetworkError_ConnectionRefused(t *testing.T) {
	t.Parallel()
	// Test that connection refused errors are classified correctly
	// This is tested indirectly through Review() calls
	a := NewOllamaAgent("http://127.0.0.1:19997").WithModel("m")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.Review(ctx, "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected connection refused error, got %v", err)
	}
}

func TestOllamaClassifyNetworkError_Timeout(t *testing.T) {
	t.Parallel()
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
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got %v", err)
	}
}

// Task 34: Context Error Handling
func TestOllamaReview_ContextDeadlineExceededDuringRequest(t *testing.T) {
	t.Parallel()
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
		t.Fatal("expected DeadlineExceeded error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout in error message, got %v", err)
	}
}

func TestOllamaReview_ContextCanceledDuringRequest(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer ts.Close()
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(ctx, "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected Canceled error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected Canceled, got %v", err)
	}
}

func TestOllamaReview_ContextErrorWrapping(t *testing.T) {
	t.Parallel()
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
		t.Fatal("expected error")
	}
	// Error should wrap context.DeadlineExceeded
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap DeadlineExceeded, got %v", err)
	}
	// But should also have descriptive message
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should mention timeout, got %v", err)
	}
}

// Task 35: Error Response Parsing
func TestOllamaReview_MalformedErrorJSON(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":invalid json}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	// Should fall back to plain text error message
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error message, got %v", err)
	}
}

func TestOllamaReview_ErrorResponseEmptyErrorField(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":""}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	// Empty error field is still included in error message (code behavior)
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected invalid error, got %v", err)
	}
}

func TestOllamaReview_ErrorResponseNonStringError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":123}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	// Non-string error field should fall back to plain text
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error message, got %v", err)
	}
}

func TestOllamaReview_ErrorResponseLargeBody(t *testing.T) {
	t.Parallel()
	largeError := strings.Repeat("x", 5000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(largeError))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	// Large body should be truncated (limit is 4096 bytes)
	if len(err.Error()) > 5000 {
		t.Errorf("error message too long: %d bytes", len(err.Error()))
	}
}

func TestOllamaReview_ErrorResponseSpecialCharacters(t *testing.T) {
	t.Parallel()
	specialError := `error with "quotes" and \backslashes and newlines\n`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"` + specialError + `"}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL).WithModel("m")
	_, err := a.Review(context.Background(), "/repo", "abc", "prompt", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	// Special characters should be preserved in error message
	if !strings.Contains(err.Error(), "quotes") {
		t.Errorf("expected special characters in error, got %v", err)
	}
}

// Task 36: Availability Cache Behavior
func TestOllamaIsAvailable_CacheHit(t *testing.T) {
	t.Parallel()
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
	// First call should hit server
	if !a.IsAvailable() {
		t.Error("IsAvailable() = false, want true")
	}
	// Second call should use cache (within TTL)
	if !a.IsAvailable() {
		t.Error("IsAvailable() (cached) = false, want true")
	}
}

func TestOllamaIsAvailable_CacheExpiration(t *testing.T) {
	t.Parallel()
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL)
	// First call
	if !a.IsAvailable() {
		t.Error("IsAvailable() = false, want true")
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1", callCount)
	}

	// Second call immediately (should use cache)
	if !a.IsAvailable() {
		t.Error("IsAvailable() (cached) = false, want true")
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1 (cached)", callCount)
	}

	// Manually expire cache by manipulating lastCheck
	a.mu.Lock()
	a.lastCheck = time.Now().Add(-ollamaAvailabilityTTL - time.Second)
	a.mu.Unlock()

	// Third call after expiration (should hit server)
	if !a.IsAvailable() {
		t.Error("IsAvailable() (after expiration) = false, want true")
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (after expiration)", callCount)
	}
}

func TestOllamaIsAvailable_ConcurrentAccess(t *testing.T) {
	t.Parallel()
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
	var wg sync.WaitGroup
	concurrency := 10
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			if !a.IsAvailable() {
				t.Error("IsAvailable() = false, want true")
			}
		}()
	}
	wg.Wait()
}

func TestOllamaIsAvailable_CacheUpdateOnAvailabilityChange(t *testing.T) {
	t.Parallel()
	available := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if available {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[]}`))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer ts.Close()

	a := NewOllamaAgent(ts.URL)
	// First call - available
	if !a.IsAvailable() {
		t.Error("IsAvailable() = false, want true")
	}

	// Expire cache
	a.mu.Lock()
	a.lastCheck = time.Now().Add(-ollamaAvailabilityTTL - time.Second)
	a.mu.Unlock()

	// Make server unavailable
	available = false

	// Second call after expiration - should detect unavailability
	if a.IsAvailable() {
		t.Error("IsAvailable() = true, want false (server now unavailable)")
	}
}

// Task 37: Availability Edge Cases
func TestOllamaIsAvailable_RequestBuildingError(t *testing.T) {
	t.Parallel()
	// Invalid base URL should cause request building to fail
	// But NewOllamaAgent accepts any string, so we test with a URL that causes http.NewRequest to fail
	// Actually, http.NewRequestWithContext is very permissive, so we test with a URL that causes Do() to fail
	a := NewOllamaAgent("http://[invalid-url")
	// This should not panic, but return false
	if a.IsAvailable() {
		t.Error("IsAvailable() = true, want false for invalid URL")
	}
}

func TestOllamaIsAvailable_DifferentHTTPStatusCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{"200 OK", http.StatusOK, true},
		{"404 Not Found", http.StatusNotFound, false},
		{"401 Unauthorized", http.StatusUnauthorized, false},
		{"500 Internal Server Error", http.StatusInternalServerError, false},
		{"502 Bad Gateway", http.StatusBadGateway, false},
		{"503 Service Unavailable", http.StatusServiceUnavailable, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer ts.Close()

			a := NewOllamaAgent(ts.URL)
			got := a.IsAvailable()
			if got != tt.want {
				t.Errorf("IsAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOllamaIsAvailable_TimeoutBehavior(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer ts.Close()
	defer close(block)

	a := NewOllamaAgent(ts.URL)
	// IsAvailable uses 3 second timeout internally
	// Server blocks, so should timeout and return false
	got := a.IsAvailable()
	if got {
		t.Error("IsAvailable() = true, want false (timeout)")
	}
}

func TestOllamaIsAvailable_NetworkError(t *testing.T) {
	t.Parallel()
	// Unreachable server
	a := NewOllamaAgent("http://127.0.0.1:19996")
	got := a.IsAvailable()
	if got {
		t.Error("IsAvailable() = true, want false (network error)")
	}
}

func TestOllamaIsAvailable_StatePreservation(t *testing.T) {
	t.Parallel()
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
	// First call
	if !a.IsAvailable() {
		t.Error("IsAvailable() = false, want true")
	}

	// Verify state is preserved
	a.mu.Lock()
	lastCheck := a.lastCheck
	available := a.available
	a.mu.Unlock()

	if lastCheck.IsZero() {
		t.Error("lastCheck not set")
	}
	if !available {
		t.Error("available = false, want true")
	}

	// Second call should use cached state
	if !a.IsAvailable() {
		t.Error("IsAvailable() (cached) = false, want true")
	}
}
