//go:build integration

package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestHandleEnqueueReviewTypeNormalization(t *testing.T) {
	tests := []struct {
		name         string
		reviewType   string
		wantCode     int
		wantStored   string // expected ReviewType on response job
		wantErrorMsg string // substring expected in error response
	}{
		{name: "empty defaults to default", reviewType: "", wantCode: http.StatusCreated, wantStored: "default"},
		{name: "general alias normalized", reviewType: "general", wantCode: http.StatusCreated, wantStored: "default"},
		{name: "review alias normalized", reviewType: "review", wantCode: http.StatusCreated, wantStored: "default"},
		{name: "default stored as-is", reviewType: "default", wantCode: http.StatusCreated, wantStored: "default"},
		{name: "security stored as-is", reviewType: "security", wantCode: http.StatusCreated, wantStored: "security"},
		{name: "design stored as-is", reviewType: "design", wantCode: http.StatusCreated, wantStored: "design"},
		{name: "invalid type rejected", reviewType: "bogus", wantCode: http.StatusBadRequest, wantErrorMsg: "invalid review_type"},
	}

	sharedTmpDir := t.TempDir()
	repoDir := filepath.Join(sharedTmpDir, "repo")
	testutil.InitTestGitRepo(t, repoDir)
	headSHA := testutil.GetHeadSHA(t, repoDir)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _, _ := newTestServer(t)

			reqData := EnqueueRequest{
				RepoPath:   repoDir,
				GitRef:     headSHA,
				Agent:      "test",
				ReviewType: tt.reviewType,
			}
			req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
			w := httptest.NewRecorder()

			server.handleEnqueue(w, req)

			if w.Code != tt.wantCode {
				t.Fatalf("status=%d, want %d; body=%s", w.Code, tt.wantCode, w.Body.String())
			}

			if tt.wantErrorMsg != "" {
				if !strings.Contains(w.Body.String(), tt.wantErrorMsg) {
					t.Fatalf("expected error containing %q, got %s", tt.wantErrorMsg, w.Body.String())
				}
				return
			}

			// Decode the job from the response to verify ReviewType
			var job storage.ReviewJob
			testutil.DecodeJSON(t, w, &job)
			if job.ReviewType != tt.wantStored {
				t.Errorf("ReviewType=%q, want %q", job.ReviewType, tt.wantStored)
			}
		})
	}
}
