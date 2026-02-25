package daemon

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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
	// Ignore the dynamic TS (timestamp) field for consistent comparison
	if diff := cmp.Diff(expected, got, cmpopts.IgnoreFields(Event{}, "TS")); diff != "" {
		t.Errorf("Event mismatch (-want +got):\n%s", diff)
	}
}

func assertNoEventWithin(t *testing.T, ch <-chan Event, duration time.Duration) {
	t.Helper()
	// fast path non-blocking check
	select {
	case e := <-ch:
		t.Errorf("Received unexpected event: %+v", e)
		return
	default:
	}

	// timeout-backed check
	select {
	case e := <-ch:
		t.Errorf("Received unexpected event: %+v", e)
	case <-time.After(duration):
		// OK
	}
}

func TestBroadcaster_BroadcastAndSubscribe(t *testing.T) {
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

func TestBroadcaster_MultiSubscriber(t *testing.T) {
	broadcaster := NewBroadcaster()

	_, chAll := broadcaster.Subscribe("")
	_, chRepo1 := broadcaster.Subscribe("/path/to/repo1")
	_, chRepo2 := broadcaster.Subscribe("/path/to/repo2")

	broadcaster.Broadcast(newTestEvent(123, func(e *Event) { e.Repo = "/path/to/repo1" }))

	// catch-all should receive
	e1 := testutil.ReceiveWithTimeout(t, chAll, 500*time.Millisecond)
	if e1.JobID != 123 {
		t.Errorf("Expected catch-all to receive JobID 123, got %d", e1.JobID)
	}

	// exact-match should receive
	e2 := testutil.ReceiveWithTimeout(t, chRepo1, 500*time.Millisecond)
	if e2.JobID != 123 {
		t.Errorf("Expected exact-match to receive JobID 123, got %d", e2.JobID)
	}

	// mismatch should NOT receive
	assertNoEventWithin(t, chRepo2, 100*time.Millisecond)
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

func TestBroadcaster_NonBlockingBroadcast(t *testing.T) {
	b := NewBroadcaster()

	_, ch := b.Subscribe("")

	const testBufferSize = 10 // Must match channel buffer size in NewBroadcaster

	// Fill the channel buffer
	for i := range testBufferSize {
		b.Broadcast(Event{JobID: int64(i)})
	}

	// Broadcast one more event - should not block even though channel is full
	done := make(chan bool, 1)
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

	// Verify we received the first testBufferSize events (not the dropped one)
	for i := range testBufferSize {
		e := <-ch
		if e.JobID != int64(i) {
			t.Errorf("expected JobID %d, got %d", i, e.JobID)
		}
	}

	// Channel should be empty now
	assertNoEventWithin(t, ch, 10*time.Millisecond)
}
