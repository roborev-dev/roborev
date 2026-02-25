package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// TestHandleJobOutput_InvalidJobID tests that invalid job_id returns 400.
func TestHandleJobOutput_InvalidJobID(t *testing.T) {
	server, _, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/job/output?job_id=notanumber", nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleJobOutput_NonExistentJob tests that non-existent job returns 404.
func TestHandleJobOutput_NonExistentJob(t *testing.T) {
	server, _, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/job/output?job_id=99999", nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleJobOutput_PollingRunningJob tests polling mode for a running job.
func TestHandleJobOutput_PollingRunningJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a running job
	job := createTestJob(t, db, filepath.Join(tmpDir, "test-repo"), "abc123", "test-agent")
	setJobStatus(t, db, job.ID, storage.JobStatusRunning)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/job/output?job_id=%d", job.ID), nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		JobID  int64  `json:"job_id"`
		Status string `json:"status"`
		Lines  []struct {
			TS       string `json:"ts"`
			Text     string `json:"text"`
			LineType string `json:"line_type"`
		} `json:"lines"`
		HasMore bool `json:"has_more"`
	}
	testutil.DecodeJSON(t, w, &resp)

	if resp.JobID != job.ID {
		t.Errorf("Expected job_id %d, got %d", job.ID, resp.JobID)
	}
	if resp.Status != "running" {
		t.Errorf("Expected status 'running', got %q", resp.Status)
	}
	if !resp.HasMore {
		t.Error("Expected has_more=true for running job")
	}
}

// TestHandleJobOutput_PollingCompletedJob tests polling mode for a completed job.
func TestHandleJobOutput_PollingCompletedJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a completed job
	job := createTestJob(t, db, filepath.Join(tmpDir, "test-repo"), "abc123", "test-agent")
	setJobStatus(t, db, job.ID, storage.JobStatusDone)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/job/output?job_id=%d", job.ID), nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		JobID   int64  `json:"job_id"`
		Status  string `json:"status"`
		HasMore bool   `json:"has_more"`
	}
	testutil.DecodeJSON(t, w, &resp)

	if resp.Status != "done" {
		t.Errorf("Expected status 'done', got %q", resp.Status)
	}
	if resp.HasMore {
		t.Error("Expected has_more=false for completed job")
	}
}

// TestHandleJobOutput_StreamingCompletedJob tests that streaming mode for a
// completed job returns an immediate complete response instead of hanging.
func TestHandleJobOutput_StreamingCompletedJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a completed job
	job := createTestJob(t, db, filepath.Join(tmpDir, "test-repo"), "abc123", "test-agent")
	setJobStatus(t, db, job.ID, storage.JobStatusDone)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/job/output?job_id=%d&stream=1", job.ID), nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	// Should return immediately with complete message, not hang
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	testutil.DecodeJSON(t, w, &resp)

	if resp.Type != "complete" {
		t.Errorf("Expected type 'complete', got %q", resp.Type)
	}
	if resp.Status != "done" {
		t.Errorf("Expected status 'done', got %q", resp.Status)
	}
}

func TestHandleJobOutput_MissingJobID(t *testing.T) {
	server, _, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/job/output", nil)
	w := httptest.NewRecorder()

	server.handleJobOutput(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleJobOutputIDParsing(t *testing.T) {
	server, _, _ := newTestServer(t)
	testInvalidIDParsing(t, server.handleJobOutput, "/api/job-output?job_id=%s")
}

func TestHandleJobLog(t *testing.T) {
	server, db, tmpDir := newTestServer(t)
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	// Create a repo and a job
	repo, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "testrepo"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID: repo.ID,
		GitRef: "abc123",
		Agent:  "test",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	t.Run("missing job_id returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/job/log", nil)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("nonexistent job returns 404", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet, "/api/job/log?job_id=99999", nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})

	t.Run("no log file returns 404", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/api/job/log?job_id=%d", job.ID),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("returns log content with headers", func(t *testing.T) {
		// Create a log file
		logDir := JobLogDir()
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		logContent := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n"
		if err := os.WriteFile(
			JobLogPath(job.ID), []byte(logContent), 0644,
		); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/api/job/log?job_id=%d", job.ID),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
			t.Errorf("expected Content-Type application/x-ndjson, got %q", ct)
		}
		if js := w.Header().Get("X-Job-Status"); js != "queued" {
			t.Errorf("expected X-Job-Status queued, got %q", js)
		}
		if w.Body.String() != logContent {
			t.Errorf("expected log content %q, got %q", logContent, w.Body.String())
		}
	})

	t.Run("running job with no log returns empty 200", func(t *testing.T) {
		// Claim the existing queued job to move it to "running"
		claimed, err := db.ClaimJob("worker-test")
		if err != nil {
			t.Fatalf("ClaimJob: %v", err)
		}
		// Remove any log file to simulate startup race
		os.Remove(JobLogPath(claimed.ID))

		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/api/job/log?job_id=%d", claimed.ID),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if js := w.Header().Get("X-Job-Status"); js != "running" {
			t.Errorf("expected X-Job-Status running, got %q", js)
		}
		if w.Body.Len() != 0 {
			t.Errorf("expected empty body, got %q", w.Body.String())
		}
	})

	t.Run("POST returns 405", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPost,
			fmt.Sprintf("/api/job/log?job_id=%d", job.ID),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", w.Code)
		}
	})
}

