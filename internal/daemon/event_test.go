package daemon

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEvent_MarshalJSON(t *testing.T) {
	event := Event{
		Type:     "review.completed",
		TS:       time.Date(2026, 1, 11, 10, 0, 30, 0, time.UTC),
		JobID:    42,
		Repo:     "/path/to/myrepo",
		RepoName: "myrepo",
		SHA:      "abc123",
		Agent:    "claude-code",
		Verdict:  "F",
	}

	data, err := event.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Could not unmarshal generated JSON: %v", err)
	}

	tests := []struct {
		key      string
		expected any
	}{
		{"type", "review.completed"},
		{"ts", "2026-01-11T10:00:30Z"},
		{"job_id", float64(42)}, // JSON numbers are floats in map[string]interface{}
		{"repo", "/path/to/myrepo"},
		{"repo_name", "myrepo"},
		{"sha", "abc123"},
		{"agent", "claude-code"},
		{"verdict", "F"},
	}

	if len(decoded) != len(tests) {
		t.Errorf("expected %d fields in JSON, got %d", len(tests), len(decoded))
	}

	for _, tc := range tests {
		if got, ok := decoded[tc.key]; !ok {
			t.Errorf("missing expected key: %s", tc.key)
		} else if got != tc.expected {
			t.Errorf("expected %s to be %v, got %v", tc.key, tc.expected, got)
		}
	}

	// Explicitly check that 'error' is not present
	if _, ok := decoded["error"]; ok {
		t.Error("expected 'error' field to be omitted")
	}
}
