//go:build ollama_integration

package agent

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// checkOllamaAvailable checks if Ollama server is reachable at the given base URL
func checkOllamaAvailable(baseURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/api/tags", nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// getOllamaBaseURL returns the Ollama base URL from environment or default
func getOllamaBaseURL() string {
	baseURL := os.Getenv("OLLAMA_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return baseURL
}

// getOllamaModel returns the Ollama model from environment or default
func getOllamaModel() string {
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "llama3.2:3b" // Small default model for testing
	}
	return model
}

// TestOllamaIntegration_RealServerAvailable skips if Ollama server is not available
func TestOllamaIntegration_RealServerAvailable(t *testing.T) {
	baseURL := getOllamaBaseURL()
	if !checkOllamaAvailable(baseURL) {
		t.Skipf("Ollama server not available at %s (set OLLAMA_BASE_URL to test with remote server)", baseURL)
	}

	// If we get here, server is available
	a := NewOllamaAgent(baseURL)
	if !a.IsAvailable() {
		t.Error("IsAvailable() should return true when server is reachable")
	}
}

// TestOllamaIntegration_ReviewWithRealServer tests actual review with real Ollama server
func TestOllamaIntegration_ReviewWithRealServer(t *testing.T) {
	baseURL := getOllamaBaseURL()
	if !checkOllamaAvailable(baseURL) {
		t.Skipf("Ollama server not available at %s", baseURL)
	}

	model := getOllamaModel()
	a := NewOllamaAgent(baseURL).WithModel(model)

	// Simple prompt for testing
	prompt := "Review this code: func add(a, b int) int { return a + b }"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var outputBuf strings.Builder
	result, err := a.Review(ctx, "/test/repo", "testsha123", prompt, &outputBuf)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	if result == "" {
		t.Error("Review result should not be empty")
	}

	// Verify output was streamed
	if outputBuf.Len() == 0 {
		t.Error("Output should have been streamed to writer")
	}

	// Verify result contains accumulated content
	if len(result) < 10 {
		t.Errorf("Review result seems too short: %q", result)
	}
}

// TestOllamaIntegration_ModelValidation tests model validation via /api/tags
func TestOllamaIntegration_ModelValidation(t *testing.T) {
	baseURL := getOllamaBaseURL()
	if !checkOllamaAvailable(baseURL) {
		t.Skipf("Ollama server not available at %s", baseURL)
	}

	model := getOllamaModel()
	agent := NewOllamaAgent(baseURL).WithModel(model)

	// Test that IsAvailable works (type assert to *OllamaAgent)
	a, ok := agent.(*OllamaAgent)
	if !ok {
		t.Fatal("Expected *OllamaAgent")
	}
	if !a.IsAvailable() {
		t.Error("IsAvailable() should return true when server is reachable")
	}

	// Test review with valid model
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	prompt := "Say hello"
	result, err := agent.Review(ctx, "/test/repo", "testsha", prompt, nil)
	if err != nil {
		t.Fatalf("Review with valid model failed: %v", err)
	}

	if result == "" {
		t.Error("Review result should not be empty")
	}
}

// TestOllamaIntegration_Streaming tests real streaming response
func TestOllamaIntegration_Streaming(t *testing.T) {
	baseURL := getOllamaBaseURL()
	if !checkOllamaAvailable(baseURL) {
		t.Skipf("Ollama server not available at %s", baseURL)
	}

	model := getOllamaModel()
	a := NewOllamaAgent(baseURL).WithModel(model)

	prompt := "Count from 1 to 5, one number per line."

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var chunks []string
	var outputBuf strings.Builder

	// Capture streaming output
	result, err := a.Review(ctx, "/test/repo", "testsha", prompt, &outputBuf)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	// Verify output was streamed (should contain raw NDJSON lines)
	output := outputBuf.String()
	if output == "" {
		t.Error("Streaming output should not be empty")
	}

	// Output should contain NDJSON lines (check for JSON structure)
	if !strings.Contains(output, `"model"`) && !strings.Contains(output, `"message"`) {
		t.Logf("Output may not be in expected NDJSON format: %q", output)
	}

	// Verify result contains accumulated content
	if result == "" {
		t.Error("Review result should not be empty")
	}

	// Verify chunks were accumulated (result should be longer than any single chunk)
	if len(result) < 10 {
		t.Errorf("Accumulated result seems too short: %q", result)
	}

	_ = chunks // Avoid unused variable
}

// TestOllamaIntegration_ErrorHandling tests real error scenarios
func TestOllamaIntegration_ErrorHandling(t *testing.T) {
	baseURL := getOllamaBaseURL()
	if !checkOllamaAvailable(baseURL) {
		t.Skipf("Ollama server not available at %s", baseURL)
	}

	t.Run("missing model returns error", func(t *testing.T) {
		a := NewOllamaAgent(baseURL).WithModel("nonexistent-model-12345")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err := a.Review(ctx, "/test/repo", "testsha", "test prompt", nil)
		if err == nil {
			t.Fatal("Expected error for nonexistent model")
		}

		// Verify error message is helpful
		if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "pull") {
			t.Errorf("Error message should mention model not found or pull, got: %v", err)
		}
	})

	t.Run("empty model returns error", func(t *testing.T) {
		a := NewOllamaAgent(baseURL).WithModel("")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := a.Review(ctx, "/test/repo", "testsha", "test prompt", nil)
		if err == nil {
			t.Fatal("Expected error for empty model")
		}

		if !strings.Contains(err.Error(), "model not configured") {
			t.Errorf("Error message should mention model not configured, got: %v", err)
		}
	})
}

