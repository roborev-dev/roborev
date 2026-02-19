package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// simpleNormalizer matches the common pattern used in Writer tests.
func simpleNormalizer(line string) *OutputLine {
	return &OutputLine{Text: line, Type: "text"}
}

// assertLines verifies the count and text content of lines for a job.
func assertLines(t *testing.T, lines []OutputLine, expectedTexts ...string) {
	t.Helper()
	if len(lines) != len(expectedTexts) {
		t.Fatalf("expected %d lines, got %d", len(expectedTexts), len(lines))
	}
	for i, want := range expectedTexts {
		if lines[i].Text != want {
			t.Errorf("line[%d]: expected %q, got %q", i, want, lines[i].Text)
		}
	}
}

func TestOutputBuffer_Append(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)

	ob.Append(1, OutputLine{Text: "line 1", Type: "text"})
	ob.Append(1, OutputLine{Text: "line 2", Type: "tool"})

	lines := ob.GetLines(1)
	assertLines(t, lines, "line 1", "line 2")

	if lines[1].Type != "tool" {
		t.Errorf("expected type 'tool', got %q", lines[1].Type)
	}
}

func TestOutputBuffer_GetLinesEmpty(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)

	lines := ob.GetLines(999)
	if lines != nil {
		t.Errorf("expected nil for non-existent job, got %v", lines)
	}
}

func TestOutputBuffer_Limits(t *testing.T) {
	type action struct {
		jobID int64
		text  string
	}

	tests := []struct {
		name        string
		jobLimit    int
		globalLimit int
		actions     []action
		expectLines map[int64][]string
	}{
		{
			name:        "PerJobLimit_Eviction",
			jobLimit:    50,
			globalLimit: 1000,
			actions: []action{
				{1, "12345678901234567890"}, // 20b
				{1, "12345678901234567890"}, // 20b
				{1, "12345678901234567890"}, // 20b - should evict first
			},
			expectLines: map[int64][]string{
				1: {"12345678901234567890", "12345678901234567890"},
			},
		},
		{
			name:        "GlobalLimit_Drop",
			jobLimit:    30,
			globalLimit: 50,
			actions: []action{
				{1, "12345678901234567890"}, // 20b
				{2, "12345678901234567890"}, // 20b
				{3, "12345678901234567890"}, // 20b - would exceed 50, dropped
			},
			expectLines: map[int64][]string{
				1: {"12345678901234567890"},
				2: {"12345678901234567890"},
				3: {},
			},
		},
		{
			name:        "OversizedLine_Drop",
			jobLimit:    20,
			globalLimit: 1000,
			actions: []action{
				{1, "this line is way too long to fit in buffer"}, // 43b > 20b
				{1, "short"}, // 5b
			},
			expectLines: map[int64][]string{
				1: {"short"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ob := NewOutputBuffer(tt.jobLimit, tt.globalLimit)
			for _, act := range tt.actions {
				ob.Append(act.jobID, OutputLine{Text: act.text, Type: "text"})
			}

			for jobID, want := range tt.expectLines {
				assertLines(t, ob.GetLines(jobID), want...)
			}
		})
	}
}

func TestOutputBuffer_GlobalLimitPreservesExistingLines(t *testing.T) {
	// Scenario: job has lines, per-job eviction would occur, but global limit rejects.
	// Existing lines should be preserved (not evicted for nothing).
	// Per-job: 50 bytes, Global: 80 bytes
	ob := NewOutputBuffer(50, 80)

	// Job 1: add 40 bytes (two 20-byte lines)
	ob.Append(1, OutputLine{Text: "12345678901234567890", Type: "text"}) // 20 bytes
	ob.Append(1, OutputLine{Text: "12345678901234567890", Type: "text"}) // 20 bytes, job1=40, total=40

	// Job 2: add 30 bytes
	ob.Append(2, OutputLine{Text: "123456789012345678901234567890", Type: "text"}) // 30 bytes, total=70

	// Verify initial state
	assertLines(t, ob.GetLines(1), "12345678901234567890", "12345678901234567890")
	assertLines(t, ob.GetLines(2), "123456789012345678901234567890")

	// Now try to add 20 bytes to job 1
	// Per-job: 40+20=60 > 50, would need to evict 20 bytes (1 line)
	// After eviction: job1=40, but total would be 70-20+20=70, still under 80
	// This SHOULD succeed
	ob.Append(1, OutputLine{Text: "AAAAAAAAAAAAAAAAAAAA", Type: "text"}) // 20 bytes

	assertLines(t, ob.GetLines(1), "12345678901234567890", "AAAAAAAAAAAAAAAAAAAA")

	// Now global is at 70. Try to add 20 more bytes to job 1.
	// Per-job: 40+20=60 > 50, would evict 20 bytes
	// After eviction: total would be 70-20+20=70, under 80
	// This SHOULD succeed
	ob.Append(1, OutputLine{Text: "BBBBBBBBBBBBBBBBBBBB", Type: "text"}) // 20 bytes

	assertLines(t, ob.GetLines(1), "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB")

	// Now try to add 15 bytes to job 2 (total would be 70+15=85 > 80)
	// Per-job: 30+15=45 < 50, no eviction
	// Global: 70+15=85 > 80, REJECTED
	// Job 2 should keep its original line
	ob.Append(2, OutputLine{Text: "123456789012345", Type: "text"}) // 15 bytes - rejected

	assertLines(t, ob.GetLines(2), "123456789012345678901234567890")
}

func TestOutputBuffer_CloseJob(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)

	ob.Append(1, OutputLine{Text: "test", Type: "text"})
	if !ob.IsActive(1) {
		t.Error("expected job to be active")
	}

	ob.CloseJob(1)

	if ob.IsActive(1) {
		t.Error("expected job to be inactive after close")
	}

	lines := ob.GetLines(1)
	if lines != nil {
		t.Error("expected nil lines after close")
	}
}

