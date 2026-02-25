package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// safeRecorder wraps httptest.ResponseRecorder with mutex protection for concurrent access
type safeRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func newSafeRecorder() *safeRecorder {
	return &safeRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (s *safeRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Write(p)
}

func (s *safeRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResponseRecorder.WriteHeader(code)
}

func (s *safeRecorder) Header() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Header()
}

func (s *safeRecorder) bodyString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Body.String()
}

// waitForSubscriberIncrease polls until subscriber count increases from initialCount
func waitForSubscriberIncrease(b Broadcaster, initialCount int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b.SubscriberCount() > initialCount {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// waitForEvents polls until the response body contains at least minEvents newline-delimited events
func waitForEvents(w *safeRecorder, minEvents int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body := w.bodyString()
		count := strings.Count(body, "\n")
		if count >= minEvents {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// newTestServer creates a Server with a test DB and default config.
// Returns the server, DB (for seeding/assertions), and temp directory.
// setJobStatus is a test helper to update a job's status.
func setJobStatus(t *testing.T, db *storage.DB, jobID int64, status storage.JobStatus) {
	t.Helper()
	var query string
	switch status {
	case storage.JobStatusRunning:
		query = `UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`
	case storage.JobStatusDone:
		query = `UPDATE review_jobs SET status = 'done', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`
	case storage.JobStatusFailed:
		query = `UPDATE review_jobs SET status = 'failed', started_at = datetime('now'), finished_at = datetime('now'), error = 'test error' WHERE id = ?`
	case storage.JobStatusCanceled:
		query = `UPDATE review_jobs SET status = 'canceled', started_at = datetime('now'), finished_at = datetime('now') WHERE id = ?`
	default:
		t.Fatalf("unsupported status in helper: %v", status)
	}
	res, err := db.Exec(query, jobID)
	if err != nil {
		t.Fatalf("failed to set job status: %v", err)
	}
	if rows, err := res.RowsAffected(); err != nil {
		t.Fatalf("failed to get rows affected: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected 1 row affected when setting job status, got %d", rows)
	}
}

// createTestJob is a helper to reduce boilerplate for creating a test repo, commit, and job.
func createTestJob(t *testing.T, db *storage.DB, dir, gitRef, agent string) *storage.ReviewJob {
	t.Helper()
	repo, err := db.GetOrCreateRepo(dir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, gitRef, "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: gitRef, Agent: agent})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	return job
}

// testInvalidIDParsing is a generic helper to test invalid ID query parameters.
func testInvalidIDParsing(t *testing.T, handler http.HandlerFunc, urlTemplate string) {
	t.Helper()
	tests := []string{"abc", "10abc", "1.5"}
	for _, invalidID := range tests {
		t.Run("invalid_id_"+invalidID, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf(urlTemplate, invalidID), nil)
			w := httptest.NewRecorder()
			handler(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected status %d for id %s, got %d", http.StatusBadRequest, invalidID, w.Code)
			}
		})
	}
}

func newTestServer(t *testing.T) (*Server, *storage.DB, string) {
	t.Helper()
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")
	return server, db, tmpDir
}

func TestNewServerAllowUnsafeAgents(t *testing.T) {
	boolTrue := true
	boolFalse := false

	t.Run("config nil keeps agent state false", func(t *testing.T) {
		// Save and restore global state
		prev := agent.AllowUnsafeAgents()
		t.Cleanup(func() { agent.SetAllowUnsafeAgents(prev) })

		// Pre-set to true to verify it gets reset
		agent.SetAllowUnsafeAgents(true)

		db, _ := testutil.OpenTestDBWithDir(t)
		cfg := config.DefaultConfig()
		cfg.AllowUnsafeAgents = nil // not set

		_ = NewServer(db, cfg, "")

		if agent.AllowUnsafeAgents() {
			t.Error("expected AllowUnsafeAgents to be false when config is nil")
		}
	})

	t.Run("config false sets agent state false", func(t *testing.T) {
		prev := agent.AllowUnsafeAgents()
		t.Cleanup(func() { agent.SetAllowUnsafeAgents(prev) })

		// Pre-set to true to verify it gets reset
		agent.SetAllowUnsafeAgents(true)

		db, _ := testutil.OpenTestDBWithDir(t)
		cfg := config.DefaultConfig()
		cfg.AllowUnsafeAgents = &boolFalse

		_ = NewServer(db, cfg, "")

		if agent.AllowUnsafeAgents() {
			t.Error("expected AllowUnsafeAgents to be false when config is false")
		}
	})

	t.Run("config true sets agent state true", func(t *testing.T) {
		prev := agent.AllowUnsafeAgents()
		t.Cleanup(func() { agent.SetAllowUnsafeAgents(prev) })

		// Pre-set to false to verify it gets set
		agent.SetAllowUnsafeAgents(false)

		db, _ := testutil.OpenTestDBWithDir(t)
		cfg := config.DefaultConfig()
		cfg.AllowUnsafeAgents = &boolTrue

		_ = NewServer(db, cfg, "")

		if !agent.AllowUnsafeAgents() {
			t.Error("expected AllowUnsafeAgents to be true when config is true")
		}
	})
}

// seedRepoWithJobs creates a repo and enqueues a number of jobs with predictable SHAs.
// shaPrefix is used to generate SHAs like "{prefix}sha{a,b,c...}".
func seedRepoWithJobs(t *testing.T, db *storage.DB, repoPath string, jobCount int, shaPrefix string) (*storage.Repo, []*storage.ReviewJob) {
	t.Helper()
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	var jobs []*storage.ReviewJob
	for i := range jobCount {
		sha := fmt.Sprintf("%ssha%c", shaPrefix, 'a'+i)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Author", "Subject", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}
		job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: sha, Agent: "test"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		jobs = append(jobs, job)
	}
	return repo, jobs
}

// setJobBranch directly updates a job's branch in the DB (for test setup).
func setJobBranch(t *testing.T, db *storage.DB, jobID int64, branch string) {
	t.Helper()
	if _, err := db.Exec("UPDATE review_jobs SET branch = ? WHERE id = ?", branch, jobID); err != nil {
		t.Fatalf("setJobBranch failed: %v", err)
	}
}

func TestGetMachineID_CachingBehavior(t *testing.T) {
	t.Run("caches valid machine ID", func(t *testing.T) {
		server, _, _ := newTestServer(t)

		// First call should fetch from DB and cache
		id1 := server.getMachineID()
		if id1 == "" {
			t.Fatal("Expected non-empty machine ID on first call")
		}

		// Second call should return cached value
		id2 := server.getMachineID()
		if id2 != id1 {
			t.Errorf("Expected cached value %q, got %q", id1, id2)
		}

		// Verify internal state is cached
		server.machineIDMu.Lock()
		cachedID := server.machineID
		server.machineIDMu.Unlock()
		if cachedID != id1 {
			t.Errorf("Expected internal machineID to be %q, got %q", id1, cachedID)
		}
	})

	t.Run("error then success caches on success", func(t *testing.T) {
		// This tests the error→success retry path:
		// 1. First call fails (DB closed) → returns empty, not cached
		// 2. DB replaced with working one
		// 3. Second call succeeds → returns valid ID and caches it

		server, db, tmpDir := newTestServer(t)

		// Close the DB to simulate error condition
		db.Close()

		// First call should return empty since DB is closed
		id1 := server.getMachineID()
		if id1 != "" {
			t.Fatalf("Expected empty machine ID on error, got %q", id1)
		}

		// Verify nothing was cached
		server.machineIDMu.Lock()
		if server.machineID != "" {
			server.machineIDMu.Unlock()
			t.Fatal("Should not cache on error")
		}
		server.machineIDMu.Unlock()

		// "Fix" the error by opening a new DB and replacing it
		newDB, err := storage.Open(filepath.Join(tmpDir, "reviews.db"))
		if err != nil {
			t.Fatalf("Failed to reopen DB: %v", err)
		}
		t.Cleanup(func() { newDB.Close() })
		server.db = newDB

		// Second call should succeed and cache
		id2 := server.getMachineID()
		if id2 == "" {
			t.Fatal("Expected non-empty machine ID after DB recovery")
		}

		// Verify it's now cached
		server.machineIDMu.Lock()
		cachedID := server.machineID
		server.machineIDMu.Unlock()
		if cachedID != id2 {
			t.Errorf("Expected cached ID %q, got %q", id2, cachedID)
		}

		// Third call should return cached value
		id3 := server.getMachineID()
		if id3 != id2 {
			t.Errorf("Expected cached ID %q on third call, got %q", id2, id3)
		}
	})
}

func TestServerStop_StopsCIPoller(t *testing.T) {
	server, db, _ := newTestServer(t)

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	cfg.CI.PollInterval = "1h"

	poller := NewCIPoller(db, NewStaticConfig(cfg), server.Broadcaster())
	if err := poller.Start(); err != nil {
		t.Fatalf("Start poller: %v", err)
	}

	healthy, _ := poller.HealthCheck()
	if !healthy {
		t.Fatal("expected poller running after Start")
	}

	server.SetCIPoller(poller)
	if err := server.Stop(); err != nil {
		t.Fatalf("Server.Stop: %v", err)
	}

	healthy, msg := poller.HealthCheck()
	if healthy {
		t.Fatalf("expected poller stopped after Server.Stop, got (%v, %q)", healthy, msg)
	}
}