// TestOllamaIntegration_RemoteServer tests with remote Ollama server if configured
func TestOllamaIntegration_RemoteServer(t *testing.T) {
	baseURL := os.Getenv("OLLAMA_BASE_URL")
	if baseURL == "" {
		t.Skip("OLLAMA_BASE_URL not set, skipping remote server test")
	}

	// Only test if it's not localhost (indicating remote server)
	if strings.Contains(baseURL, "localhost") || strings.Contains(baseURL, "127.0.0.1") {
		t.Skip("OLLAMA_BASE_URL points to localhost, skipping remote server test")
	}

	if !checkOllamaAvailable(baseURL) {
		t.Skipf("Remote Ollama server not available at %s", baseURL)
	}

	model := getOllamaModel()
	agent := NewOllamaAgent(baseURL).WithModel(model)

	// Test availability check (type assert to *OllamaAgent)
	a, ok := agent.(*OllamaAgent)
	if !ok {
		t.Fatal("Expected *OllamaAgent")
	}
	if !a.IsAvailable() {
		t.Error("IsAvailable() should return true for remote server")
	}

	// Test review with remote server
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	prompt := "Say hello"
	result, err := agent.Review(ctx, "/test/repo", "testsha", prompt, nil)
	if err != nil {
		t.Fatalf("Review with remote server failed: %v", err)
	}

	if result == "" {
		t.Error("Review result should not be empty")
	}
}

// TestOllamaIntegration_ContextTimeout tests context timeout handling
func TestOllamaIntegration_ContextTimeout(t *testing.T) {
	baseURL := getOllamaBaseURL()
	if !checkOllamaAvailable(baseURL) {
		t.Skipf("Ollama server not available at %s", baseURL)
	}

	model := getOllamaModel()
	a := NewOllamaAgent(baseURL).WithModel(model)

	// Use a very short timeout to trigger timeout error
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Use a prompt that might take longer than 100ms
	prompt := "Write a detailed analysis of this code: " + strings.Repeat("func test() { } ", 100)

	_, err := a.Review(ctx, "/test/repo", "testsha", prompt, nil)
	if err == nil {
		t.Fatal("Expected timeout error")
	}

	// Verify it's a timeout error
	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

// TestOllamaIntegration_BaseURLConfiguration tests different base URL configurations
func TestOllamaIntegration_BaseURLConfiguration(t *testing.T) {
	baseURL := getOllamaBaseURL()
	if !checkOllamaAvailable(baseURL) {
		t.Skipf("Ollama server not available at %s", baseURL)
	}

	model := getOllamaModel()

	t.Run("default base URL", func(t *testing.T) {
		a := NewOllamaAgent("")
		if a.BaseURL != "http://localhost:11434" {
			t.Errorf("Expected default base URL, got %q", a.BaseURL)
		}
	})

	t.Run("custom base URL", func(t *testing.T) {
		a := NewOllamaAgent(baseURL)
		if a.BaseURL != baseURL {
			t.Errorf("Expected base URL %q, got %q", baseURL, a.BaseURL)
		}
	})

	t.Run("base URL with trailing slash", func(t *testing.T) {
		baseURLWithSlash := baseURL + "/"
		a := NewOllamaAgent(baseURLWithSlash)
		// BaseURL should preserve trailing slash, but Review() should handle it
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		result, err := a.WithModel(model).Review(ctx, "/test/repo", "testsha", "Say hello", nil)
		if err != nil {
			t.Fatalf("Review with trailing slash base URL failed: %v", err)
		}
		if result == "" {
			t.Error("Review result should not be empty")
		}
	})
}

// TestOllamaIntegration_AvailabilityCache tests availability caching behavior
func TestOllamaIntegration_AvailabilityCache(t *testing.T) {
	baseURL := getOllamaBaseURL()
	if !checkOllamaAvailable(baseURL) {
		t.Skipf("Ollama server not available at %s", baseURL)
	}

	a := NewOllamaAgent(baseURL)

	// First call should hit server
	if !a.IsAvailable() {
		t.Error("IsAvailable() should return true")
	}

	// Second call immediately should use cache
	if !a.IsAvailable() {
		t.Error("IsAvailable() (cached) should return true")
	}

	// Wait for cache to expire (30 seconds + 1 second buffer)
	time.Sleep(ollamaAvailabilityTTL + 1*time.Second)

	// Third call after expiration should hit server again
	if !a.IsAvailable() {
		t.Error("IsAvailable() (after expiration) should return true")
	}
}
