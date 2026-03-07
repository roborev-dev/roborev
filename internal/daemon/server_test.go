package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	"github.com/roborev-dev/roborev/internal/testenv"
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

func waitUntil(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// waitForSubscriberIncrease polls until subscriber count increases from initialCount
func waitForSubscriberIncrease(b Broadcaster, initialCount int, timeout time.Duration) bool {
	return waitUntil(timeout, func() bool { return b.SubscriberCount() > initialCount })
}

// waitForEvents polls until the response body contains at least minEvents newline-delimited events
func waitForEvents(w *safeRecorder, minEvents int, timeout time.Duration) bool {
	return waitUntil(timeout, func() bool { return strings.Count(w.bodyString(), "\n") >= minEvents })
}

// newTestServer creates a Server with a test DB and default config.
// Returns the server, DB (for seeding/assertions), and temp directory.
// setJobStatus is a test helper to update a job's status.
func setJobStatus(t *testing.T, db *storage.DB, jobID int64, status storage.JobStatus) {
	t.Helper()
	finishedAt := "NULL"
	errStr := "NULL"

	switch status {
	case storage.JobStatusDone, storage.JobStatusCanceled:
		finishedAt = "datetime('now')"
	case storage.JobStatusFailed:
		finishedAt = "datetime('now')"
		errStr = "'test error'"
	case storage.JobStatusRunning:
		// default bindings
	default:
		t.Fatalf("unsupported status in helper: %v", status)
	}

	query := fmt.Sprintf(`UPDATE review_jobs SET status = ?, started_at = datetime('now'), finished_at = %s, error = %s WHERE id = ?`, finishedAt, errStr)
	res, err := db.Exec(query, string(status), jobID)
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

func TestServerStartRejectsNonLoopbackBindAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
	}{
		{name: "all interfaces", addr: "0.0.0.0:0"},
		{name: "unspecified host", addr: ":7373"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := testutil.OpenTestDBWithDir(t)
			cfg := config.DefaultConfig()
			cfg.ServerAddr = tt.addr

			server := NewServer(db, cfg, "")
			err := server.Start(context.Background())
			if err == nil {
				t.Fatalf("expected start error for %q", tt.addr)
			}
			if !strings.Contains(err.Error(), "loopback host") {
				t.Fatalf("unexpected error for %q: %v", tt.addr, err)
			}
		})
	}
}

func TestWaitForServerReadySurfacesServeError(t *testing.T) {
	serveErrCh := make(chan error, 1)
	wantErr := errors.New("serve failed")
	serveErrCh <- wantErr

	ready, serveExited, err := waitForServerReady(context.Background(), "127.0.0.1:1", 50*time.Millisecond, serveErrCh)
	if ready {
		t.Fatal("expected ready=false when serve exits early")
	}
	if !serveExited {
		t.Fatal("expected serveExited=true when serveErrCh was consumed")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v, got %v", wantErr, err)
	}
}

func TestWaitForServerReadyLeavesServeExitUnreadWhenContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	serveErrCh := make(chan error, 1)
	serveErrCh <- http.ErrServerClosed

	ready, serveExited, err := waitForServerReady(ctx, "127.0.0.1:1", 50*time.Millisecond, serveErrCh)
	if ready {
		t.Fatal("expected ready=false when startup is canceled")
	}
	if serveExited {
		t.Fatal("expected serveExited=false when waitForServerReady exits before reading serveErrCh")
	}
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestAwaitServeExitOnUnreadyStartupReturnsImmediatelyWhenServeAlreadyExited(t *testing.T) {
	serveErrCh := make(chan error)
	done := make(chan error, 1)
	go func() {
		done <- awaitServeExitOnUnreadyStartup(true, serveErrCh)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("awaitServeExitOnUnreadyStartup blocked even though serve had already exited")
	}
}

func TestAwaitServeExitOnUnreadyStartupWaitsForServeExit(t *testing.T) {
	serveErrCh := make(chan error)
	done := make(chan error, 1)
	go func() {
		done <- awaitServeExitOnUnreadyStartup(false, serveErrCh)
	}()

	select {
	case err := <-done:
		t.Fatalf("expected helper to block before serve exit, got %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	serveErrCh <- http.ErrServerClosed

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("awaitServeExitOnUnreadyStartup did not return after serve exited")
	}
}

func TestServerStartSupportsIPv6LoopbackBindAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback not available: %v", err)
	}
	ln.Close()

	testenv.SetDataDir(t)

	db, _ := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	cfg.ServerAddr = "[::1]:0"
	server := NewServer(db, cfg, "")

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(context.Background())
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("server exited before becoming ready: %v", err)
		default:
		}

		info, err := ReadRuntime()
		if err == nil {
			host, _, splitErr := net.SplitHostPort(info.Addr)
			if splitErr != nil {
				t.Fatalf("runtime addr %q is invalid: %v", info.Addr, splitErr)
			}
			if host != "::1" {
				t.Fatalf("expected IPv6 loopback host, got %q", host)
			}
			if stopErr := server.Stop(); stopErr != nil {
				t.Fatalf("server.Stop() error: %v", stopErr)
			}
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("server.Start() returned error after stop: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for server to stop")
			}
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("timed out waiting for IPv6 daemon runtime")
}

func TestNewServerAllowUnsafeAgents(t *testing.T) {
	boolTrue, boolFalse := true, false
	tests := []struct {
		name     string
		preSet   bool
		cfgValue *bool
		expected bool
	}{
		{"config nil keeps agent state false", true, nil, false},
		{"config false sets agent state false", true, &boolFalse, false},
		{"config true sets agent state true", false, &boolTrue, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := agent.AllowUnsafeAgents()
			t.Cleanup(func() { agent.SetAllowUnsafeAgents(prev) })
			agent.SetAllowUnsafeAgents(tt.preSet)

			db, _ := testutil.OpenTestDBWithDir(t)
			cfg := config.DefaultConfig()
			cfg.AllowUnsafeAgents = tt.cfgValue

			_ = NewServer(db, cfg, "")

			if agent.AllowUnsafeAgents() != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, agent.AllowUnsafeAgents())
			}
		})
	}
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
