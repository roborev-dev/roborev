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

func requestActivity(t *testing.T, server *Server, method, query string) *httptest.ResponseRecorder {
	t.Helper()
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
	res := w.Result()
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode activity response: %v", err)
	}
	return resp
}
func TestHandleActivity_MethodNotAllowed(t *testing.T) {
	s := setupTestServer(t)
	w := requestActivity(t, s, http.MethodPost, "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleActivity_Limits(t *testing.T) {
	tests := []struct {
		name          string
		logCount      int
		query         string
		expectedCount int
	}{
		{name: "EmptyLog", logCount: 0, query: "", expectedCount: 0},
		{name: "DefaultLimit", logCount: 60, query: "", expectedCount: 50},
		{name: "CustomLimit", logCount: 20, query: "limit=5", expectedCount: 5},
		{name: "LimitClamped", logCount: 10, query: "limit=9999", expectedCount: 10},
		{name: "InvalidLimit_Alpha", logCount: 60, query: "limit=abc", expectedCount: 50},
		{name: "InvalidLimit_Negative", logCount: 60, query: "limit=-5", expectedCount: 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := setupTestServer(t)
			for range tt.logCount {
				s.activityLog.Log("test", "test", "msg", nil)
			}

			w := requestActivity(t, s, http.MethodGet, tt.query)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}

			resp := decodeActivityResponse(t, w)
			if len(resp.Entries) != tt.expectedCount {
				t.Errorf("expected %d entries, got %d", tt.expectedCount, len(resp.Entries))
			}
		})
	}
}

func TestHandleActivity_NilActivityLog(t *testing.T) {
	s := setupTestServer(t)
	s.activityLog.Close()
	s.activityLog = nil

	w := requestActivity(t, s, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeActivityResponse(t, w)
	if len(resp.Entries) != 0 {
		t.Errorf("expected 0 entries with nil log, got %d", len(resp.Entries))
	}
}
