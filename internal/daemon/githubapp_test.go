package daemon

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func generateTestKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, string(pemBytes)
}

func generateTestKeyPKCS8(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})
	return key, string(pemBytes)
}

func TestParsePrivateKey_PKCS1(t *testing.T) {
	_, pemData := generateTestKey(t)
	key, err := parsePrivateKey([]byte(pemData))
	if err != nil {
		t.Fatalf("parse PKCS1: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}
}

func TestParsePrivateKey_PKCS8(t *testing.T) {
	_, pemData := generateTestKeyPKCS8(t)
	key, err := parsePrivateKey([]byte(pemData))
	if err != nil {
		t.Fatalf("parse PKCS8: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}
}

func TestParsePrivateKey_Invalid(t *testing.T) {
	_, err := parsePrivateKey([]byte("not a PEM"))
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
	if !strings.Contains(err.Error(), "no PEM block") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSignJWT_Structure(t *testing.T) {
	_, pemData := generateTestKey(t)
	tp, err := NewGitHubAppTokenProvider(12345, pemData)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	jwt, err := tp.signJWT()
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}

	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}

	// Decode and verify header
	headerBytes, err := base64URLDecode(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatalf("parse header: %v", err)
	}
	if header["alg"] != "RS256" {
		t.Errorf("expected alg RS256, got %s", header["alg"])
	}
	if header["typ"] != "JWT" {
		t.Errorf("expected typ JWT, got %s", header["typ"])
	}

	// Decode and verify payload
	payloadBytes, err := base64URLDecode(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("parse payload: %v", err)
	}

	iss, ok := payload["iss"].(float64)
	if !ok || int64(iss) != 12345 {
		t.Errorf("expected iss=12345, got %v", payload["iss"])
	}

	iat, ok := payload["iat"].(float64)
	if !ok {
		t.Fatal("missing iat")
	}
	exp, ok := payload["exp"].(float64)
	if !ok {
		t.Fatal("missing exp")
	}

	// iat should be ~60s in the past, exp ~10min in the future
	now := float64(time.Now().Unix())
	if iat > now {
		t.Error("iat should be in the past")
	}
	if exp < now {
		t.Error("exp should be in the future")
	}
}

func TestTokenCaching(t *testing.T) {
	_, pemData := generateTestKey(t)
	tp, err := NewGitHubAppTokenProvider(12345, pemData)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++

		// Verify Authorization header has Bearer JWT
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("expected Bearer auth, got: %s", auth)
		}

		// Verify the URL path
		if r.URL.Path != "/app/installations/67890/access_tokens" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		// Verify User-Agent is set
		if ua := r.Header.Get("User-Agent"); ua != "roborev" {
			t.Errorf("expected User-Agent 'roborev', got %q", ua)
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_test_token_123",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	tp.baseURL = srv.URL

	// First call should hit the server
	token1, err := tp.TokenForInstallation(67890)
	if err != nil {
		t.Fatalf("first TokenForInstallation(): %v", err)
	}
	if token1 != "ghs_test_token_123" {
		t.Errorf("expected ghs_test_token_123, got %s", token1)
	}
	if callCount != 1 {
		t.Errorf("expected 1 server call, got %d", callCount)
	}

	// Second call should use cache
	token2, err := tp.TokenForInstallation(67890)
	if err != nil {
		t.Fatalf("second TokenForInstallation(): %v", err)
	}
	if token2 != token1 {
		t.Error("expected cached token")
	}
	if callCount != 1 {
		t.Errorf("expected still 1 server call (cached), got %d", callCount)
	}
}

func TestTokenRefreshOnExpiry(t *testing.T) {
	_, pemData := generateTestKey(t)
	tp, err := NewGitHubAppTokenProvider(12345, pemData)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_refreshed",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	tp.baseURL = srv.URL

	// Manually set an expired cached token for installation 67890
	tp.mu.Lock()
	tp.tokens[int64(67890)] = &cachedToken{
		token:   "ghs_old",
		expires: time.Now().Add(2 * time.Minute), // Within 5 min buffer → should refresh
	}
	tp.mu.Unlock()

	token, err := tp.TokenForInstallation(67890)
	if err != nil {
		t.Fatalf("TokenForInstallation(): %v", err)
	}
	if token != "ghs_refreshed" {
		t.Errorf("expected refreshed token, got %s", token)
	}
	if callCount != 1 {
		t.Errorf("expected 1 refresh call, got %d", callCount)
	}
}

func TestTokenExchangeError(t *testing.T) {
	_, pemData := generateTestKey(t)
	tp, err := NewGitHubAppTokenProvider(12345, pemData)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	tp.baseURL = srv.URL

	_, err = tp.TokenForInstallation(67890)
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
}

func TestTokenCaching_MultipleInstallations(t *testing.T) {
	_, pemData := generateTestKey(t)
	tp, err := NewGitHubAppTokenProvider(12345, pemData)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Return a token that encodes which installation was requested
		var token string
		if strings.Contains(r.URL.Path, "/111/") {
			token = "ghs_token_for_111"
		} else if strings.Contains(r.URL.Path, "/222/") {
			token = "ghs_token_for_222"
		} else {
			token = "ghs_unknown"
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      token,
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	tp.baseURL = srv.URL

	// Get token for installation 111
	token1, err := tp.TokenForInstallation(111)
	if err != nil {
		t.Fatalf("TokenForInstallation(111): %v", err)
	}
	if token1 != "ghs_token_for_111" {
		t.Errorf("expected ghs_token_for_111, got %s", token1)
	}
	if callCount != 1 {
		t.Errorf("expected 1 server call, got %d", callCount)
	}

	// Get token for installation 222
	token2, err := tp.TokenForInstallation(222)
	if err != nil {
		t.Fatalf("TokenForInstallation(222): %v", err)
	}
	if token2 != "ghs_token_for_222" {
		t.Errorf("expected ghs_token_for_222, got %s", token2)
	}
	if callCount != 2 {
		t.Errorf("expected 2 server calls, got %d", callCount)
	}

	// Re-request installation 111 — should be cached
	token1b, err := tp.TokenForInstallation(111)
	if err != nil {
		t.Fatalf("TokenForInstallation(111) cached: %v", err)
	}
	if token1b != "ghs_token_for_111" {
		t.Errorf("expected cached ghs_token_for_111, got %s", token1b)
	}
	if callCount != 2 {
		t.Errorf("expected still 2 server calls (cached), got %d", callCount)
	}

	// Re-request installation 222 — should be cached
	token2b, err := tp.TokenForInstallation(222)
	if err != nil {
		t.Fatalf("TokenForInstallation(222) cached: %v", err)
	}
	if token2b != "ghs_token_for_222" {
		t.Errorf("expected cached ghs_token_for_222, got %s", token2b)
	}
	if callCount != 2 {
		t.Errorf("expected still 2 server calls (cached), got %d", callCount)
	}
}
