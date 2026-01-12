package daemon

import (
	"testing"
	"time"
)

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

	// Verify broadcaster has 2 subscribers
	eb := b.(*EventBroadcaster)
	eb.mu.RLock()
	count := len(eb.subscribers)
	eb.mu.RUnlock()
	if count != 2 {
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

	// Verify broadcaster has 0 subscribers
	eb := b.(*EventBroadcaster)
	eb.mu.RLock()
	count := len(eb.subscribers)
	eb.mu.RUnlock()
	if count != 0 {
		t.Errorf("expected 0 subscribers after unsubscribe, got %d", count)
	}
}

func TestBroadcaster_Broadcast(t *testing.T) {
	b := NewBroadcaster()

	// Subscribe two clients
	_, ch1 := b.Subscribe("")
	_, ch2 := b.Subscribe("")

	// Broadcast an event
	event := Event{
		Type:    "review.completed",
		TS:      time.Now(),
		JobID:   123,
		Repo:    "/path/to/repo",
		SHA:     "abc123",
		Agent:   "test-agent",
		Verdict: "P",
	}

	b.Broadcast(event)

	// Both subscribers should receive the event
	select {
	case e := <-ch1:
		if e.JobID != 123 {
			t.Errorf("expected JobID 123, got %d", e.JobID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("subscriber 1 did not receive event")
	}

	select {
	case e := <-ch2:
		if e.JobID != 123 {
			t.Errorf("expected JobID 123, got %d", e.JobID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("subscriber 2 did not receive event")
	}
}

func TestBroadcaster_BroadcastWithFilter(t *testing.T) {
	b := NewBroadcaster()

	// Subscribe with different filters
	_, chAll := b.Subscribe("")
	_, chRepo1 := b.Subscribe("/path/to/repo1")
	_, chRepo2 := b.Subscribe("/path/to/repo2")

	// Broadcast event for repo1
	event := Event{
		Type:    "review.completed",
		TS:      time.Now(),
		JobID:   123,
		Repo:    "/path/to/repo1",
		SHA:     "abc123",
		Agent:   "test-agent",
		Verdict: "P",
	}

	b.Broadcast(event)

	// Subscriber with no filter should receive it
	select {
	case <-chAll:
		// OK
	case <-time.After(100 * time.Millisecond):
		t.Error("subscriber with no filter did not receive event")
	}

	// Subscriber for repo1 should receive it
	select {
	case <-chRepo1:
		// OK
	case <-time.After(100 * time.Millisecond):
		t.Error("subscriber for repo1 did not receive event")
	}

	// Subscriber for repo2 should NOT receive it
	select {
	case <-chRepo2:
		t.Error("subscriber for repo2 should not have received event")
	case <-time.After(100 * time.Millisecond):
		// OK - no event received
	}
}

func TestBroadcaster_NonBlockingBroadcast(t *testing.T) {
	b := NewBroadcaster()

	// Subscribe a client
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
	select {
	case <-ch:
		t.Error("unexpected event in channel")
	case <-time.After(10 * time.Millisecond):
		// OK
	}
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
