package daemon

import (
	"testing"
	"time"
)

const (
	testTimeout    = 100 * time.Millisecond
	testBufferSize = 10 // Must match channel buffer size in NewBroadcaster
)

// assertEventReceived waits for an event or fails the test if it times out.
func assertEventReceived(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

// assertNoEventReceived verifies no event arrives within a short window.
func assertNoEventReceived(t *testing.T, ch <-chan Event) {
	t.Helper()
	// Small sleep to allow asynchronous goroutines to flush
	time.Sleep(1 * time.Millisecond)
	select {
	case e := <-ch:
		t.Fatalf("received unexpected event: %v", e)
	default:
		// OK
	}
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

	// Subscribe with filter
	id2, ch2 := b.Subscribe("/path/to/repo")

	// Verify IDs are different
	if id1 == id2 {
		t.Errorf("expected distinct subscriber IDs, got %v for both", id1)
	}

	// Verify channels are different
	if ch1 == ch2 {
		t.Error("subscriber channels should be different")
	}

	if count := b.SubscriberCount(); count != 2 {
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

	if count := b.SubscriberCount(); count != 0 {
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
	tests := []struct {
		name        string
		subFilter   string
		eventRepo   string
		shouldMatch bool
	}{
		{"CatchAll", "", "/path/to/repo1", true},
		{"ExactMatch", "/path/to/repo1", "/path/to/repo1", true},
		{"Mismatch", "/path/to/repo2", "/path/to/repo1", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBroadcaster()
			_, ch := b.Subscribe(tc.subFilter)
			b.Broadcast(makeTestEvent(123, tc.eventRepo))

			if tc.shouldMatch {
				assertEventReceived(t, ch)
			} else {
				assertNoEventReceived(t, ch)
			}
		})
	}
}

func TestBroadcaster_NonBlockingBroadcast(t *testing.T) {
	b := NewBroadcaster()

	_, ch := b.Subscribe("")

	// Fill the channel buffer
	for i := range testBufferSize {
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
	case <-time.After(testTimeout):
		t.Error("broadcast blocked when channel was full")
	}

	// Verify we received the first testBufferSize events (not the dropped one)
	for i := range testBufferSize {
		e := <-ch
		if e.JobID != int64(i) {
			t.Errorf("expected JobID %d, got %d", i, e.JobID)
		}
	}

	// Channel should be empty now
	assertNoEventReceived(t, ch)
}
