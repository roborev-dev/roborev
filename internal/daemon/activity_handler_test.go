package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type activityResponse struct {
	Entries []ActivityEntry `json:"entries"`
}

func requestActivity(server *Server, method, query string) *httptest.ResponseRecorder {
	url := "/api/activity"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(method, url, nil)
	w := httptest.NewRecorder()
	server.handleActivity(w, req)
	return w
}

func decodeActivityResponse(t *testing.T, w *httptest.ResponseRecorder) activityResponse {
	t.Helper()
	var resp activityResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode activity response: %v", err)
	}
	return resp
}

func TestHandleActivity_MethodNotAllowed(t *testing.T) {
	s := setupTestServer(t)
	w := requestActivity(s, http.MethodPost, "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleActivity_EmptyLog(t *testing.T) {
	s := setupTestServer(t)
	w := requestActivity(s, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeActivityResponse(t, w)
	if len(resp.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(resp.Entries))
	}
}

func TestHandleActivity_DefaultLimit(t *testing.T) {
	s := setupTestServer(t)

	// Log more than the default limit (50)
	for i := range 60 {
		s.activityLog.Log("test", "test", "msg", map[string]string{
			"i": string(rune('A' + i%26)),
		})
	}

	w := requestActivity(s, http.MethodGet, "")
	resp := decodeActivityResponse(t, w)
	if len(resp.Entries) != 50 {
		t.Errorf("expected default limit 50, got %d", len(resp.Entries))
	}
}

func TestHandleActivity_CustomLimit(t *testing.T) {
	s := setupTestServer(t)
	for range 20 {
		s.activityLog.Log("test", "test", "msg", nil)
	}

	w := requestActivity(s, http.MethodGet, "limit=5")
	resp := decodeActivityResponse(t, w)
	if len(resp.Entries) != 5 {
		t.Errorf("expected 5 entries, got %d", len(resp.Entries))
	}
}

func TestHandleActivity_LimitClamped(t *testing.T) {
	s := setupTestServer(t)
	for range 10 {
		s.activityLog.Log("test", "test", "msg", nil)
	}

	// Limit exceeding capacity is clamped
	w := requestActivity(s, http.MethodGet, "limit=9999")
	resp := decodeActivityResponse(t, w)
	if len(resp.Entries) != 10 {
		t.Errorf("expected 10 entries (all logged), got %d", len(resp.Entries))
	}
}

func TestHandleActivity_InvalidLimit(t *testing.T) {
	s := setupTestServer(t)
	for range 60 {
		s.activityLog.Log("test", "test", "msg", nil)
	}

	// Non-numeric limit falls back to default 50
	w := requestActivity(s, http.MethodGet, "limit=abc")
	resp := decodeActivityResponse(t, w)
	if len(resp.Entries) != 50 {
		t.Errorf("expected default 50 on invalid limit, got %d", len(resp.Entries))
	}

	// Negative limit falls back to default 50
	w = requestActivity(s, http.MethodGet, "limit=-5")
	resp = decodeActivityResponse(t, w)
	if len(resp.Entries) != 50 {
		t.Errorf("expected default 50 on negative limit, got %d", len(resp.Entries))
	}
}

func TestHandleActivity_NilActivityLog(t *testing.T) {
	s := setupTestServer(t)
	s.activityLog.Close()
	s.activityLog = nil

	w := requestActivity(s, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeActivityResponse(t, w)
	if len(resp.Entries) != 0 {
		t.Errorf("expected 0 entries with nil log, got %d", len(resp.Entries))
	}
}
