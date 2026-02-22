package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func createTestActivityLog(t *testing.T) (*ActivityLog, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.log")
	al, err := NewActivityLog(path)
	if err != nil {
		t.Fatalf("NewActivityLog failed: %v", err)
	}
	t.Cleanup(func() { al.Close() })
	return al, path
}

func TestActivityLog_WriteAndRecent(t *testing.T) {
	al, _ := createTestActivityLog(t)

	tests := []struct {
		event     string
		component string
		message   string
	}{
		{"daemon.started", "server", "daemon started on :7373"},
		{"job.enqueued", "server", "job 1 enqueued"},
		{"job.started", "worker", "job 1 started"},
		{"job.completed", "worker", "job 1 completed"},
	}

	for _, tt := range tests {
		al.Log(tt.event, tt.component, tt.message, nil)
	}

	recent := al.Recent()
	if len(recent) != len(tests) {
		t.Fatalf("expected %d entries, got %d", len(tests), len(recent))
	}

	// Newest first
	for i, tt := range tests {
		got := recent[len(tests)-1-i]
		if got.Event != tt.event {
			t.Errorf("entry %d: event = %q, want %q", i, got.Event, tt.event)
		}
		if got.Component != tt.component {
			t.Errorf("entry %d: component = %q, want %q", i, got.Component, tt.component)
		}
		if got.Message != tt.message {
			t.Errorf("entry %d: message = %q, want %q", i, got.Message, tt.message)
		}
	}
}

func TestActivityLog_RecentN(t *testing.T) {
	al, _ := createTestActivityLog(t)

	for i := range 10 {
		al.Log("test", "test", fmt.Sprintf("msg %d", i), nil)
	}

	got := al.RecentN(3)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	if got[0].Message != "msg 9" {
		t.Errorf("first entry = %q, want %q", got[0].Message, "msg 9")
	}

	// When n exceeds count, return all
	got = al.RecentN(100)
	if len(got) != 10 {
		t.Errorf("expected 10 entries, got %d", len(got))
	}
}

func TestActivityLog_RingBuffer(t *testing.T) {
	al, _ := createTestActivityLog(t)

	// Write more than capacity
	total := activityLogCapacity + 100
	for i := range total {
		al.Log("test", "test", fmt.Sprintf("msg %d", i), nil)
	}

	recent := al.Recent()
	if len(recent) != activityLogCapacity {
		t.Fatalf(
			"expected %d entries (ring buffer cap), got %d",
			activityLogCapacity, len(recent),
		)
	}

	// Newest should be the last written
	if recent[0].Message != fmt.Sprintf("msg %d", total-1) {
		t.Errorf(
			"newest = %q, want %q",
			recent[0].Message,
			fmt.Sprintf("msg %d", total-1),
		)
	}

	// Oldest should be total - capacity
	oldest := recent[activityLogCapacity-1]
	want := fmt.Sprintf("msg %d", total-activityLogCapacity)
	if oldest.Message != want {
		t.Errorf("oldest = %q, want %q", oldest.Message, want)
	}
}

func TestActivityLog_FileFormat(t *testing.T) {
	al, path := createTestActivityLog(t)

	al.Log("daemon.started", "server", "started", nil)
	al.Log(
		"job.enqueued", "server", "job enqueued",
		map[string]string{"job_id": "42", "agent": "test"},
	)
	al.Close()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(content))

	// First entry
	var e1 ActivityEntry
	if err := decoder.Decode(&e1); err != nil {
		t.Fatalf("decode first entry: %v", err)
	}
	if e1.Event != "daemon.started" {
		t.Errorf("first event = %q, want %q", e1.Event, "daemon.started")
	}
	if e1.Details != nil {
		t.Errorf("first entry details should be nil, got %v", e1.Details)
	}

	// Second entry
	var e2 ActivityEntry
	if err := decoder.Decode(&e2); err != nil {
		t.Fatalf("decode second entry: %v", err)
	}
	if e2.Event != "job.enqueued" {
		t.Errorf("second event = %q, want %q", e2.Event, "job.enqueued")
	}
	if e2.Details["job_id"] != "42" {
		t.Errorf("details[job_id] = %q, want %q", e2.Details["job_id"], "42")
	}
	if e2.Details["agent"] != "test" {
		t.Errorf("details[agent] = %q, want %q", e2.Details["agent"], "test")
	}
}

func TestActivityLog_Details(t *testing.T) {
	al, _ := createTestActivityLog(t)

	details := map[string]string{
		"version": "1.0.0",
		"addr":    "127.0.0.1:7373",
		"pid":     "1234",
	}
	al.Log("daemon.started", "server", "started", details)

	recent := al.Recent()
	if len(recent) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(recent))
	}

	got := recent[0].Details
	for k, v := range details {
		if got[k] != v {
			t.Errorf("details[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestActivityLog_DetailsCopied(t *testing.T) {
	al, _ := createTestActivityLog(t)

	details := map[string]string{"key": "original"}
	al.Log("test", "test", "msg", details)

	// Mutate the caller's map after logging
	details["key"] = "mutated"
	details["extra"] = "added"

	recent := al.Recent()
	if recent[0].Details["key"] != "original" {
		t.Errorf("stored details mutated: got %q, want %q",
			recent[0].Details["key"], "original")
	}
	if _, ok := recent[0].Details["extra"]; ok {
		t.Error("stored details gained extra key from caller mutation")
	}

	// Mutate the returned entry's map
	recent[0].Details["key"] = "returned-mutated"
	again := al.Recent()
	if again[0].Details["key"] != "original" {
		t.Errorf("ring buffer mutated via returned entry: got %q, want %q",
			again[0].Details["key"], "original")
	}
}

func TestActivityLog_RecentN_Negative(t *testing.T) {
	al, _ := createTestActivityLog(t)
	al.Log("test", "test", "msg", nil)

	got := al.RecentN(-1)
	if got != nil {
		t.Errorf("RecentN(-1) = %v, want nil", got)
	}

	got = al.RecentN(0)
	if got != nil {
		t.Errorf("RecentN(0) = %v, want nil", got)
	}
}

func TestActivityLog_FileTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.log")

	// Create a file exceeding the threshold
	data := make([]byte, maxActivityLogSize+1)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	al, err := NewActivityLog(path)
	if err != nil {
		t.Fatalf("NewActivityLog: %v", err)
	}
	defer al.Close()

	// Write an entry and verify the file is small (old content gone)
	al.Log("test", "test", "after truncation", nil)
	al.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() > 1024 {
		t.Errorf("file should be small after truncation, got %d bytes",
			info.Size())
	}
}

func TestActivityLog_RuntimeRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.log")

	al, err := NewActivityLog(path)
	if err != nil {
		t.Fatalf("NewActivityLog: %v", err)
	}
	defer al.Close()

	// Each entry is ~200 bytes. We need > maxActivityLogSize (5MB)
	// bytes written before the check at rotateCheckInterval (1000).
	// 5MB / 1000 = 5KB per entry minimum. Use a ~6KB payload.
	bigMsg := string(make([]byte, 6*1024))
	for i := range rotateCheckInterval + 50 {
		al.Log("test", "test", fmt.Sprintf("entry-%d", i),
			map[string]string{"big": bigMsg})
	}

	al.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// After rotation the file should be well under the cap.
	// The exact size depends on how many entries were written after
	// the truncate, but it should be far smaller than the cap.
	if info.Size() > maxActivityLogSize {
		t.Errorf(
			"file should be under cap after rotation, got %d bytes",
			info.Size(),
		)
	}
}