func TestOutputBuffer_Subscribe(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)

	// Add initial line
	ob.Append(1, OutputLine{Text: "initial", Type: "text"})

	// Subscribe
	initial, ch, cancel := ob.Subscribe(1)
	defer cancel()

	if len(initial) != 1 {
		t.Fatalf("expected 1 initial line, got %d", len(initial))
	}
	if initial[0].Text != "initial" {
		t.Errorf("expected 'initial', got %q", initial[0].Text)
	}

	// Add more lines after subscription
	go func() {
		time.Sleep(10 * time.Millisecond)
		ob.Append(1, OutputLine{Text: "new", Type: "text"})
	}()

	select {
	case line := <-ch:
		if line.Text != "new" {
			t.Errorf("expected 'new', got %q", line.Text)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for subscribed line")
	}
}

func TestOutputBuffer_SubscribeCancel(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)

	_, ch, cancel := ob.Subscribe(1)
	cancel()

	// Channel should be closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed")
		}
	default:
		// Channel closed, as expected
	}
}

func TestOutputBuffer_CloseJobClosesSubscribers(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)

	ob.Append(1, OutputLine{Text: "test", Type: "text"})
	_, ch, _ := ob.Subscribe(1)

	ob.CloseJob(1)

	// Channel should be closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after CloseJob")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("channel not closed after CloseJob")
	}
}

func TestOutputWriter_Write(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)
	w := ob.Writer(1, simpleNormalizer)

	// Write with newline
	w.Write([]byte("hello\n"))
	w.Write([]byte("world\n"))

	assertLines(t, ob.GetLines(1), "hello", "world")
}

func TestOutputWriter_WritePartialLines(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)
	w := ob.Writer(1, simpleNormalizer)

	// Write partial line
	w.Write([]byte("hel"))
	w.Write([]byte("lo\nwor"))
	w.Write([]byte("ld\n"))

	assertLines(t, ob.GetLines(1), "hello", "world")
}

func TestOutputWriter_Flush(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)
	w := ob.Writer(1, simpleNormalizer)

	// Write without newline
	w.Write([]byte("incomplete"))

	// Should not appear yet
	assertLines(t, ob.GetLines(1))

	// Flush should process remaining
	w.Flush()

	assertLines(t, ob.GetLines(1), "incomplete")
}

func TestOutputWriter_NormalizeFilters(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)
	// Normalizer that filters out empty lines
	normalize := func(line string) *OutputLine {
		if line == "" {
			return nil
		}
		return &OutputLine{Text: line, Type: "text"}
	}

	w := ob.Writer(1, normalize)

	w.Write([]byte("keep\n\nskip empty\n"))

	lines := ob.GetLines(1)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (empty filtered), got %d", len(lines))
	}
}

func TestOutputWriter_LongLineWithoutNewline(t *testing.T) {
	// Per-job limit: 50 bytes
	ob := NewOutputBuffer(50, 1000)
	w := ob.Writer(1, simpleNormalizer)

	// Write a very long line without newline - should be force-flushed with truncation
	longLine := strings.Repeat("x", 100)
	w.Write([]byte(longLine))

	lines := ob.GetLines(1)
	// Should have at least one line from forced flush
	if len(lines) == 0 {
		t.Fatalf("expected at least 1 line after forced flush, got 0")
	}

	// First line should be truncated to maxLine-3 + "..." = 50 bytes total
	if len(lines[0].Text) != 50 {
		t.Errorf("expected truncated line to be 50 bytes, got %d bytes: %q", len(lines[0].Text), lines[0].Text)
	}
	if !strings.HasSuffix(lines[0].Text, "...") {
		t.Errorf("expected line to end with '...', got %q", lines[0].Text)
	}
}

