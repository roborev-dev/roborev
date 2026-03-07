package daemon

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testAppID          = 12345
	testInstallationID = 67890
)

// sharedTestKey generates one 2048-bit RSA key for the entire test
// binary. Key generation is ~0.5-1s per call, and most tests only
// need a valid key — not a unique one.
var (
	sharedKey     *rsa.PrivateKey
	sharedKeyPEM  string
	sharedKeyErr  error
	sharedKeyOnce sync.Once
)

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	sharedKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			sharedKeyErr = err
			return
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		})
		sharedKey = key
		sharedKeyPEM = string(pemBytes)
	})
	if sharedKeyErr != nil {
		t.Fatalf("generate test key: %v", sharedKeyErr)
	}
	return sharedKey, sharedKeyPEM
}

func generateTestKeyPKCS8(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	// PKCS8 parse test needs a PKCS8-encoded key specifically,
	// so generate a fresh one (only called once).
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

func setupMockProvider(t *testing.T, handler http.HandlerFunc) (*GitHubAppTokenProvider, *httptest.Server) {
	t.Helper()
	_, pemData := testKey(t)
	tp, err := NewGitHubAppTokenProvider(testAppID, pemData)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	tp.baseURL = srv.URL
	return tp, srv
}

func setupMockProviderWithResponses(t *testing.T, responses []mockResponse) (*GitHubAppTokenProvider, *mockServer) {
	t.Helper()
	mock := &mockServer{t: t, responses: responses}
	tp, _ := setupMockProvider(t, mock.ServeHTTP)
	return tp, mock
}

func parseInsecureJWT(t *testing.T, token string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}

	headerBytes, err := base64URLDecode(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatalf("parse header: %v", err)
	}

	payloadBytes, err := base64URLDecode(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	return header, payload
}

func TestParsePrivateKey(t *testing.T) {
	_, pkcs1PEM := testKey(t)
	_, pkcs8PEM := generateTestKeyPKCS8(t)

	tests := []struct {
		name    string
		pemData []byte
		wantErr string
	}{
		{"PKCS1", []byte(pkcs1PEM), ""},
		{"PKCS8", []byte(pkcs8PEM), ""},
		{"Invalid", []byte("not a PEM"), "no PEM block"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := parsePrivateKey(tt.pemData)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key == nil {
				t.Fatal("expected non-nil key")
			}
		})
	}
}

func TestSignJWT_Structure(t *testing.T) {
	_, pemData := testKey(t)
	tp, err := NewGitHubAppTokenProvider(testAppID, pemData)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	fixedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tp.now = func() time.Time { return fixedTime }

	jwt, err := tp.signJWT()
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}

	header, payload := parseInsecureJWT(t, jwt)

	if header["alg"] != "RS256" {
		t.Errorf("expected alg RS256, got %v", header["alg"])
	}
	if header["typ"] != "JWT" {
		t.Errorf("expected typ JWT, got %v", header["typ"])
	}

	iss, ok := payload["iss"].(float64)
	if !ok || int64(iss) != testAppID {
		t.Errorf("expected iss=%d, got %v", testAppID, payload["iss"])
	}

	iat, ok := payload["iat"].(float64)
	if !ok {
		t.Fatal("missing iat")
	}
	exp, ok := payload["exp"].(float64)
	if !ok {
		t.Fatal("missing exp")
	}

	// assert relative invariants to avoid flaky tests under slow execution
	if exp-iat != 660 {
		t.Errorf("expected duration (exp-iat) to be 660s, got %v", exp-iat)
	}

	expectedIat := float64(fixedTime.Add(-60 * time.Second).Unix())
	expectedExp := float64(fixedTime.Add(10 * time.Minute).Unix())

	if iat != expectedIat {
		t.Errorf("expected iat %v, got %v", expectedIat, iat)
	}
	if exp != expectedExp {
		t.Errorf("expected exp %v, got %v", expectedExp, exp)
	}
}

func TestGitHubAppTokenProvider_NilNow(t *testing.T) {
	_, pemData := testKey(t)

	// Construct the provider directly to simulate missing 'now' as if by a struct literal
	key, err := parsePrivateKey([]byte(pemData))
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}

	tp := &GitHubAppTokenProvider{
		appID:  testAppID,
		key:    key,
		tokens: make(map[int64]*cachedToken),
	}

	// This should not panic
	_, err = tp.signJWT()
	if err != nil {
		t.Fatalf("sign JWT with nil now: %v", err)
	}

	// Test cached token path which also calls getNow()
	tp.tokens[123] = &cachedToken{
		token:   "test-token",
		expires: time.Now().Add(1 * time.Hour),
	}

	token, err := tp.TokenForInstallation(123)
	if err != nil {
		t.Fatalf("TokenForInstallation with nil now: %v", err)
	}
	if token != "test-token" {
		t.Errorf("expected test-token, got %s", token)
	}
}

type mockResponse struct {
	path string
	body map[string]any
}

type mockServer struct {
	callCount int
	responses []mockResponse
	t         *testing.T
}

