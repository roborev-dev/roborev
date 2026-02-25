package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
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

func TestNewMockDaemon_ServerWiring(t *testing.T) {
	// A single integration check to ensure the server actually boots
	// and routes to the handler correctly.
	md := NewMockDaemon(t, MockRefineHooks{})
	defer md.Close()

	resp, err := http.DefaultClient.Do(
		mustNewRequest(t, http.MethodGet, md.Server.URL+"/api/status"),
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
}

func TestNewMockDaemon_HookRouting(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantCalled bool
		wantStatus int
		makeHooks  func(called *bool) MockRefineHooks
	}{
		{
			name:       "OnStatus hook invoked on GET",
			method:     http.MethodGet,
			path:       "/api/status",
			wantCalled: true,
			wantStatus: http.StatusOK,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnStatus: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						_ = json.NewEncoder(w).Encode(map[string]any{
							"version": version.Version,
						})
						return true
					},
				}
			},
		},
		{
			name:       "OnReview hook invoked on GET",
			method:     http.MethodGet,
			path:       "/api/review",
			wantCalled: true,
			wantStatus: http.StatusOK,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnReview: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						w.WriteHeader(http.StatusOK)
						return true
					},
				}
			},
		},
		{
			name:       "OnComments hook invoked on GET",
			method:     http.MethodGet,
			path:       "/api/comments",
			wantCalled: true,
			wantStatus: http.StatusOK,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnComments: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						w.WriteHeader(http.StatusOK)
						return true
					},
				}
			},
		},
		{
			name:       "OnGetJobs hook invoked on GET",
			method:     http.MethodGet,
			path:       "/api/jobs",
			wantCalled: true,
			wantStatus: http.StatusOK,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnGetJobs: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						w.WriteHeader(http.StatusOK)
						return true
					},
				}
			},
		},
		{
			name:       "OnEnqueue hook invoked on POST",
			method:     http.MethodPost,
			path:       "/api/enqueue",
			wantCalled: true,
			wantStatus: http.StatusCreated,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnEnqueue: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						w.WriteHeader(http.StatusCreated)
						return true
					},
				}
			},
		},
		{
			name:       "OnStatus hook not invoked on POST",
			method:     http.MethodPost,
			path:       "/api/status",
			wantCalled: false,
			wantStatus: http.StatusMethodNotAllowed,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnStatus: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						return true
					},
				}
			},
		},
		{
			name:       "OnReview hook not invoked on POST",
			method:     http.MethodPost,
			path:       "/api/review",
			wantCalled: false,
			wantStatus: http.StatusMethodNotAllowed,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnReview: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						return true
					},
				}
			},
		},
		{
			name:       "OnComments hook not invoked on POST",
			method:     http.MethodPost,
			path:       "/api/comments",
			wantCalled: false,
			wantStatus: http.StatusMethodNotAllowed,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnComments: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						return true
					},
				}
			},
		},
		{
			name:       "OnGetJobs hook not invoked on POST",
			method:     http.MethodPost,
			path:       "/api/jobs",
			wantCalled: false,
			wantStatus: http.StatusMethodNotAllowed,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnGetJobs: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						return true
					},
				}
			},
		},
		{
			name:       "OnEnqueue hook not invoked on GET",
			method:     http.MethodGet,
			path:       "/api/enqueue",
			wantCalled: false,
			wantStatus: http.StatusMethodNotAllowed,
			makeHooks: func(called *bool) MockRefineHooks {
				return MockRefineHooks{
					OnEnqueue: func(
						w http.ResponseWriter, r *http.Request,
						_ *mockRefineState,
					) bool {
						*called = true
						return true
					},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hookCalled bool
			md := NewMockDaemon(t, tt.makeHooks(&hookCalled))
			defer md.Close()

			resp, err := http.DefaultClient.Do(
				mustNewRequest(t, tt.method, md.Server.URL+tt.path),
			)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if hookCalled != tt.wantCalled {
				t.Errorf("hookCalled = %v, want %v", hookCalled, tt.wantCalled)
			}
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("got status %d, want %d", resp.StatusCode, tt.wantStatus)
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

// MockDaemonBuilder helps construct a mock daemon with specific behavior
type MockDaemonBuilder struct {
	t        *testing.T
	handlers map[string]http.HandlerFunc
	reviews  map[int64]storage.Review
}

func newMockDaemonBuilder(t *testing.T) *MockDaemonBuilder {
	return &MockDaemonBuilder{
		t:        t,
		handlers: make(map[string]http.HandlerFunc),
		reviews:  make(map[int64]storage.Review),
	}
}

func (b *MockDaemonBuilder) WithHandler(path string, handler http.HandlerFunc) *MockDaemonBuilder {
	b.handlers[path] = handler
	return b
}

func (b *MockDaemonBuilder) WithReview(jobID int64, output string) *MockDaemonBuilder {
	b.reviews[jobID] = storage.Review{
		JobID:  jobID,
		Output: output,
	}
	return b
}

func (b *MockDaemonBuilder) WithJobs(jobs []storage.ReviewJob) *MockDaemonBuilder {
	return b.WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"jobs":     jobs,
			"has_more": false,
		})
	})
}

func (b *MockDaemonBuilder) Build() *httptest.Server {
	// Register default review handler if not already overridden
	if _, ok := b.handlers["/api/review"]; !ok && len(b.reviews) > 0 {
		b.handlers["/api/review"] = func(w http.ResponseWriter, r *http.Request) {
			jobIDStr := r.URL.Query().Get("job_id")
			jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
			if err != nil {
				http.Error(w, "invalid job_id", http.StatusBadRequest)
				return
			}

			if review, ok := b.reviews[jobID]; ok {
				json.NewEncoder(w).Encode(review)
			} else {
				// Fallback: if there is only one review and no job_id was requested
				// (or even if it was), some tests might rely on "any review".
				// But strictly, we should require job_id match.
				// However, existing tests might be loose.
				// For now, return 404 if not found to be strict.
				http.Error(w, fmt.Sprintf("review for job %d not found", jobID), http.StatusNotFound)
			}
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := b.handlers[r.URL.Path]; ok {
			h(w, r)
			return
		}
		// Default handlers
		switch r.URL.Path {
		case "/api/comment":
			w.WriteHeader(http.StatusCreated)
		case "/api/review/address":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return daemonFromHandler(b.t, handler).Server
}
