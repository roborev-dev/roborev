package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/roborev-dev/roborev/internal/version"
)

func TestCreateMockRefineHandler_MethodEnforcement(t *testing.T) {
	state := newMockRefineState()
	handler := createMockRefineHandler(state)

	tests := []struct {
		name       string
		method     string
		path       string
		wantMethod string // expected correct method
		wantStatus int
	}{
		// Correct methods
		{
			name:       "GET /api/status returns 200",
			method:     http.MethodGet,
			path:       "/api/status",
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /api/jobs returns 200",
			method:     http.MethodGet,
			path:       "/api/jobs",
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /api/review returns 404 (no data)",
			method:     http.MethodGet,
			path:       "/api/review",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "GET /api/comments returns 200",
			method:     http.MethodGet,
			path:       "/api/comments",
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST /api/enqueue returns 201",
			method:     http.MethodPost,
			path:       "/api/enqueue",
			wantStatus: http.StatusCreated,
		},
		{
			name:       "POST /api/comment returns 201",
			method:     http.MethodPost,
			path:       "/api/comment",
			wantStatus: http.StatusCreated,
		},
		// Wrong methods should return 405
		{
			name:       "POST /api/status returns 405",
			method:     http.MethodPost,
			path:       "/api/status",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "POST /api/review returns 405",
			method:     http.MethodPost,
			path:       "/api/review",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "POST /api/comments returns 405",
			method:     http.MethodPost,
			path:       "/api/comments",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "POST /api/jobs returns 405",
			method:     http.MethodPost,
			path:       "/api/jobs",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "GET /api/enqueue returns 405",
			method:     http.MethodGet,
			path:       "/api/enqueue",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "GET /api/comment returns 405",
			method:     http.MethodGet,
			path:       "/api/comment",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "PUT /api/status returns 405",
			method:     http.MethodPut,
			path:       "/api/status",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "DELETE /api/review returns 405",
			method:     http.MethodDelete,
			path:       "/api/review",
			wantStatus: http.StatusMethodNotAllowed,
		},
		// Unknown path returns 404
		{
			name:       "GET /api/unknown returns 404",
			method:     http.MethodGet,
			path:       "/api/unknown",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf(
					"got status %d, want %d",
					w.Code, tt.wantStatus,
				)
			}
		})
	}
}

func TestCreateMockRefineHandler_405ResponseBody(t *testing.T) {
	state := newMockRefineState()
	handler := createMockRefineHandler(state)

	req := httptest.NewRequest(http.MethodPost, "/api/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d, want 405", w.Code)
	}

	body, err := io.ReadAll(w.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	var resp map[string]string
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshaling body %q: %v", body, err)
	}
	if resp["error"] != "method not allowed" {
		t.Errorf(
			"got error %q, want %q",
			resp["error"], "method not allowed",
		)
	}
}

func TestNewMockDaemon_MethodEnforcement(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		// Correct methods
		{
			name:       "GET /api/status returns 200",
			method:     http.MethodGet,
			path:       "/api/status",
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /api/jobs returns 200",
			method:     http.MethodGet,
			path:       "/api/jobs",
			wantStatus: http.StatusOK,
		},
		// Wrong methods return 405
		{
			name:       "POST /api/status returns 405",
			method:     http.MethodPost,
			path:       "/api/status",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "POST /api/review returns 405",
			method:     http.MethodPost,
			path:       "/api/review",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "POST /api/comments returns 405",
			method:     http.MethodPost,
			path:       "/api/comments",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "POST /api/jobs returns 405",
			method:     http.MethodPost,
			path:       "/api/jobs",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "GET /api/enqueue returns 405",
			method:     http.MethodGet,
			path:       "/api/enqueue",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	md := NewMockDaemon(t, MockRefineHooks{})
	defer md.Close()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.DefaultClient.Do(
				mustNewRequest(t, tt.method, md.Server.URL+tt.path),
			)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf(
					"got status %d, want %d",
					resp.StatusCode, tt.wantStatus,
				)
			}
		})
	}
}

