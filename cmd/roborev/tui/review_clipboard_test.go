package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

// mockClipboard implements ClipboardWriter for testing
type mockClipboard struct {
	lastText string
	err      error
}

func (m *mockClipboard) WriteText(text string) error {
	if m.err != nil {
		return m.err
	}
	m.lastText = text
	return nil
}

func TestTUIYankCopyFromReviewView(t *testing.T) {
	mock := &mockClipboard{}

	m := newModel("http://localhost", withExternalIODisabled())
	m.clipboard = mock
	m.currentView = viewReview
	m.currentReview = makeReview(1, &storage.ReviewJob{ID: 1}, withReviewAgent("test"), withReviewOutput("This is the review content to copy"))

	// Press 'y' to yank/copy
	m, cmd := pressKey(m, 'y')

	// Should return a command to copy to clipboard
	if cmd == nil {
		t.Fatal("Expected a command to be returned")
	}

	// Execute the command to get the result
	msg := cmd()
	result, ok := msg.(clipboardResultMsg)
	if !ok {
		t.Fatalf("Expected clipboardResultMsg, got %T", msg)
	}

	if result.err != nil {
		t.Errorf("Expected no error, got %v", result.err)
	}

	expectedContent := "Review #1\n\nThis is the review content to copy"
	if mock.lastText != expectedContent {
		t.Errorf("Expected clipboard to contain review with header, got %q", mock.lastText)
	}
}

func TestTUIYankCopyShowsFlashMessage(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewReview
	m.currentReview = makeReview(1, &storage.ReviewJob{ID: 1}, withReviewAgent("test"), withReviewOutput("Review content"))
	m.width = 80
	m.height = 24

	// Simulate receiving a successful clipboard result (view captured at trigger time)
	m, _ = updateModel(t, m, clipboardResultMsg{err: nil, view: viewReview})

	if m.flashMessage != "Copied to clipboard" {
		t.Errorf("Expected flash message 'Copied to clipboard', got %q", m.flashMessage)
	}

	if m.flashExpiresAt.IsZero() {
		t.Error("Expected flashExpiresAt to be set")
	}

	if m.flashView != viewReview {
		t.Errorf("Expected flashView to be viewReview, got %v", m.flashView)
	}

	// Verify flash message appears in the rendered output
	output := m.renderReviewView()
	if !strings.Contains(output, "Copied to clipboard") {
		t.Error("Expected flash message to appear in rendered output")
	}
}

func TestTUIYankCopyShowsErrorOnFailure(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewQueue

	// Simulate receiving a failed clipboard result
	m, _ = updateModel(t, m, clipboardResultMsg{err: fmt.Errorf("clipboard not available"), view: viewQueue})

	if m.err == nil {
		t.Error("Expected error to be set")
	}

	if !strings.Contains(m.err.Error(), "copy failed") {
		t.Errorf("Expected error to contain 'copy failed', got %q", m.err.Error())
	}
}

func TestTUIYankFlashViewNotAffectedByViewChange(t *testing.T) {
	// Test that flash message is attributed to the view where copy was triggered,
	// even if the user switches views before the clipboard result arrives.
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewQueue
	m.width = 80
	m.height = 24
	m.currentReview = makeReview(1, &storage.ReviewJob{ID: 1}, withReviewAgent("test"), withReviewOutput("Review content"))

	// User switches to review view before clipboard result arrives
	m.currentView = viewReview

	// Clipboard result arrives with view captured at trigger time (queue)
	m, _ = updateModel(t, m, clipboardResultMsg{err: nil, view: viewQueue})

	// Flash should be attributed to queue view, not current (review) view
	if m.flashView != viewQueue {
		t.Errorf("Expected flashView to be viewQueue (trigger view), got %v", m.flashView)
	}

	// Flash should NOT appear in review view since it was triggered in queue
	output := m.renderReviewView()
	if strings.Contains(output, "Copied to clipboard") {
		t.Error("Flash message should not appear in review view when triggered from queue")
	}
}

func TestTUIYankFromQueueRequiresCompletedJob(t *testing.T) {
	m := newModel("http://localhost", withExternalIODisabled())
	m.currentView = viewQueue
	m.jobs = []storage.ReviewJob{
		makeJob(1, withRef("abc123"), withAgent("test"), withStatus(storage.JobStatusRunning)),
		makeJob(2, withRef("def456"), withAgent("test")),
	}
	m.selectedIdx = 0

	// Press 'y' on running job - should not copy
	_, cmd := pressKey(m, 'y')
	if cmd != nil {
		t.Error("Expected no command for running job")
	}

	// Select completed job
	m.selectedIdx = 1
	_, cmd = pressKey(m, 'y')
	if cmd == nil {
		t.Error("Expected command for completed job")
	}
}

