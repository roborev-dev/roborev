package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
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

	want := []ActivityEntry{
		{Event: "daemon.started", Component: "server", Message: "started"},
		{Event: "job.enqueued", Component: "server", Message: "job enqueued", Details: map[string]string{"job_id": "42", "agent": "test"}},
	}

	for i, w := range want {
		var got ActivityEntry
		if err := decoder.Decode(&got); err != nil {
			t.Fatalf("decode entry %d: %v", i, err)
		}
		if got.Event != w.Event || got.Component != w.Component || got.Message != w.Message {
			t.Errorf("entry %d = %+v, want %+v", i, got, w)
		}
		// If both are empty (nil or len 0), we consider them equal.
		if len(got.Details) == 0 && len(w.Details) == 0 {
			continue
		}
		if !maps.Equal(got.Details, w.Details) {
			t.Errorf("entry %d details = %v, want %v", i, got.Details, w.Details)
		}
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
	if !maps.Equal(got, details) {
		t.Errorf("details = %v, want %v", got, details)
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

	const testMaxSize = 1024
	const testInterval = 10

	al, err := newActivityLogWithConfig(path, testMaxSize, testInterval)
	if err != nil {
		t.Fatalf("newActivityLogWithConfig: %v", err)
	}
	defer al.Close()

	// We need > testMaxSize (1KB) written before the check at testInterval (10).
	// 1KB / 10 = 102 bytes per entry minimum. Use a 200 byte payload.
	bigMsg := string(bytes.Repeat([]byte("a"), 200))
	// Write testInterval + 2 entries. The first testInterval entries will exceed testMaxSize
	// and trigger truncation on the next maybeRotate check. The remaining 2 entries will be
	// written to the new file, which will be well under testMaxSize.
	for i := range testInterval + 2 {
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
	if info.Size() > testMaxSize {
		t.Errorf(
			"file should be under cap after rotation, got %d bytes",
			info.Size(),
		)
	}
}

func TestActivityLog_RotationRecovery(t *testing.T) {
	// Exercises maybeRotate's happy path: trigger rotation via
	// writeCount + oversized file, then verify the log continues
	// working after truncate-reopen. The O_TRUNC-failure fallback
	// branch (line 178 of activitylog.go) is not testable without
	// injecting a mock opener, because on Unix O_TRUNC on an
	// existing writable file always succeeds.
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.log")

	const testMaxSize = 1024
	const testInterval = 2

	al, err := newActivityLogWithConfig(path, testMaxSize, testInterval)
	if err != nil {
		t.Fatalf("newActivityLogWithConfig: %v", err)
	}
	defer al.Close()

	// First write shouldn't trigger rotation yet (writeCount < testInterval)
	al.Log("test", "test", "msg1", nil)

	// Second write triggers rotation because testInterval is 2
	// Use a payload bigger than the cap so stat triggers rotation on next check.
	bigMsg := string(make([]byte, testMaxSize+1024))
	al.Log("test", "test", bigMsg, nil)

	// After rotation, writing should still work (file was reopened)
	al.Log("post-rotate", "test", "still logging", nil)

	entries := al.RecentN(1)
	if len(entries) == 0 {
		t.Fatal("expected entries after rotation")
	}
	if entries[0].Event != "post-rotate" {
		t.Errorf("got event %q, want %q", entries[0].Event, "post-rotate")
	}

	// Verify the file exists and has content
	al.Close()
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("log file should exist after rotation: %v", statErr)
	}
	if info.Size() == 0 {
		t.Error("log file should have content after post-rotate write")
	}
}