func (m *mockServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.callCount++
	if m.callCount > len(m.responses) {
		m.t.Errorf("unexpected call %d", m.callCount)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	resp := m.responses[m.callCount-1]

	// Verify Authorization header has Bearer JWT
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		m.t.Errorf("expected Bearer auth, got: %s", auth)
	}

	if resp.path != "" && r.URL.Path != resp.path {
		m.t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, resp.path)
	}

	// Verify User-Agent is set
	if ua := r.Header.Get("User-Agent"); ua != "roborev" {
		m.t.Errorf("expected User-Agent 'roborev', got %q", ua)
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp.body)
}

func TestTokenCaching(t *testing.T) {
	tp, mock := setupMockProviderWithResponses(t, []mockResponse{
		{
			path: fmt.Sprintf("/app/installations/%d/access_tokens", testInstallationID),
			body: map[string]any{
				"token":      "ghs_test_token_123",
				"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			},
		},
	})

	// First call should hit the server
	token1, err := tp.TokenForInstallation(testInstallationID)
	if err != nil {
		t.Fatalf("first TokenForInstallation(): %v", err)
	}
	if token1 != "ghs_test_token_123" {
		t.Errorf("expected ghs_test_token_123, got %s", token1)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 server call, got %d", mock.callCount)
	}

	// Second call should use cache
	token2, err := tp.TokenForInstallation(testInstallationID)
	if err != nil {
		t.Fatalf("second TokenForInstallation(): %v", err)
	}
	if token2 != token1 {
		t.Error("expected cached token")
	}
	if mock.callCount != 1 {
		t.Errorf("expected still 1 server call (cached), got %d", mock.callCount)
	}
}
func TestTokenRefreshOnExpiry(t *testing.T) {
	// Set fixed time to easily control boundaries
	fixedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tp, mock := setupMockProviderWithResponses(t, []mockResponse{
		{
			path: fmt.Sprintf("/app/installations/%d/access_tokens", testInstallationID),
			body: map[string]any{
				"token":      "ghs_old",
				"expires_at": fixedTime.Add(1 * time.Second).Format(time.RFC3339),
			},
		},
		{
			path: fmt.Sprintf("/app/installations/%d/access_tokens", testInstallationID),
			body: map[string]any{
				"token":      "ghs_refreshed",
				"expires_at": fixedTime.Add(1 * time.Hour).Format(time.RFC3339),
			},
		},
	})
	tp.now = func() time.Time { return fixedTime }

	// First call caches expiring token
	token1, err := tp.TokenForInstallation(testInstallationID)
	if err != nil {
		t.Fatalf("first TokenForInstallation(): %v", err)
	}
	if token1 != "ghs_old" {
		t.Errorf("expected expiring token, got %s", token1)
	}

	// Second call should refresh since token is within 5 min buffer
	token2, err := tp.TokenForInstallation(testInstallationID)
	if err != nil {
		t.Fatalf("second TokenForInstallation(): %v", err)
	}
	if token2 != "ghs_refreshed" {
		t.Errorf("expected refreshed token, got %s", token2)
	}
	if mock.callCount != 2 {
		t.Errorf("expected 2 server calls (refresh), got %d", mock.callCount)
	}
}
func TestTokenExchangeError(t *testing.T) {
	tp, _ := setupMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	})

	_, err := tp.TokenForInstallation(testInstallationID)
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
}

func TestTokenCaching_MultipleInstallations(t *testing.T) {
	tp, mock := setupMockProviderWithResponses(t, []mockResponse{
		{
			path: "/app/installations/111/access_tokens",
			body: map[string]any{
				"token":      "ghs_token_for_111",
				"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			},
		},
		{
			path: "/app/installations/222/access_tokens",
			body: map[string]any{
				"token":      "ghs_token_for_222",
				"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			},
		},
	})

	// Get token for installation 111
	token1, err := tp.TokenForInstallation(111)
	if err != nil {
		t.Fatalf("TokenForInstallation(111): %v", err)
	}
	if token1 != "ghs_token_for_111" {
		t.Errorf("expected ghs_token_for_111, got %s", token1)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 server call, got %d", mock.callCount)
	}

	// Get token for installation 222
	token2, err := tp.TokenForInstallation(222)
	if err != nil {
		t.Fatalf("TokenForInstallation(222): %v", err)
	}
	if token2 != "ghs_token_for_222" {
		t.Errorf("expected ghs_token_for_222, got %s", token2)
	}
	if mock.callCount != 2 {
		t.Errorf("expected 2 server calls, got %d", mock.callCount)
	}

	// Re-request installation 111 — should be cached
	token1b, err := tp.TokenForInstallation(111)
	if err != nil {
		t.Fatalf("TokenForInstallation(111) cached: %v", err)
	}
	if token1b != "ghs_token_for_111" {
		t.Errorf("expected cached ghs_token_for_111, got %s", token1b)
	}
	if mock.callCount != 2 {
		t.Errorf("expected still 2 server calls (cached), got %d", mock.callCount)
	}

	// Re-request installation 222 — should be cached
	token2b, err := tp.TokenForInstallation(222)
	if err != nil {
		t.Fatalf("TokenForInstallation(222) cached: %v", err)
	}
	if token2b != "ghs_token_for_222" {
		t.Errorf("expected cached ghs_token_for_222, got %s", token2b)
	}
	if mock.callCount != 2 {
		t.Errorf("expected still 2 server calls (cached), got %d", mock.callCount)
	}
}