func TestOutputWriter_SmallMaxLine(t *testing.T) {
	// Test that truncation works correctly with very small maxLine values
	// where there's no room for "..." suffix
	tests := []struct {
		name        string
		maxLine     int
		input       string
		expectLen   int
		expectNoEll bool // true if no ellipsis expected
	}{
		{"maxLine=3", 3, "abcdefgh", 3, true},           // No room for ellipsis
		{"maxLine=4", 4, "abcdefgh", 4, false},          // Just enough: 1 char + "..."
		{"maxLine=5", 5, "abcdefgh", 5, false},          // 2 chars + "..."
		{"maxLine=10", 10, "abcdefghijklmn", 10, false}, // 7 chars + "..."
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ob := NewOutputBuffer(tt.maxLine, 10000)
			w := ob.Writer(1, simpleNormalizer)
			w.Write([]byte(tt.input)) // No newline, triggers truncation

			lines := ob.GetLines(1)
			if len(lines) != 1 {
				t.Fatalf("expected 1 line, got %d", len(lines))
			}

			if len(lines[0].Text) != tt.expectLen {
				t.Errorf("expected line length %d, got %d: %q", tt.expectLen, len(lines[0].Text), lines[0].Text)
			}

			hasEllipsis := strings.HasSuffix(lines[0].Text, "...")
			if tt.expectNoEll && hasEllipsis {
				t.Errorf("expected no ellipsis for maxLine=%d, got %q", tt.maxLine, lines[0].Text)
			}
			if !tt.expectNoEll && !hasEllipsis {
				t.Errorf("expected ellipsis for maxLine=%d, got %q", tt.maxLine, lines[0].Text)
			}
		})
	}
}

func TestOutputWriter_MultiWriteLongLineDiscard(t *testing.T) {
	// Test that after truncating a long line, subsequent writes for the
	// same line are discarded until a newline is seen.
	// Key invariant: repeated writes without newlines produce at most ONE truncated line
	ob := NewOutputBuffer(100, 10000)
	w := ob.Writer(1, simpleNormalizer)

	// Write data exceeding maxLine (100 bytes) multiple times WITHOUT a newline
	// This simulates a single very long line being written in chunks
	for range 5 {
		w.Write([]byte(strings.Repeat("x", 50))) // 5 * 50 = 250 bytes total
	}

	// Should only have 1 line (the truncated one), not 5 fragments
	lines := ob.GetLines(1)
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 truncated line (not multiple fragments), got %d", len(lines))
	}

	// Verify it's truncated
	if !strings.HasSuffix(lines[0].Text, "...") {
		t.Errorf("expected truncated line to end with '...', got %q", lines[0].Text)
	}
}

func TestOutputBuffer_PerJobEvictionBlockedByGlobal(t *testing.T) {
	// Test the case where per-job eviction would be needed but global limit
	// would still be exceeded after eviction - no eviction should occur.
	//
	// Per-job: 30 bytes, Global: 40 bytes
	ob := NewOutputBuffer(30, 40)

	// Job 1: add 20 bytes
	ob.Append(1, OutputLine{Text: "12345678901234567890", Type: "text"}) // 20 bytes

	// Job 2: add 20 bytes
	ob.Append(2, OutputLine{Text: "12345678901234567890", Type: "text"}) // 20 bytes, total=40

	// Now try to add 25 bytes to job 1
	// Per-job: 20+25=45 > 30, would evict 20 bytes (the existing line)
	// After eviction: job1=0, total=40-20=20.
	// Adding new line: 20+25=45 > 40 (Global Limit) - REJECTED.
	// Result: Existing line should be preserved rather than evicted for no benefit.

	lines1Before := ob.GetLines(1)
	if len(lines1Before) != 1 {
		t.Fatalf("expected 1 line for job 1 before, got %d", len(lines1Before))
	}

	// Try to add a line that would exceed global limit even after eviction
	ob.Append(1, OutputLine{Text: "1234567890123456789012345", Type: "text"}) // 25 bytes

	lines1After := ob.GetLines(1)
	if len(lines1After) != 1 {
		t.Fatalf("expected 1 line for job 1 (preserved), got %d", len(lines1After))
	}
	// Original line should still be there
	if lines1After[0].Text != "12345678901234567890" {
		t.Errorf("original line should be preserved, got %q", lines1After[0].Text)
	}
}

func TestOutputBuffer_Concurrent(t *testing.T) {
	ob := NewOutputBuffer(10240, 40960)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(jobID int64) {
			defer wg.Done()
			for range 100 {
				ob.Append(jobID, OutputLine{Text: "test", Type: "text"})
			}
		}(int64(i))
	}

	wg.Wait()

	// All jobs should have lines
	for i := range 10 {
		lines := ob.GetLines(int64(i))
		if len(lines) == 0 {
			t.Errorf("job %d has no lines", i)
		}
	}
}