func TestHandleJobLogOffset(t *testing.T) {
	server, db, tmpDir := newTestServer(t)
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	repo, err := db.GetOrCreateRepo(
		filepath.Join(tmpDir, "testrepo"),
	)
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID: repo.ID,
		GitRef: "def456",
		Agent:  "test",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// Create log file with two JSONL lines.
	logDir := JobLogDir()
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	line1 := `{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}` + "\n"
	line2 := `{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}` + "\n"
	logContent := line1 + line2
	if err := os.WriteFile(
		JobLogPath(job.ID), []byte(logContent), 0644,
	); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Run("offset=0 returns full content", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=0", job.ID,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if w.Body.String() != logContent {
			t.Errorf(
				"expected full content, got %q",
				w.Body.String(),
			)
		}

		// X-Log-Offset should equal file size.
		offsetStr := w.Header().Get("X-Log-Offset")
		offset, err := strconv.ParseInt(offsetStr, 10, 64)
		if err != nil {
			t.Fatalf("parse X-Log-Offset %q: %v", offsetStr, err)
		}
		if offset != int64(len(logContent)) {
			t.Errorf(
				"X-Log-Offset = %d, want %d",
				offset, len(logContent),
			)
		}
	})

	t.Run("offset returns partial content", func(t *testing.T) {
		off := len(line1)
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=%d",
				job.ID, off,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if w.Body.String() != line2 {
			t.Errorf(
				"expected second line only, got %q",
				w.Body.String(),
			)
		}
	})

	t.Run("offset at end returns empty", func(t *testing.T) {
		off := len(logContent)
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=%d",
				job.ID, off,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if w.Body.Len() != 0 {
			t.Errorf(
				"expected empty body, got %q",
				w.Body.String(),
			)
		}
	})

	t.Run("negative offset returns 400", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=-1", job.ID,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("offset beyond file resets to 0", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d&offset=999999",
				job.ID,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		// Should return full content since offset was clamped.
		if w.Body.String() != logContent {
			t.Errorf(
				"expected full content after clamp, got %q",
				w.Body.String(),
			)
		}
	})

	t.Run("running job snaps to newline boundary", func(t *testing.T) {
		// Claim the existing queued job first so the next
		// ClaimJob picks up job2.
		if _, err := db.ClaimJob("worker-drain"); err != nil {
			t.Fatalf("ClaimJob (drain): %v", err)
		}

		// Create a new running job with a partial line at end.
		job2, err := db.EnqueueJob(storage.EnqueueOpts{
			RepoID: repo.ID,
			GitRef: "ghi789",
			Agent:  "test",
		})
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		if _, err := db.ClaimJob("worker-test2"); err != nil {
			t.Fatalf("ClaimJob: %v", err)
		}

		// Write a complete line + partial line.
		completeLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}` + "\n"
		partialLine := `{"type":"assistant","message":{"content":`
		if err := os.WriteFile(
			JobLogPath(job2.ID),
			[]byte(completeLine+partialLine),
			0644,
		); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf(
				"/api/job/log?job_id=%d", job2.ID,
			),
			nil,
		)
		w := httptest.NewRecorder()
		server.handleJobLog(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

		// Should only return up to the newline, not the partial.
		body := w.Body.String()
		if body != completeLine {
			t.Errorf(
				"expected only complete line, got %q",
				body,
			)
		}

		// X-Log-Offset should point past the newline.
		offsetStr := w.Header().Get("X-Log-Offset")
		offset, err := strconv.ParseInt(offsetStr, 10, 64)
		if err != nil {
			t.Fatalf("parse X-Log-Offset: %v", err)
		}
		if offset != int64(len(completeLine)) {
			t.Errorf(
				"X-Log-Offset = %d, want %d",
				offset, len(completeLine),
			)
		}
	})
}

func TestJobLogSafeEnd(t *testing.T) {
	t.Run("empty file", func(t *testing.T) {
		f := writeTempFile(t, []byte{})
		if got := jobLogSafeEnd(f, 0); got != 0 {
			t.Errorf("expected 0, got %d", got)
		}
	})

	t.Run("ends with newline", func(t *testing.T) {
		data := []byte("line1\nline2\n")
		f := writeTempFile(t, data)
		if got := jobLogSafeEnd(f, int64(len(data))); got != int64(len(data)) {
			t.Errorf("expected %d, got %d", len(data), got)
		}
	})

	t.Run("partial line at end", func(t *testing.T) {
		data := []byte("line1\npartial")
		f := writeTempFile(t, data)
		got := jobLogSafeEnd(f, int64(len(data)))
		if got != 6 { // "line1\n" is 6 bytes
			t.Errorf("expected 6, got %d", got)
		}
	})

	t.Run("no newlines at all", func(t *testing.T) {
		data := []byte("no-newlines-here")
		f := writeTempFile(t, data)
		if got := jobLogSafeEnd(f, int64(len(data))); got != 0 {
			t.Errorf("expected 0, got %d", got)
		}
	})

	t.Run("large partial beyond 64KB", func(t *testing.T) {
		// A complete line followed by a partial line > 64KB.
		// The chunked backward scan should still find the newline.
		completeLine := "line1\n"
		partial := strings.Repeat("x", 100*1024) // 100KB
		data := []byte(completeLine + partial)
		f := writeTempFile(t, data)
		got := jobLogSafeEnd(f, int64(len(data)))
		want := int64(len(completeLine))
		if got != want {
			t.Errorf("expected %d, got %d", want, got)
		}
	})
}

func writeTempFile(t *testing.T, data []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "logtest-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return f
}
