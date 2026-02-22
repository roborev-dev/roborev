//go:build integration

package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// newTestEvent creates an Event with sensible defaults. Override fields via opts.
func newTestEvent(jobID int64, opts ...func(*Event)) Event {
	e := Event{
		Type:    "review.completed",
		TS:      time.Now(),
		JobID:   jobID,
		Repo:    "/test/repo",
		SHA:     "abc123",
		Agent:   "test",
		Verdict: "P",
	}
	for _, o := range opts {
		o(&e)
	}
	return e
}

func assertEventFields(t *testing.T, got Event, expected Event) {
	t.Helper()
	if got.JobID != expected.JobID {
		t.Errorf("JobID: got %d, want %d", got.JobID, expected.JobID)
	}
	if got.Repo != expected.Repo {
		t.Errorf("Repo: got %q, want %q", got.Repo, expected.Repo)
	}
	if got.SHA != expected.SHA {
		t.Errorf("SHA: got %q, want %q", got.SHA, expected.SHA)
	}
	if got.Agent != expected.Agent {
		t.Errorf("Agent: got %q, want %q", got.Agent, expected.Agent)
	}
	if got.Verdict != expected.Verdict {
		t.Errorf("Verdict: got %q, want %q", got.Verdict, expected.Verdict)
	}
	if got.Type != expected.Type {
		t.Errorf("Type: got %q, want %q", got.Type, expected.Type)
	}
}

func waitForEventType(t *testing.T, ch <-chan Event, eventType string, timeout time.Duration) Event {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed waiting for event type: %s", eventType)
				return Event{}
			}
			if e.Type == eventType {
				return e
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for event type: %s", eventType)
			return Event{}
		}
	}
}

func assertNoEventWithin(t *testing.T, ch <-chan Event, duration time.Duration) {
	t.Helper()
	select {
	case e := <-ch:
		t.Errorf("Received unexpected event: %+v", e)
	case <-time.After(duration):
		// OK
	}
}

func TestStreamEventsEndToEnd(t *testing.T) {
	broadcaster := NewBroadcaster()

	_, eventCh := broadcaster.Subscribe("")

	testEvent := newTestEvent(42, func(e *Event) {
		e.Repo = "/path/to/repo"
		e.Agent = "test-agent"
	})
	broadcaster.Broadcast(testEvent)

	received := testutil.ReceiveWithTimeout(t, eventCh, 1*time.Second)
	assertEventFields(t, received, testEvent)
}

func TestStreamEventsWithRepoFilter(t *testing.T) {
	broadcaster := NewBroadcaster()

	_, eventCh := broadcaster.Subscribe("/path/to/repo1")

	broadcaster.Broadcast(newTestEvent(1, func(e *Event) { e.Repo = "/path/to/repo1"; e.SHA = "sha1" }))
	broadcaster.Broadcast(newTestEvent(2, func(e *Event) { e.Repo = "/path/to/repo2"; e.SHA = "sha2"; e.Verdict = "F" }))
	broadcaster.Broadcast(newTestEvent(3, func(e *Event) { e.Repo = "/path/to/repo1"; e.SHA = "sha3" }))

	// Should receive only events for repo1 (JobID 1 and 3)
	e1 := testutil.ReceiveWithTimeout(t, eventCh, 500*time.Millisecond)
	e2 := testutil.ReceiveWithTimeout(t, eventCh, 500*time.Millisecond)

	// Should not receive more events (repo2 event was filtered out)
	assertNoEventWithin(t, eventCh, 100*time.Millisecond)

	if e1.JobID != 1 {
		t.Errorf("Expected first event JobID 1, got %d", e1.JobID)
	}
	if e2.JobID != 3 {
		t.Errorf("Expected second event JobID 3, got %d", e2.JobID)
	}
}

func TestStreamEventsMethodNotAllowed(t *testing.T) {
	db := testutil.OpenTestDB(t)

	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	req := httptest.NewRequest("POST", "/api/stream/events", nil)
	rec := httptest.NewRecorder()

	server.handleStreamEvents(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", rec.Code)
	}
}

func TestBroadcasterIntegrationWithWorker(t *testing.T) {
	db, tmpDir := testutil.OpenTestDBWithDir(t)

	cfg := config.DefaultConfig()
	broadcaster := NewBroadcaster()

	_, eventCh := broadcaster.Subscribe("")

	// Create a real git repo so the worker can read the commit
	repoDir := filepath.Join(tmpDir, "repo")
	testutil.InitTestGitRepo(t, repoDir)
	sha := testutil.GetHeadSHA(t, repoDir)

	// Create repo and job
	repo, err := db.GetOrCreateRepo(repoDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	job := testutil.CreateTestJobWithSHA(t, db, repo, sha, "test")

	// Create worker pool with our broadcaster
	pool := NewWorkerPool(db, NewStaticConfig(cfg), 1, broadcaster, nil, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to finish (done or failed)
	finalJob := testutil.WaitForJobStatus(t, db, job.ID, 10*time.Second, storage.JobStatusDone, storage.JobStatusFailed)

	if finalJob.Status != storage.JobStatusDone {
		t.Fatalf("Expected job to succeed, got status %s", finalJob.Status)
	}

	// Drain events until we find the review.completed event (bounded to prevent misleading timeout errors)
	event := waitForEventType(t, eventCh, "review.completed", 2*time.Second)

	if event.JobID != job.ID {
		t.Errorf("Expected JobID %d, got %d", job.ID, event.JobID)
	}
	if event.Agent != "test" {
		t.Errorf("Expected agent 'test', got %s", event.Agent)
	}
	if event.Verdict == "" {
		t.Error("Expected verdict to be set")
	}
}

func TestStreamMultipleEvents(t *testing.T) {
	broadcaster := NewBroadcaster()

	_, eventCh := broadcaster.Subscribe("")

	for i := 1; i <= 3; i++ {
		broadcaster.Broadcast(newTestEvent(int64(i)))
	}

	// Receive all 3 events
	for i := 1; i <= 3; i++ {
		e := testutil.ReceiveWithTimeout(t, eventCh, 500*time.Millisecond)
		if e.JobID != int64(i) {
			t.Errorf("Expected event %d, got JobID %d", i, e.JobID)
		}
	}
}
