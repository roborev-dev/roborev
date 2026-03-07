package daemon

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEvent_MarshalJSON(t *testing.T) {
	cases := []struct {
		name        string
		event       Event
		expected    map[string]any
		notExpected []string
	}{
		{
			name: "success event without error",
			event: Event{
				Type:     "review.completed",
				TS:       time.Date(2026, 1, 11, 10, 0, 30, 0, time.UTC),
				JobID:    42,
				Repo:     "/path/to/myrepo",
				RepoName: "myrepo",
				SHA:      "abc123",
				Agent:    "claude-code",
				Verdict:  "F",
			},
			expected: map[string]any{
				"type":      "review.completed",
				"ts":        "2026-01-11T10:00:30Z",
				"job_id":    float64(42),
				"repo":      "/path/to/myrepo",
				"repo_name": "myrepo",
				"sha":       "abc123",
				"agent":     "claude-code",
				"verdict":   "F",
			},
			notExpected: []string{"error"},
		},
		{
			name: "event with error",
			event: Event{
				Type:     "review.failed",
				TS:       time.Date(2026, 1, 11, 10, 5, 0, 0, time.UTC),
				JobID:    43,
				Repo:     "/path/to/myrepo",
				RepoName: "myrepo",
				SHA:      "abc124",
				Agent:    "claude-code",
				Error:    "something went wrong",
			},
			expected: map[string]any{
				"type":      "review.failed",
				"ts":        "2026-01-11T10:05:00Z",
				"job_id":    float64(43),
				"repo":      "/path/to/myrepo",
				"repo_name": "myrepo",
				"sha":       "abc124",
				"agent":     "claude-code",
				"error":     "something went wrong",
			},
			notExpected: []string{"verdict"},
		},
		{
			name: "event with findings",
			event: Event{
				Type:     "review.completed",
				TS:       time.Date(2026, 1, 11, 10, 10, 0, 0, time.UTC),
				JobID:    44,
				Repo:     "/path/to/myrepo",
				RepoName: "myrepo",
				SHA:      "abc125",
				Agent:    "claude-code",
				Findings: "found a bug",
			},
			expected: map[string]any{
				"type":      "review.completed",
				"ts":        "2026-01-11T10:10:00Z",
				"job_id":    float64(44),
				"repo":      "/path/to/myrepo",
				"repo_name": "myrepo",
				"sha":       "abc125",
				"agent":     "claude-code",
				"findings":  "found a bug",
			},
			notExpected: []string{"verdict", "error"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.event.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON failed: %v", err)
			}

			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Could not unmarshal generated JSON: %v", err)
			}

			for k, v := range tc.expected {
				if got, ok := decoded[k]; !ok {
					t.Errorf("expected key %s missing", k)
				} else if got != v {
					t.Errorf("expected %s=%v, got %v", k, v, got)
				}
			}

			for _, k := range tc.notExpected {
				if _, ok := decoded[k]; ok {
					t.Errorf("expected %s to be omitted", k)
				}
			}

			if len(decoded) != len(tc.expected) {
				t.Errorf("expected %d fields in JSON, got %d", len(tc.expected), len(decoded))
			}
		})
	}
}
