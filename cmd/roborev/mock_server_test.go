package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMockServerHandler_HandleEnqueue_MethodRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method     string
		wantStatus int
	}{
		{http.MethodPost, http.StatusCreated},
		{http.MethodGet, http.StatusMethodNotAllowed},
		{http.MethodPut, http.StatusMethodNotAllowed},
		{http.MethodDelete, http.StatusMethodNotAllowed},
		{http.MethodPatch, http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()

			h := &mockServerHandler{
				state: &MockServerState{},
			}

			req := httptest.NewRequest(tt.method, "/api/enqueue", nil)
			w := httptest.NewRecorder()

			h.handleEnqueue(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}
