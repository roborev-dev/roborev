package daemon

import (
	"testing"
	"time"
)

// assertEventReceived waits for an event or fails the test if it times out.
func assertEventReceived(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

// assertNoEventReceived verifies no event arrives within a short window.
func assertNoEventReceived(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("received unexpected event: %v", e)
	case <-time.After(100 * time.Millisecond):
		// OK
	}
}

// getSubscriberCount safely retrieves the current number of subscribers.
func getSubscriberCount(t *testing.T, b Broadcaster) int {
	t.Helper()
	eb, ok := b.(*EventBroadcaster)
	if !ok {
		t.Fatal("Broadcaster is not *EventBroadcaster")
	}
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return len(eb.subscribers)
}

// makeTestEvent creates a valid event with the given job ID and repo.
func makeTestEvent(jobID int64, repo string) Event {
	return Event{
		Type:    "review.completed",
		TS:      time.Now(),
		JobID:   jobID,
		Repo:    repo,
		SHA:     "abc123",
		Agent:   "test-agent",
		Verdict: "P",
	}
}

func TestBroadcaster_Subscribe(t *testing.T) {
	b := NewBroadcaster()

	// Subscribe without filter
	id1, ch1 := b.Subscribe("")
	if id1 != 1 {
		t.Errorf("expected first subscriber ID to be 1, got %d", id1)
	}

	// Subscribe with filter
	id2, ch2 := b.Subscribe("/path/to/repo")
	if id2 != 2 {
		t.Errorf("expected second subscriber ID to be 2, got %d", id2)
	}

	// Verify channels are different
	if ch1 == ch2 {
		t.Error("subscriber channels should be different")
	}

	if count := getSubscriberCount(t, b); count != 2 {
		t.Errorf("expected 2 subscribers, got %d", count)
	}
}

func TestBroadcaster_Unsubscribe(t *testing.T) {
	b := NewBroadcaster()

	id, ch := b.Subscribe("")

	// Unsubscribe
	b.Unsubscribe(id)

	// Verify channel is closed
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}

	if count := getSubscriberCount(t, b); count != 0 {
		t.Errorf("expected 0 subscribers after unsubscribe, got %d", count)
	}
}

func TestBroadcaster_Broadcast(t *testing.T) {
	b := NewBroadcaster()

	_, ch1 := b.Subscribe("")
	_, ch2 := b.Subscribe("")

	b.Broadcast(makeTestEvent(123, "/path/to/repo"))

	e1 := assertEventReceived(t, ch1)
	if e1.JobID != 123 {
		t.Errorf("expected JobID 123, got %d", e1.JobID)
	}

	e2 := assertEventReceived(t, ch2)
	if e2.JobID != 123 {
		t.Errorf("expected JobID 123, got %d", e2.JobID)
	}
}

func TestBroadcaster_BroadcastWithFilter(t *testing.T) {
	b := NewBroadcaster()

	_, chAll := b.Subscribe("")
	_, chRepo1 := b.Subscribe("/path/to/repo1")
	_, chRepo2 := b.Subscribe("/path/to/repo2")

	b.Broadcast(makeTestEvent(123, "/path/to/repo1"))

	assertEventReceived(t, chAll)
	assertEventReceived(t, chRepo1)
	assertNoEventReceived(t, chRepo2)
}

func TestBroadcaster_NonBlockingBroadcast(t *testing.T) {
	b := NewBroadcaster()

	_, ch := b.Subscribe("")

	// Fill the channel buffer (capacity is 10)
	for i := 0; i < 10; i++ {
		b.Broadcast(Event{JobID: int64(i)})
	}

	// Broadcast one more event - should not block even though channel is full
	done := make(chan bool)
	go func() {
		b.Broadcast(Event{JobID: 999})
		done <- true
	}()

	select {
	case <-done:
		// OK - broadcast didn't block
	case <-time.After(100 * time.Millisecond):
		t.Error("broadcast blocked when channel was full")
	}

	// Verify we received the first 10 events (not the dropped one)
	for i := 0; i < 10; i++ {
		e := <-ch
		if e.JobID != int64(i) {
			t.Errorf("expected JobID %d, got %d", i, e.JobID)
		}
	}

	// Channel should be empty now
	assertNoEventReceived(t, ch)
}

func TestEvent_MarshalJSON(t *testing.T) {
	event := Event{
		Type:     "review.completed",
		TS:       time.Date(2026, 1, 11, 10, 0, 30, 0, time.UTC),
		JobID:    42,
		Repo:     "/path/to/myrepo",
		RepoName: "myrepo",
		SHA:      "abc123",
		Agent:    "claude-code",
		Verdict:  "F",
	}

	data, err := event.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	expected := `{"type":"review.completed","ts":"2026-01-11T10:00:30Z","job_id":42,"repo":"/path/to/myrepo","repo_name":"myrepo","sha":"abc123","agent":"claude-code","verdict":"F"}`
	got := string(data)

	if got != expected {
		t.Errorf("JSON mismatch\nexpected: %s\ngot:      %s", expected, got)
	}
}