func TestTUIFetchReviewAndCopySuccess(t *testing.T) {
	mock := &mockClipboard{}

	// Create test server that returns a review
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/review" {
			t.Errorf("Expected /api/review, got %s", r.URL.Path)
		}
		jobID := r.URL.Query().Get("job_id")
		if jobID != "123" {
			t.Errorf("Expected job_id=123, got %s", jobID)
		}
		review := storage.Review{
			ID:     1,
			JobID:  123,
			Agent:  "test",
			Output: "Review content for clipboard",
		}
		json.NewEncoder(w).Encode(review)
	})
	m.clipboard = mock

	// Execute fetchReviewAndCopy
	cmd := m.fetchReviewAndCopy(123, nil)
	msg := cmd()

	result, ok := msg.(clipboardResultMsg)
	if !ok {
		t.Fatalf("Expected clipboardResultMsg, got %T", msg)
	}

	if result.err != nil {
		t.Errorf("Expected no error, got %v", result.err)
	}

	// Clipboard should contain header with JobID + review content
	expectedContent := "Review #123\n\nReview content for clipboard"
	if mock.lastText != expectedContent {
		t.Errorf("Expected clipboard to contain review with header, got %q", mock.lastText)
	}
}

func TestTUIFetchReviewAndCopy404(t *testing.T) {
	// Create test server that returns 404
	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	cmd := m.fetchReviewAndCopy(123, nil)
	msg := cmd()

	result, ok := msg.(clipboardResultMsg)
	if !ok {
		t.Fatalf("Expected clipboardResultMsg, got %T", msg)
	}

	if result.err == nil {
		t.Error("Expected error for 404 response")
	}

	if !strings.Contains(result.err.Error(), "no review found") {
		t.Errorf("Expected 'no review found' error, got %q", result.err.Error())
	}
}

func TestTUIFetchReviewAndCopyEmptyOutput(t *testing.T) {
	_, m := mockServerModel(t, mockReviewHandler(
		storage.Review{ID: 1, JobID: 123, Agent: "test", Output: ""},
		nil,
	))

	cmd := m.fetchReviewAndCopy(123, nil)
	msg := cmd()

	result, ok := msg.(clipboardResultMsg)
	if !ok {
		t.Fatalf("Expected clipboardResultMsg, got %T", msg)
	}

	if result.err == nil {
		t.Error("Expected error for empty output")
	}

	if !strings.Contains(result.err.Error(), "review has no content") {
		t.Errorf("Expected 'review has no content' error, got %q", result.err.Error())
	}
}

func TestTUIClipboardWriteFailurePropagates(t *testing.T) {
	mock := &mockClipboard{err: fmt.Errorf("clipboard unavailable: xclip not found")}

	m := initTestModel(
		withCurrentView(viewReview),
		withClipboard(mock),
		withReview(makeReview(1, &storage.ReviewJob{ID: 1}, withReviewAgent("test"), withReviewOutput("Review content"))),
	)

	// Press 'y' to copy
	_, cmd := pressKey(m, 'y')
	if cmd == nil {
		t.Fatal("Expected command to be returned")
	}

	// Execute the command
	msg := cmd()
	result, ok := msg.(clipboardResultMsg)
	if !ok {
		t.Fatalf("Expected clipboardResultMsg, got %T", msg)
	}

	// Error should propagate
	if result.err == nil {
		t.Error("Expected clipboard write error to propagate")
	}

	if !strings.Contains(result.err.Error(), "clipboard unavailable") {
		t.Errorf("Expected clipboard error message, got %q", result.err.Error())
	}
}

func TestTUIFetchReviewAndCopyClipboardFailure(t *testing.T) {
	mock := &mockClipboard{err: fmt.Errorf("clipboard unavailable: pbcopy not found")}

	_, m := mockServerModel(t, mockReviewHandler(
		storage.Review{ID: 1, JobID: 123, Agent: "test", Output: "Review content"},
		nil,
	))
	m.clipboard = mock

	// Fetch succeeds but clipboard write fails
	cmd := m.fetchReviewAndCopy(123, nil)
	msg := cmd()

	result, ok := msg.(clipboardResultMsg)
	if !ok {
		t.Fatalf("Expected clipboardResultMsg, got %T", msg)
	}

	if result.err == nil {
		t.Error("Expected clipboard write error after successful fetch")
	}

	if !strings.Contains(result.err.Error(), "clipboard unavailable") {
		t.Errorf("Expected clipboard error message, got %q", result.err.Error())
	}
}

