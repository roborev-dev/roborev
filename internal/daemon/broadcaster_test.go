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
	select {
	case e := <-ch:
		t.Errorf("Received unexpected event: %+v", e)
	case <-time.After(duration):
		// OK
	}
}

func TestBroadcaster_BroadcastAndSubscribe(t *testing.T) {
	broadcaster := NewBroadcaster()

	id, eventCh := broadcaster.Subscribe("")
	t.Cleanup(func() { broadcaster.Unsubscribe(id) })

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

	id, eventCh := broadcaster.Subscribe("/path/to/repo1")
	t.Cleanup(func() { broadcaster.Unsubscribe(id) })

	broadcaster.Broadcast(newTestEvent(1, func(e *Event) { e.Repo = "/path/to/repo1"; e.SHA = "sha1" }))
	broadcaster.Broadcast(newTestEvent(2, func(e *Event) { e.Repo = "/path/to/repo2"; e.SHA = "sha2"; e.Verdict = "F" }))
	broadcaster.Broadcast(newTestEvent(3, func(e *Event) { e.Repo = "/path/to/repo1"; e.SHA = "sha3" }))

	// Should receive only events for repo1 (JobID 1 and 3)
	e1 := testutil.ReceiveWithTimeout(t, eventCh, 500*time.Millisecond)
	e2 := testutil.ReceiveWithTimeout(t, eventCh, 500*time.Millisecond)

	// Should not receive more events (repo2 event was filtered out)
	assertNoEventWithin(t, eventCh, 100*time.Millisecond)

	assertEventFields(t, e1, newTestEvent(1, func(e *Event) { e.Repo = "/path/to/repo1"; e.SHA = "sha1" }))
	assertEventFields(t, e2, newTestEvent(3, func(e *Event) { e.Repo = "/path/to/repo1"; e.SHA = "sha3" }))
}

func TestStreamMultipleEvents(t *testing.T) {
	broadcaster := NewBroadcaster()

	id, eventCh := broadcaster.Subscribe("")
	t.Cleanup(func() { broadcaster.Unsubscribe(id) })

	for i := 1; i <= 3; i++ {
		broadcaster.Broadcast(newTestEvent(int64(i)))
	}

	// Receive all 3 events
	for i := 1; i <= 3; i++ {
		e := testutil.ReceiveWithTimeout(t, eventCh, 500*time.Millisecond)
		assertEventFields(t, e, newTestEvent(int64(i)))
	}
}

func TestBroadcaster_MultiSubscriber(t *testing.T) {
	broadcaster := NewBroadcaster()

	idAll, chAll := broadcaster.Subscribe("")
	t.Cleanup(func() { broadcaster.Unsubscribe(idAll) })

	idRepo1, chRepo1 := broadcaster.Subscribe("/path/to/repo1")
	t.Cleanup(func() { broadcaster.Unsubscribe(idRepo1) })

	idRepo2, chRepo2 := broadcaster.Subscribe("/path/to/repo2")
	t.Cleanup(func() { broadcaster.Unsubscribe(idRepo2) })

	expectedEvent := newTestEvent(123, func(e *Event) { e.Repo = "/path/to/repo1" })
	broadcaster.Broadcast(expectedEvent)

	// catch-all should receive
	e1 := testutil.ReceiveWithTimeout(t, chAll, 500*time.Millisecond)
	assertEventFields(t, e1, expectedEvent)

	// exact-match should receive
	e2 := testutil.ReceiveWithTimeout(t, chRepo1, 500*time.Millisecond)
	assertEventFields(t, e2, expectedEvent)

	// mismatch should NOT receive
	assertNoEventWithin(t, chRepo2, 100*time.Millisecond)
}

func TestBroadcaster_Subscribe(t *testing.T) {
	b := NewBroadcaster()

	// Subscribe without filter
	id1, ch1 := b.Subscribe("")
	t.Cleanup(func() { b.Unsubscribe(id1) })

	// Subscribe with filter
	id2, ch2 := b.Subscribe("/path/to/repo")
	t.Cleanup(func() { b.Unsubscribe(id2) })

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

	id, ch := b.Subscribe("")
	t.Cleanup(func() { b.Unsubscribe(id) })

	const testBufferSize = DefaultBufferSize // Must match channel buffer size in NewBroadcaster

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
		assertEventFields(t, e, Event{JobID: int64(i)})
	}

	// Channel should be empty now
	assertNoEventWithin(t, ch, 10*time.Millisecond)
}