func TestNewMockDaemon_HooksInvokedOnCorrectMethod(t *testing.T) {
	var hookCalled bool

	tests := []struct {
		name  string
		hooks MockRefineHooks
		path  string
	}{
		{
			name: "OnStatus hook invoked on GET",
			hooks: MockRefineHooks{
				OnStatus: func(
					w http.ResponseWriter, r *http.Request,
					_ *mockRefineState,
				) bool {
					hookCalled = true
					_ = json.NewEncoder(w).Encode(map[string]any{
						"version": version.Version,
					})
					return true
				},
			},
			path: "/api/status",
		},
		{
			name: "OnReview hook invoked on GET",
			hooks: MockRefineHooks{
				OnReview: func(
					w http.ResponseWriter, r *http.Request,
					_ *mockRefineState,
				) bool {
					hookCalled = true
					w.WriteHeader(http.StatusOK)
					return true
				},
			},
			path: "/api/review",
		},
		{
			name: "OnComments hook invoked on GET",
			hooks: MockRefineHooks{
				OnComments: func(
					w http.ResponseWriter, r *http.Request,
					_ *mockRefineState,
				) bool {
					hookCalled = true
					w.WriteHeader(http.StatusOK)
					return true
				},
			},
			path: "/api/comments",
		},
		{
			name: "OnGetJobs hook invoked on GET",
			hooks: MockRefineHooks{
				OnGetJobs: func(
					w http.ResponseWriter, r *http.Request,
					_ *mockRefineState,
				) bool {
					hookCalled = true
					w.WriteHeader(http.StatusOK)
					return true
				},
			},
			path: "/api/jobs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hookCalled = false
			md := NewMockDaemon(t, tt.hooks)
			defer md.Close()

			resp, err := http.DefaultClient.Do(
				mustNewRequest(t, http.MethodGet, md.Server.URL+tt.path),
			)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()

			if !hookCalled {
				t.Error("hook was not invoked on correct method")
			}
		})
	}
}

func TestNewMockDaemon_HooksNotInvokedOnWrongMethod(t *testing.T) {
	var hookCalled bool

	tests := []struct {
		name  string
		hooks MockRefineHooks
		path  string
	}{
		{
			name: "OnStatus hook not invoked on POST",
			hooks: MockRefineHooks{
				OnStatus: func(
					w http.ResponseWriter, r *http.Request,
					_ *mockRefineState,
				) bool {
					hookCalled = true
					return true
				},
			},
			path: "/api/status",
		},
		{
			name: "OnReview hook not invoked on POST",
			hooks: MockRefineHooks{
				OnReview: func(
					w http.ResponseWriter, r *http.Request,
					_ *mockRefineState,
				) bool {
					hookCalled = true
					return true
				},
			},
			path: "/api/review",
		},
		{
			name: "OnComments hook not invoked on POST",
			hooks: MockRefineHooks{
				OnComments: func(
					w http.ResponseWriter, r *http.Request,
					_ *mockRefineState,
				) bool {
					hookCalled = true
					return true
				},
			},
			path: "/api/comments",
		},
		{
			name: "OnGetJobs hook not invoked on POST",
			hooks: MockRefineHooks{
				OnGetJobs: func(
					w http.ResponseWriter, r *http.Request,
					_ *mockRefineState,
				) bool {
					hookCalled = true
					return true
				},
			},
			path: "/api/jobs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hookCalled = false
			md := NewMockDaemon(t, tt.hooks)
			defer md.Close()

			resp, err := http.DefaultClient.Do(
				mustNewRequest(t, http.MethodPost, md.Server.URL+tt.path),
			)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()

			if hookCalled {
				t.Error("hook was invoked on wrong method")
			}
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf(
					"got status %d, want 405",
					resp.StatusCode,
				)
			}
		})
	}
}

func mustNewRequest(
	t *testing.T, method, url string,
) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	return req
}
