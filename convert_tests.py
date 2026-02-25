import re

with open('cmd/roborev/refine_test.go', 'r') as f:
    content = f.read()

start_str = "func TestFindFailedReviewForBranch(t *testing.T) {"
start_idx = content.find(start_str)

next_func_str = "func chdirForTest(t *testing.T, dir string) {"
end_idx = content.find(next_func_str)

replacement = """func TestFindFailedReviewForBranch(t *testing.T) {
	tests := []struct {
		name             string
		setup            func(*mockDaemonClient)
		commits          []string
		skip             map[int64]bool
		wantJobID        int64 // 0 if nil expected
		wantErr          string
		wantAddressedIDs []int64
	}{
		{
			name: "oldest first",
			setup: func(c *mockDaemonClient) {
				c.WithReview("oldest123", 100, "No issues found.", false).
					WithReview("middle456", 200, "Found a bug in the code.", false).
					WithReview("newest789", 300, "Security vulnerability detected.", false)
			},
			commits:          []string{"oldest123", "middle456", "newest789"},
			wantJobID:        200,
			wantAddressedIDs: []int64{100},
		},
		{
			name: "skips addressed",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "Bug found.", false).
					WithReview("commit2", 200, "Another bug.", true).
					WithReview("commit3", 300, "More issues.", false)
			},
			commits:   []string{"commit1", "commit2", "commit3"},
			wantJobID: 100,
		},
		{
			name: "skips given up reviews",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "Bug found.", false).
					WithReview("commit2", 200, "Another bug.", false).
					WithReview("commit3", 300, "No issues found.", false)
			},
			commits:   []string{"commit1", "commit2", "commit3"},
			skip:      map[int64]bool{1: true},
			wantJobID: 200,
		},
		{
			name: "all skipped returns nil",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "Bug found.", false).
					WithReview("commit2", 200, "Another.", false)
			},
			commits:   []string{"commit1", "commit2"},
			skip:      map[int64]bool{1: true, 2: true},
			wantJobID: 0,
		},
		{
			name: "all pass",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false).
					WithReview("commit2", 200, "No findings.", false)
			},
			commits:          []string{"commit1", "commit2"},
			wantJobID:        0,
			wantAddressedIDs: []int64{100, 200},
		},
		{
			name:      "no reviews",
			setup:     func(c *mockDaemonClient) {},
			commits:   []string{"unreviewed1", "unreviewed2"},
			wantJobID: 0,
		},
		{
			name: "marks passing as addressed",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false).
					WithReview("commit2", 200, "No findings.", false)
			},
			commits:          []string{"commit1", "commit2"},
			wantJobID:        0,
			wantAddressedIDs: []int64{100, 200},
		},
		{
			name: "marks passing before failure",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false).
					WithReview("commit2", 200, "Bug found.", false)
			},
			commits:          []string{"commit1", "commit2"},
			wantJobID:        200,
			wantAddressedIDs: []int64{100},
		},
		{
			name: "does not mark already addressed",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", true).
					WithReview("commit2", 200, "Bug found.", false)
			},
			commits:   []string{"commit1", "commit2"},
			wantJobID: 200,
		},
		{
			name: "mixed scenario",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false).
					WithReview("commit2", 200, "No issues.", true).
					WithReview("commit3", 300, "Bug found.", true).
					WithReview("commit4", 400, "No findings detected.", false).
					WithReview("commit5", 500, "Critical error.", false)
			},
			commits:          []string{"commit1", "commit2", "commit3", "commit4", "commit5"},
			wantJobID:        500,
			wantAddressedIDs: []int64{100, 400},
		},
		{
			name: "stops at first failure",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "Bug found.", false).
					WithReview("commit2", 200, "No issues found.", false).
					WithReview("commit3", 300, "Another bug.", false)
			},
			commits:   []string{"commit1", "commit2", "commit3"},
			wantJobID: 100,
		},
		{
			name: "mark addressed error",
			setup: func(c *mockDaemonClient) {
				c.WithReview("commit1", 100, "No issues found.", false)
				c.markAddressedErr = fmt.Errorf("daemon connection failed")
			},
			commits: []string{"commit1"},
			wantErr: "marking review (job 100) as addressed",
		},
		{
			name: "get review by sha error",
			setup: func(c *mockDaemonClient) {
				c.getReviewBySHAErr = fmt.Errorf("daemon connection failed")
			},
			commits: []string{"commit1", "commit2"},
			wantErr: "fetching review",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMockDaemonClient()
			tt.setup(client)

			found, err := findFailedReviewForBranch(client, tt.commits, tt.skip)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got: %v", tt.wantErr, err)
				}
				if found != nil {
					t.Errorf("expected nil review when error occurs, got job %d", found.JobID)
				}
				return
			}

			if err != nil {
				t.Fatalf("findFailedReviewForBranch failed: %v", err)
			}

			if tt.wantJobID == 0 {
				if found != nil {
					t.Errorf("expected no failed reviews, got job %d", found.JobID)
				}
			} else {
				if found == nil {
					t.Fatalf("expected to find a failed review (job %d)", tt.wantJobID)
				}
				if found.JobID != tt.wantJobID {
					t.Errorf("expected job %d, got job %d", tt.wantJobID, found.JobID)
				}
			}

			if len(tt.wantAddressedIDs) > 0 {
				if len(client.addressedJobIDs) != len(tt.wantAddressedIDs) {
					t.Errorf("expected %d reviews to be marked addressed, got %d", len(tt.wantAddressedIDs), len(client.addressedJobIDs))
				}
				addressed := make(map[int64]bool)
				for _, id := range client.addressedJobIDs {
					addressed[id] = true
				}
				for _, id := range tt.wantAddressedIDs {
					if !addressed[id] {
						t.Errorf("expected job %d to be marked addressed, got %v", id, client.addressedJobIDs)
					}
				}
			} else if len(client.addressedJobIDs) > 0 {
				t.Errorf("expected no reviews to be marked addressed, got %v", client.addressedJobIDs)
			}
		})
	}
}

func TestFindPendingJobForBranch(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*mockDaemonClient)
		commits   []string
		wantJobID int64 // 0 if nil expected
	}{
		{
			name: "finds running job",
			setup: func(c *mockDaemonClient) {
				c.WithJob(100, "commit1", storage.JobStatusDone).
					WithJob(200, "commit2", storage.JobStatusRunning)
			},
			commits:   []string{"commit1", "commit2"},
			wantJobID: 200,
		},
		{
			name: "finds queued job",
			setup: func(c *mockDaemonClient) {
				c.WithJob(100, "commit1", storage.JobStatusQueued)
			},
			commits:   []string{"commit1"},
			wantJobID: 100,
		},
		{
			name: "no pending jobs",
			setup: func(c *mockDaemonClient) {
				c.WithJob(100, "commit1", storage.JobStatusDone).
					WithJob(200, "commit2", storage.JobStatusDone)
			},
			commits:   []string{"commit1", "commit2"},
			wantJobID: 0,
		},
		{
			name:      "no jobs for commits",
			setup:     func(c *mockDaemonClient) {},
			commits:   []string{"unreviewed1", "unreviewed2"},
			wantJobID: 0,
		},
		{
			name: "oldest first",
			setup: func(c *mockDaemonClient) {
				c.WithJob(100, "commit1", storage.JobStatusRunning).
					WithJob(200, "commit2", storage.JobStatusRunning)
			},
			commits:   []string{"commit1", "commit2"},
			wantJobID: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMockDaemonClient()
			tt.setup(client)

			pending, err := findPendingJobForBranch(client, "/repo", tt.commits)
			if err != nil {
				t.Fatalf("findPendingJobForBranch failed: %v", err)
			}

			if tt.wantJobID == 0 {
				if pending != nil {
					t.Errorf("expected no pending jobs, got job %d", pending.ID)
				}
			} else {
				if pending == nil {
					t.Fatalf("expected to find a pending job (job %d)", tt.wantJobID)
				}
				if pending.ID != tt.wantJobID {
					t.Errorf("expected job %d, got job %d", tt.wantJobID, pending.ID)
				}
			}
		})
	}
}

"""

new_content = content[:start_idx] + replacement + content[end_idx:]

with open('cmd/roborev/refine_test.go', 'w') as f:
    f.write(new_content)
