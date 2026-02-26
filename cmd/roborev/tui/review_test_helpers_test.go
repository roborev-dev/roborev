package tui

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

// --- testModelOption functions for review tests ---

func withReview(r *storage.Review) testModelOption {
	return func(m *model) { m.currentReview = r }
}

func withDimensions(w, h int) testModelOption {
	return func(m *model) { m.width = w; m.height = h }
}

func withBranchName(b string) testModelOption {
	return func(m *model) { m.currentBranch = b }
}

func withReviewFromView(v viewKind) testModelOption {
	return func(m *model) { m.reviewFromView = v }
}

func withFixPanel(open, focused bool) testModelOption {
	return func(m *model) {
		m.reviewFixPanelOpen = open
		m.reviewFixPanelFocused = focused
	}
}

func withFixPanelPending(pending bool) testModelOption {
	return func(m *model) { m.reviewFixPanelPending = pending }
}

func withFixPrompt(jobID int64, text string) testModelOption {
	return func(m *model) {
		m.fixPromptJobID = jobID
		m.fixPromptText = text
	}
}

func withClipboard(c ClipboardWriter) testModelOption {
	return func(m *model) { m.clipboard = c }
}

// --- Assertion helpers ---

func assertFixPanelState(
	t *testing.T, m model, open, focused bool,
) {
	t.Helper()
	if m.reviewFixPanelOpen != open {
		t.Errorf("reviewFixPanelOpen: got %v, want %v",
			m.reviewFixPanelOpen, open)
	}
	if m.reviewFixPanelFocused != focused {
		t.Errorf("reviewFixPanelFocused: got %v, want %v",
			m.reviewFixPanelFocused, focused)
	}
}

func assertFixPanelClosed(t *testing.T, m model) {
	t.Helper()
	assertFixPanelState(t, m, false, false)
	if m.fixPromptText != "" {
		t.Errorf("fixPromptText not cleared: %q", m.fixPromptText)
	}
	if m.fixPromptJobID != 0 {
		t.Errorf("fixPromptJobID not cleared: %d", m.fixPromptJobID)
	}
}

func assertFixPanelOpen(
	t *testing.T, m model, jobID int64,
) {
	t.Helper()
	assertFixPanelState(t, m, true, true)
	if m.fixPromptJobID != jobID {
		t.Errorf("fixPromptJobID: got %d, want %d",
			m.fixPromptJobID, jobID)
	}
}

// --- Mock review server handler ---

func mockReviewHandler(
	review storage.Review, responses []storage.Response,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/review":
			json.NewEncoder(w).Encode(review)
		case "/api/comments":
			json.NewEncoder(w).Encode(map[string]any{
				"responses": responses,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}
