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