func TestTUIFetchReviewAndCopyJobInjection(t *testing.T) {
	mock := &mockClipboard{}

	// Server returns a review WITHOUT Job populated (intentionally nil)
	_, m := mockServerModel(t, mockReviewHandler(
		storage.Review{ID: 42, JobID: 123, Agent: "test", Output: "Review content"},
		nil,
	))
	m.clipboard = mock

	// Pass a job parameter - this should be injected when review.Job is nil
	j := makeJob(123, withRepoPath("/path/to/repo"), withRef("a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"))

	cmd := m.fetchReviewAndCopy(123, &j)
	msg := cmd()

	result, ok := msg.(clipboardResultMsg)
	if !ok {
		t.Fatalf("Expected clipboardResultMsg, got %T", msg)
	}

	if result.err != nil {
		t.Errorf("Expected no error, got %v", result.err)
	}

	// Clipboard should contain header with injected job info (job ID, truncated SHA)
	expectedContent := "Review #123 /path/to/repo a1b2c3d\n\nReview content"
	if mock.lastText != expectedContent {
		t.Errorf("Expected clipboard with injected job info, got %q", mock.lastText)
	}
}

func TestFormatClipboardContent(t *testing.T) {
	tests := []struct {
		name     string
		review   *storage.Review
		expected string
	}{
		{
			name:     "nil review",
			review:   nil,
			expected: "",
		},
		{
			name: "empty output",
			review: &storage.Review{
				ID:     1,
				Output: "",
			},
			expected: "",
		},
		{
			name: "review with JobID only (no job struct)",
			review: &storage.Review{
				ID:     99, // review.ID is different from JobID
				JobID:  42,
				Output: "Content here",
			},
			expected: "Review #42\n\nContent here",
		},
		{
			name: "review with JobID 0 but review ID set (legacy fallback)",
			review: &storage.Review{
				ID:     77,
				JobID:  0,
				Output: "Content here",
			},
			expected: "Review #77\n\nContent here",
		},
		{
			name: "review with all IDs 0 and no job struct (no header)",
			review: &storage.Review{
				ID:     0,
				JobID:  0,
				Output: "Content here",
			},
			expected: "Content here",
		},
		{
			name: "review with job - full SHA truncated",
			review: &storage.Review{
				ID:     99,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       99,
					RepoPath: "/Users/test/myrepo",
					GitRef:   "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", // exactly 40 hex chars
				},
			},
			expected: "Review #99 /Users/test/myrepo a1b2c3d\n\nReview content",
		},
		{
			name: "long branch name not truncated",
			review: &storage.Review{
				ID:     101,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       101,
					RepoPath: "/repo",
					GitRef:   "feature/very-long-branch-name-that-exceeds-forty-characters",
				},
			},
			expected: "Review #101 /repo feature/very-long-branch-name-that-exceeds-forty-characters\n\nReview content",
		},
		{
			name: "review with job - range not truncated",
			review: &storage.Review{
				ID:     100,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       100,
					RepoPath: "/path/to/repo",
					GitRef:   "abc1234..def5678",
				},
			},
			expected: "Review #100 /path/to/repo abc1234..def5678\n\nReview content",
		},
		{
			name: "always uses job ID from Job struct",
			review: &storage.Review{
				ID:     999, // review.ID is ignored when Job is present with valid ID
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       555,
					RepoPath: "/repo/path",
					GitRef:   "abcdef1234567890abcdef1234567890abcdef12",
				},
			},
			expected: "Review #555 /repo/path abcdef1\n\nReview content",
		},
		{
			name: "Job present but Job.ID is 0 falls back to JobID with context",
			review: &storage.Review{
				ID:     999,
				JobID:  123,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       0, // zero ID, should fall back to JobID
					RepoPath: "/repo/path",
					GitRef:   "abc1234",
				},
			},
			expected: "Review #123 /repo/path abc1234\n\nReview content",
		},
		{
			name: "Job present but Job.ID is 0 falls back to review.ID with context",
			review: &storage.Review{
				ID:     999,
				JobID:  0,
				Output: "Review content",
				Job: &storage.ReviewJob{
					ID:       0, // zero ID, should fall back to review.ID
					RepoPath: "/repo/path",
					GitRef:   "abc1234",
				},
			},
			expected: "Review #999 /repo/path abc1234\n\nReview content",
		},
		{
			name: "short git ref not truncated",
			review: &storage.Review{
				ID:     10,
				Output: "Content",
				Job: &storage.ReviewJob{
					ID:       10,
					RepoPath: "/repo",
					GitRef:   "abc1234",
				},
			},
			expected: "Review #10 /repo abc1234\n\nContent",
		},
		{
			name: "uppercase SHA truncated",
			review: &storage.Review{
				ID:     102,
				Output: "Content",
				Job: &storage.ReviewJob{
					ID:       102,
					RepoPath: "/repo",
					GitRef:   "ABCDEF1234567890ABCDEF1234567890ABCDEF12", // uppercase 40 hex chars
				},
			},
			expected: "Review #102 /repo ABCDEF1\n\nContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatClipboardContent(tt.review)
			if got != tt.expected {
				t.Errorf("formatClipboardContent() = %q, want %q", got, tt.expected)
			}
		})
	}
}
