package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestStreamEventsEndToEnd(t *testing.T) {
	broadcaster := NewBroadcaster()

	// Subscribe directly to test
	_, eventCh := broadcaster.Subscribe("")

	// Broadcast a test event
	testEvent := Event{
		Type:    "review.completed",
		TS:      time.Now(),
		JobID:   42,
		Repo:    "/path/to/repo",
		SHA:     "abc123",
		Agent:   "test-agent",
		Verdict: "P",
	}
	broadcaster.Broadcast(testEvent)

	// Receive the event
	select {
	case receivedEvent := <-eventCh:
		// Verify event fields
		if receivedEvent.Type != "review.completed" {
			t.Errorf("Expected type 'review.completed', got %s", receivedEvent.Type)
		}
		if receivedEvent.JobID != 42 {
			t.Errorf("Expected JobID 42, got %d", receivedEvent.JobID)
		}
		if receivedEvent.Repo != "/path/to/repo" {
			t.Errorf("Expected Repo '/path/to/repo', got %s", receivedEvent.Repo)
		}
		if receivedEvent.SHA != "abc123" {
			t.Errorf("Expected SHA 'abc123', got %s", receivedEvent.SHA)
		}
		if receivedEvent.Agent != "test-agent" {
			t.Errorf("Expected Agent 'test-agent', got %s", receivedEvent.Agent)
		}
		if receivedEvent.Verdict != "P" {
			t.Errorf("Expected Verdict 'P', got %s", receivedEvent.Verdict)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Did not receive event")
	}
}

func TestStreamEventsWithRepoFilter(t *testing.T) {
	broadcaster := NewBroadcaster()

	// Subscribe with repo filter
	_, eventCh := broadcaster.Subscribe("/path/to/repo1")

	// Broadcast events for different repos
	broadcaster.Broadcast(Event{
		Type:    "review.completed",
		TS:      time.Now(),
		JobID:   1,
		Repo:    "/path/to/repo1",
		SHA:     "sha1",
		Agent:   "test",
		Verdict: "P",
	})

	broadcaster.Broadcast(Event{
		Type:    "review.completed",
		TS:      time.Now(),
		JobID:   2,
		Repo:    "/path/to/repo2", // Different repo - should be filtered
		SHA:     "sha2",
		Agent:   "test",
		Verdict: "F",
	})

	broadcaster.Broadcast(Event{
		Type:    "review.completed",
		TS:      time.Now(),
		JobID:   3,
		Repo:    "/path/to/repo1", // Same as filter
		SHA:     "sha3",
		Agent:   "test",
		Verdict: "P",
	})

	// Should receive only events for repo1 (JobID 1 and 3)
	var receivedEvents []Event

	// Receive first event
	select {
	case e := <-eventCh:
		receivedEvents = append(receivedEvents, e)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Did not receive first event")
	}

	// Receive second event
	select {
	case e := <-eventCh:
		receivedEvents = append(receivedEvents, e)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Did not receive second event")
	}

	// Should not receive third event (different repo)
	select {
	case <-eventCh:
		t.Error("Received unexpected event for filtered repo")
	case <-time.After(100 * time.Millisecond):
		// OK - no event received
	}

	if len(receivedEvents) != 2 {
		t.Fatalf("Expected 2 events, got %d", len(receivedEvents))
	}

	if receivedEvents[0].JobID != 1 {
		t.Errorf("Expected first event JobID 1, got %d", receivedEvents[0].JobID)
	}
	if receivedEvents[1].JobID != 3 {
		t.Errorf("Expected second event JobID 3, got %d", receivedEvents[1].JobID)
	}
}

func TestStreamEventsMethodNotAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

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
	// Test that worker actually broadcasts events
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	cfg := config.DefaultConfig()
	broadcaster := NewBroadcaster()

	// Subscribe to events
	_, eventCh := broadcaster.Subscribe("")

	// Create repo and job
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	commit, err := db.GetOrCreateCommit(repo.ID, "testsha", "Author", "Test", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	job, err := db.EnqueueJob(repo.ID, commit.ID, "testsha", "", "test", "", "")
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	// Create worker pool with our broadcaster
	pool := NewWorkerPool(db, NewStaticConfig(cfg), 1, broadcaster, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to complete
	deadline := time.Now().Add(10 * time.Second)
	var finalJob *storage.ReviewJob
	for time.Now().Before(deadline) {
		finalJob, err = db.GetJobByID(job.ID)
		if err != nil {
			t.Fatalf("GetJobByID failed: %v", err)
		}
		if finalJob.Status == storage.JobStatusDone || finalJob.Status == storage.JobStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// If job completed successfully, verify we received event
	if finalJob.Status == storage.JobStatusDone {
		select {
		case event := <-eventCh:
			if event.Type != "review.completed" {
				t.Errorf("Expected type 'review.completed', got %s", event.Type)
			}
			if event.JobID != job.ID {
				t.Errorf("Expected JobID %d, got %d", job.ID, event.JobID)
			}
			if event.Agent != "test" {
				t.Errorf("Expected agent 'test', got %s", event.Agent)
			}
			if event.Verdict == "" {
				t.Error("Expected verdict to be set")
			}
		case <-time.After(1 * time.Second):
			t.Error("Did not receive completion event from worker")
		}
	}
}

func TestStreamMultipleEvents(t *testing.T) {
	broadcaster := NewBroadcaster()

	// Subscribe
	_, eventCh := broadcaster.Subscribe("")

	// Broadcast multiple events
	for i := 1; i <= 3; i++ {
		broadcaster.Broadcast(Event{
			Type:    "review.completed",
			TS:      time.Now(),
			JobID:   int64(i),
			Repo:    "/test/repo",
			SHA:     "sha",
			Agent:   "test",
			Verdict: "P",
		})
	}

	// Receive all 3 events
	count := 0
	for i := 0; i < 3; i++ {
		select {
		case <-eventCh:
			count++
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("Did not receive event %d", i+1)
		}
	}

	if count != 3 {
		t.Errorf("Expected 3 events, got %d", count)
	}
}
