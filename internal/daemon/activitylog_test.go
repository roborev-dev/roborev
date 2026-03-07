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

func assertEntryEqual(t *testing.T, i int, got, want ActivityEntry) {
	t.Helper()
	if got.Event != want.Event || got.Component != want.Component || got.Message != want.Message {
		t.Errorf("entry %d: basic fields mismatch: got %+v, want %+v", i, got, want)
	}
	if len(got.Details) > 0 || len(want.Details) > 0 {
		if !maps.Equal(got.Details, want.Details) {
			t.Errorf("entry %d details = %v, want %v", i, got.Details, want.Details)
		}
	}
}

func readLogEntries(t *testing.T, path string) []ActivityEntry {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var entries []ActivityEntry
	decoder := json.NewDecoder(bytes.NewReader(content))
	for decoder.More() {
		var entry ActivityEntry
		if err := decoder.Decode(&entry); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestActivityLog_WriteAndRecent(t *testing.T) {
	al, _ := createTestActivityLog(t)

	tests := []ActivityEntry{
		{Event: "daemon.started", Component: "server", Message: "daemon started on :7373"},
		{Event: "job.enqueued", Component: "server", Message: "job 1 enqueued"},
		{Event: "job.started", Component: "worker", Message: "job 1 started"},
		{Event: "job.completed", Component: "worker", Message: "job 1 completed"},
	}

	for _, tt := range tests {
		al.Log(tt.Event, tt.Component, tt.Message, nil)
	}

	recent := al.Recent()
	if len(recent) != len(tests) {
		t.Fatalf("expected %d entries, got %d", len(tests), len(recent))
	}

	// Newest first
	for i, tt := range tests {
		got := recent[len(tests)-1-i]
		assertEntryEqual(t, i, got, tt)
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

	want := []ActivityEntry{
		{Event: "daemon.started", Component: "server", Message: "started"},
		{Event: "job.enqueued", Component: "server", Message: "job enqueued", Details: map[string]string{"job_id": "42", "agent": "test"}},
	}

	gotEntries := readLogEntries(t, path)
	if len(gotEntries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(gotEntries), len(want))
	}
	for i, w := range want {
		assertEntryEqual(t, i, gotEntries[i], w)
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

	for _, n := range []int{-1, 0} {
		if got := al.RecentN(n); got != nil {
			t.Errorf("RecentN(%d) = %v, want nil", n, got)
		}
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
