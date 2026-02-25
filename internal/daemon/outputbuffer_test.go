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

// assertLines verifies the count and content of lines for a job.
func assertLines(t *testing.T, lines []OutputLine, expected ...OutputLine) {
	t.Helper()
	if len(lines) != len(expected) {
		t.Fatalf("expected %d lines, got %d", len(expected), len(lines))
	}
	for i, want := range expected {
		actual := lines[i]
		actual.Timestamp = time.Time{}
		if actual != want {
			t.Errorf("line[%d]: expected %+v, got %+v", i, want, actual)
		}
	}
}

func TestOutputBuffer_Append(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)

	ob.Append(1, OutputLine{Text: "line 1", Type: "text"})
	ob.Append(1, OutputLine{Text: "line 2", Type: "tool"})

	lines := ob.GetLines(1)
	assertLines(t, lines, OutputLine{Text: "line 1", Type: "text"}, OutputLine{Text: "line 2", Type: "tool"})
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
		expectLines map[int64][]OutputLine
	}{
		{
			name:        "PerJobLimit_Eviction",
			jobLimit:    50,
			globalLimit: 1000,
			actions: []action{
				{1, strings.Repeat("a", 20)}, // 20b
				{1, strings.Repeat("b", 20)}, // 20b
				{1, strings.Repeat("c", 20)}, // 20b - should evict first
			},
			expectLines: map[int64][]OutputLine{
				1: {
					{Text: strings.Repeat("b", 20), Type: "text"},
					{Text: strings.Repeat("c", 20), Type: "text"},
				},
			},
		},
		{
			name:        "GlobalLimit_Drop",
			jobLimit:    30,
			globalLimit: 50,
			actions: []action{
				{1, strings.Repeat("a", 20)}, // 20b
				{2, strings.Repeat("b", 20)}, // 20b
				{3, strings.Repeat("c", 20)}, // 20b - would exceed 50, dropped
			},
			expectLines: map[int64][]OutputLine{
				1: {{Text: strings.Repeat("a", 20), Type: "text"}},
				2: {{Text: strings.Repeat("b", 20), Type: "text"}},
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
			expectLines: map[int64][]OutputLine{
				1: {{Text: "short", Type: "text"}},
			},
		},
		{
			name:        "GlobalLimit_PreservesExisting_WhenEvictionBlocked",
			jobLimit:    50,
			globalLimit: 80,
			actions: []action{
				{1, strings.Repeat("a", 20)}, // job1: 20
				{1, strings.Repeat("b", 20)}, // job1: 40, total: 40
				{2, strings.Repeat("c", 30)}, // job2: 30, total: 70
				// Now try to add 20 bytes to job 1
				// Per-job: 40+20=60 > 50, evicts 20 bytes
				// After eviction: job1=40, total=70-20+20=70 <= 80
				// Succeeds
				{1, strings.Repeat("d", 20)},
				// Now try to add 20 more bytes to job 1
				// Per-job: 40+20=60 > 50, evicts 20 bytes
				// After eviction: total=70-20+20=70 <= 80
				// Succeeds
				{1, strings.Repeat("e", 20)},
				// Now try to add 15 bytes to job 2
				// Per-job: 30+15=45 < 50, no eviction
				// Global: 70+15=85 > 80, REJECTED
				{2, strings.Repeat("f", 15)},
			},
			expectLines: map[int64][]OutputLine{
				1: {
					{Text: strings.Repeat("d", 20), Type: "text"},
					{Text: strings.Repeat("e", 20), Type: "text"},
				},
				2: {
					{Text: strings.Repeat("c", 30), Type: "text"},
				},
			},
		},
		{
			name:        "PerJobEviction_BlockedByGlobal",
			jobLimit:    30,
			globalLimit: 40,
			actions: []action{
				{1, strings.Repeat("a", 20)}, // job1: 20
				{2, strings.Repeat("b", 20)}, // job2: 20, total: 40
				// Try to add 25 bytes to job 1
				// Per-job: 20+25=45 > 30, would evict 20 bytes
				// After eviction: job1=0, total=40-20=20
				// Adding new line: 20+25=45 > 40 (Global Limit) - REJECTED.
				// Existing line preserved
				{1, strings.Repeat("c", 25)},
			},
			expectLines: map[int64][]OutputLine{
				1: {{Text: strings.Repeat("a", 20), Type: "text"}},
				2: {{Text: strings.Repeat("b", 20), Type: "text"}},
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

	assertLines(t, ob.GetLines(1), OutputLine{Text: "hello", Type: "text"}, OutputLine{Text: "world", Type: "text"})
}

func TestOutputWriter_WritePartialLines(t *testing.T) {
	ob := NewOutputBuffer(1024, 4096)
	w := ob.Writer(1, simpleNormalizer)

	// Write partial line
	w.Write([]byte("hel"))
	w.Write([]byte("lo\nwor"))
	w.Write([]byte("ld\n"))

	assertLines(t, ob.GetLines(1), OutputLine{Text: "hello", Type: "text"}, OutputLine{Text: "world", Type: "text"})
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

	assertLines(t, ob.GetLines(1), OutputLine{Text: "incomplete", Type: "text"})
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
