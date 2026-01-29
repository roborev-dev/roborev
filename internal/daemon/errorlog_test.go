package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func createTestErrorLog(t *testing.T) (*ErrorLog, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "errors.log")
	el, err := NewErrorLog(path)
	if err != nil {
		t.Fatalf("NewErrorLog failed: %v", err)
	}
	t.Cleanup(func() { el.Close() })
	return el, path
}

func seedErrorLog(el *ErrorLog, count int) {
	for i := 1; i <= count; i++ {
		el.Log("error", "worker", "error", int64(i))
	}
}

func TestNewErrorLog(t *testing.T) {
	el, path := createTestErrorLog(t)

	if el == nil {
		t.Fatal("NewErrorLog returned nil")
	}

	// Verify file was created
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("Error log file was not created")
	}
}

func TestErrorLogLog(t *testing.T) {
	el, path := createTestErrorLog(t)

	// Log an error
	el.Log("error", "worker", "test error message", 123)
	el.Close()

	// Verify file content
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read error log: %v", err)
	}

	var entry ErrorEntry
	if err := json.Unmarshal(content[:len(content)-1], &entry); err != nil { // -1 to remove newline
		t.Fatalf("Failed to parse error entry: %v", err)
	}

	if entry.Level != "error" {
		t.Errorf("Expected level 'error', got %q", entry.Level)
	}
	if entry.Component != "worker" {
		t.Errorf("Expected component 'worker', got %q", entry.Component)
	}
	if entry.Message != "test error message" {
		t.Errorf("Expected message 'test error message', got %q", entry.Message)
	}
	if entry.JobID != 123 {
		t.Errorf("Expected job_id 123, got %d", entry.JobID)
	}
}

func TestErrorLogRecent(t *testing.T) {
	el, _ := createTestErrorLog(t)

	// Log several errors
	for i := 1; i <= 5; i++ {
		el.Log("error", "worker", "error "+string(rune('0'+i)), int64(i))
	}

	// Get recent errors - should be in reverse order (newest first)
	recent := el.Recent()
	if len(recent) != 5 {
		t.Fatalf("Expected 5 recent errors, got %d", len(recent))
	}

	// First entry should be the most recent (error 5)
	if !strings.Contains(recent[0].Message, "5") {
		t.Errorf("Expected first error to be '5', got %q", recent[0].Message)
	}
	// Last entry should be the oldest (error 1)
	if !strings.Contains(recent[4].Message, "1") {
		t.Errorf("Expected last error to be '1', got %q", recent[4].Message)
	}
}

func TestErrorLogRecentN(t *testing.T) {
	el, _ := createTestErrorLog(t)

	seedErrorLog(el, 10)

	// Get only 3 recent errors
	recent := el.RecentN(3)
	if len(recent) != 3 {
		t.Errorf("Expected 3 recent errors, got %d", len(recent))
	}
}

func TestErrorLogCount24h(t *testing.T) {
	el, _ := createTestErrorLog(t)

	seedErrorLog(el, 5)

	count := el.Count24h()
	if count != 5 {
		t.Errorf("Expected count 5, got %d", count)
	}
}

func TestErrorLogRingBuffer(t *testing.T) {
	el, _ := createTestErrorLog(t)

	seedErrorLog(el, 150)

	// Should only have the last 100 errors
	recent := el.Recent()
	if len(recent) != 100 {
		t.Errorf("Expected 100 recent errors (ring buffer limit), got %d", len(recent))
	}

	// The oldest should be #51 (first 50 were evicted)
	if recent[99].JobID != 51 {
		t.Errorf("Expected oldest error to be #51, got #%d", recent[99].JobID)
	}
}

func TestErrorLogConcurrency(t *testing.T) {
	el, _ := createTestErrorLog(t)

	// Log from multiple goroutines
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				el.Log("error", "worker", "concurrent error", int64(id*10+j))
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout waiting for concurrent logging")
		}
	}

	// Should have 100 entries
	recent := el.Recent()
	if len(recent) != 100 {
		t.Errorf("Expected 100 entries, got %d", len(recent))
	}
}
