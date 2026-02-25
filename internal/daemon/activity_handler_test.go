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

func TestHandleActivity_Limits(t *testing.T) {
	tests := []struct {
		name          string
		logCount      int
		query         string
		expectedCount int
	}{
		{"EmptyLog", 0, "", 0},
		{"DefaultLimit", 60, "", 50},
		{"CustomLimit", 20, "limit=5", 5},
		{"LimitClamped", 10, "limit=9999", 10},
		{"InvalidLimit_Alpha", 60, "limit=abc", 50},
		{"InvalidLimit_Negative", 60, "limit=-5", 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := setupTestServer(t)
			for range tt.logCount {
				s.activityLog.Log("test", "test", "msg", nil)
			}

			w := requestActivity(s, http.MethodGet, tt.query)
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

	w := requestActivity(s, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeActivityResponse(t, w)
	if len(resp.Entries) != 0 {
		t.Errorf("expected 0 entries with nil log, got %d", len(resp.Entries))
	}
}
