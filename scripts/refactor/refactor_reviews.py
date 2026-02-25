import re

with open("internal/storage/reviews_test.go", "r") as f:
    content = f.read()

# 1. Update TestGetReviewByJobIDIncludesModel
content = re.sub(
    r'job, err := db\.EnqueueJob\(EnqueueOpts\{RepoID: repo\.ID, GitRef: tt\.gitRef, Agent: "codex", Model: tt\.model, Reasoning: "thorough"\}\)\s+if err != nil \{\s+t\.Fatalf\("EnqueueRangeJob failed: %v", err\)\s+\}\s+// Claim job to move to running, then complete it\s+db\.ClaimJob\("test-worker"\)\s+err = db\.CompleteJob\(job\.ID, "codex", "test prompt", "Test review output\\n\\n## Verdict: PASS"\)\s+if err != nil \{\s+t\.Fatalf\("CompleteJob failed: %v", err\)\s+\}',
    r'''job := createCompletedJobWithOptions(t, db, EnqueueOpts{
				RepoID: repo.ID, 
				GitRef: tt.gitRef, 
				Agent: "codex", 
				Model: tt.model, 
				Reasoning: "thorough",
			}, "Test review output\n\n## Verdict: PASS")''',
    content
)

# 2. Update TestGetJobsWithReviewsByIDs
content = re.sub(
    r'job1 := createCompletedJob\(t, db, repo\.ID, "abc123", "output1"\)',
    r'job1 := createCompletedJobWithOptions(t, db, EnqueueOpts{RepoID: repo.ID, GitRef: "abc123"}, "output1")',
    content
)
content = re.sub(
    r'job3 := createCompletedJob\(t, db, repo\.ID, "ghi789", "output3"\)',
    r'job3 := createCompletedJobWithOptions(t, db, EnqueueOpts{RepoID: repo.ID, GitRef: "ghi789"}, "output3")',
    content
)

# 3. Update TestGetJobsWithReviewsByIDsPopulatesVerdict
# Replace createCompletedJob with createCompletedJobWithOptions
content = re.sub(
    r'passJob := createCompletedJob\(t, db, repo\.ID, "pass111", "No issues found\.\\n\\n## Verdict: PASS"\)',
    r'passJob := createCompletedJobWithOptions(t, db, EnqueueOpts{RepoID: repo.ID, GitRef: "pass111"}, "No issues found.\n\n## Verdict: PASS")',
    content
)
content = re.sub(
    r'failJob := createCompletedJob\(t, db, repo\.ID, "fail222", "- High — Critical bug found"\)',
    r'failJob := createCompletedJobWithOptions(t, db, EnqueueOpts{RepoID: repo.ID, GitRef: "fail222"}, "- High — Critical bug found")',
    content
)

# Refactor the sequential assertions into table driven
old_assertions = r'''	// Check PASS verdict
	passResult, ok := results\[passJob\.ID\]
	if !ok \{
		t\.Fatalf\("Expected result for pass job ID %d", passJob\.ID\)
	\}
	if passResult\.Job\.Verdict == nil \{
		t\.Fatal\("Expected Verdict to be populated for pass job"\)
	\}
	if \*passResult\.Job\.Verdict != "P" \{
		t\.Errorf\("Expected verdict P, got %q", \*passResult\.Job\.Verdict\)
	\}

	// Check FAIL verdict
	failResult, ok := results\[failJob\.ID\]
	if !ok \{
		t\.Fatalf\("Expected result for fail job ID %d", failJob\.ID\)
	\}
	if failResult\.Job\.Verdict == nil \{
		t\.Fatal\("Expected Verdict to be populated for fail job"\)
	\}
	if \*failResult\.Job\.Verdict != "F" \{
		t\.Errorf\("Expected verdict F, got %q", \*failResult\.Job\.Verdict\)
	\}

	// Also verify VerdictBool on the review
	if passResult\.Review == nil \{
		t\.Fatal\("Expected review for pass job"\)
	\}
	if passResult\.Review\.VerdictBool == nil \{
		t\.Error\("Expected VerdictBool for pass job, got nil"\)
	\} else if \*passResult\.Review\.VerdictBool != 1 \{
		t\.Errorf\("Expected VerdictBool=1 for pass job, got %d", \*passResult\.Review\.VerdictBool\)
	\}
	if failResult\.Review == nil \{
		t\.Fatal\("Expected review for fail job"\)
	\}
	if failResult\.Review\.VerdictBool == nil \{
		t\.Error\("Expected VerdictBool for fail job, got nil"\)
	\} else if \*failResult\.Review\.VerdictBool != 0 \{
		t\.Errorf\("Expected VerdictBool=0 for fail job, got %d", \*failResult\.Review\.VerdictBool\)
	\}'''

new_assertions = '''	cases := []struct {
		name        string
		jobID       int64
		wantVerdict string
		wantBool    int
	}{
		{"pass", passJob.ID, "P", 1},
		{"fail", failJob.ID, "F", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, ok := results[tc.jobID]
			if !ok {
				t.Fatalf("Expected result for job ID %d", tc.jobID)
			}
			if res.Job.Verdict == nil {
				t.Fatal("Expected Verdict to be populated")
			}
			if *res.Job.Verdict != tc.wantVerdict {
				t.Errorf("Expected verdict %s, got %q", tc.wantVerdict, *res.Job.Verdict)
			}
			if res.Review == nil {
				t.Fatal("Expected review")
			}
			if res.Review.VerdictBool == nil {
				t.Fatal("Expected VerdictBool, got nil")
			} else if *res.Review.VerdictBool != tc.wantBool {
				t.Errorf("Expected VerdictBool=%d, got %d", tc.wantBool, *res.Review.VerdictBool)
			}
		})
	}'''
content = re.sub(old_assertions, new_assertions, content, flags=re.MULTILINE)

# 4. Remove createCompletedJob and put createCompletedJobWithOptions at the bottom
createCompletedJob_pattern = r'''// createCompletedJob helper creates a job, claims it, and completes it\.
func createCompletedJob\(t \*testing\.T, db \*DB, repoID int64, gitRef, output string\) \*ReviewJob \{
	t\.Helper\(\)
	job := enqueueJob\(t, db, repoID, 0, gitRef\)
	claimed, err := db\.ClaimJob\("test-worker"\)
	if err != nil \{
		t\.Fatalf\("ClaimJob failed: %v", err\)
	\}
	if claimed == nil \{
		t\.Fatal\("ClaimJob returned nil; expected a queued job"\)
	\}
	if claimed\.ID != job\.ID \{
		t\.Fatalf\("Claimed job ID %d, expected %d", claimed\.ID, job\.ID\)
	\}

	if err := db\.CompleteJob\(job\.ID, "test-agent", "prompt", output\); err != nil \{
		t\.Fatalf\("CompleteJob failed: %v", err\)
	\}

	// Refresh job to get updated status/fields
	updatedJob, err := db\.GetJobByID\(job\.ID\)
	if err != nil \{
		t\.Fatalf\("GetJobByID failed: %v", err\)
	\}
	if updatedJob\.Status != JobStatusDone \{
		t\.Fatalf\("Expected job status %s, got %s", JobStatusDone, updatedJob\.Status\)
	\}
	return updatedJob
\}
'''
content = re.sub(createCompletedJob_pattern, '', content)

new_helper = '''
// createCompletedJobWithOptions helper creates a job, claims it, and completes it.
func createCompletedJobWithOptions(t *testing.T, db *DB, opts EnqueueOpts, output string) *ReviewJob {
	t.Helper()
	job, err := db.EnqueueJob(opts)
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	claimed, err := db.ClaimJob("test-worker")
	if err != nil {
		t.Fatalf("ClaimJob failed: %v", err)
	}
	if claimed == nil {
		t.Fatal("ClaimJob returned nil; expected a queued job")
	}
	if claimed.ID != job.ID {
		t.Fatalf("Claimed job ID %d, expected %d", claimed.ID, job.ID)
	}

	agent := opts.Agent
	if agent == "" {
		agent = "test-agent"
	}

	if err := db.CompleteJob(job.ID, agent, "prompt", output); err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}

	// Refresh job to get updated status/fields
	updatedJob, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if updatedJob.Status != JobStatusDone {
		t.Fatalf("Expected job status %s, got %s", JobStatusDone, updatedJob.Status)
	}
	return updatedJob
}
'''
content += new_helper


# 5. Update TestGetReviewByJobIDUsesStoredVerdict
content = re.sub(
    r'job, err := db\.EnqueueJob\(EnqueueOpts\{RepoID: repo\.ID, CommitID: commit\.ID, GitRef: "vread123", Agent: "codex"\}\)\s+if err != nil \{\s+t\.Fatalf\("EnqueueJob: %v", err\)\s+\}\s+claimJob\(t, db, "w1"\)\s+if err := db\.CompleteJob\(job\.ID, "codex", "prompt", "No issues found\."\); err != nil \{\s+t\.Fatalf\("CompleteJob: %v", err\)\s+\}',
    r'''job := createCompletedJobWithOptions(t, db, EnqueueOpts{
			RepoID: repo.ID, 
			CommitID: commit.ID, 
			GitRef: "vread123", 
			Agent: "codex",
		}, "No issues found.")''',
    content
)

content = re.sub(
    r'job, err := db\.EnqueueJob\(EnqueueOpts\{RepoID: repo\.ID, CommitID: commit2\.ID, GitRef: "vread456", Agent: "codex"\}\)\s+if err != nil \{\s+t\.Fatalf\("EnqueueJob: %v", err\)\s+\}\s+claimJob\(t, db, "w2"\)\s+if err := db\.CompleteJob\(job\.ID, "codex", "prompt", "No issues found\."\); err != nil \{\s+t\.Fatalf\("CompleteJob: %v", err\)\s+\}',
    r'''job := createCompletedJobWithOptions(t, db, EnqueueOpts{
			RepoID: repo.ID, 
			CommitID: commit2.ID, 
			GitRef: "vread456", 
			Agent: "codex",
		}, "No issues found.")''',
    content
)

# 6. Update TestGetReviewByCommitSHAUsesStoredVerdict
content = re.sub(
    r'job, err := db\.EnqueueJob\(EnqueueOpts\{RepoID: repo\.ID, CommitID: commit\.ID, GitRef: "shav123", Agent: "codex"\}\)\s+if err != nil \{\s+t\.Fatalf\("EnqueueJob: %v", err\)\s+\}\s+claimJob\(t, db, "w1"\)\s+if err := db\.CompleteJob\(job\.ID, "codex", "prompt", "- High — Bug found"\); err != nil \{\s+t\.Fatalf\("CompleteJob: %v", err\)\s+\}',
    r'''job := createCompletedJobWithOptions(t, db, EnqueueOpts{
		RepoID: repo.ID, 
		CommitID: commit.ID, 
		GitRef: "shav123", 
		Agent: "codex",
	}, "- High — Bug found")''',
    content
)

# Fix verifyComment location, moving it to the end
verifyComment_pattern = r'''// verifyComment helper checks if a comment matches expected values\.
func verifyComment\(t \*testing\.T, actual Response, expectedUser, expectedMsg string\) \{
	t\.Helper\(\)
	if actual\.Responder != expectedUser \{
		t\.Errorf\("Expected responder %q, got %q", expectedUser, actual\.Responder\)
	\}
	if actual\.Response != expectedMsg \{
		t\.Errorf\("Expected response %q, got %q", expectedMsg, actual\.Response\)
	\}
\}
'''

if verifyComment_pattern in content:
    # it's a regex match though
    content = re.sub(verifyComment_pattern, '', content)
    content += "\n// verifyComment helper checks if a comment matches expected values.\nfunc verifyComment(t *testing.T, actual Response, expectedUser, expectedMsg string) {\n\tt.Helper()\n\tif actual.Responder != expectedUser {\n\t\tt.Errorf(\"Expected responder %q, got %q\", expectedUser, actual.Responder)\n\t}\n\tif actual.Response != expectedMsg {\n\t\tt.Errorf(\"Expected response %q, got %q\", expectedMsg, actual.Response)\n\t}\n}\n"
else:
    # Try regex match
    match = re.search(verifyComment_pattern, content)
    if match:
        content = re.sub(verifyComment_pattern, '', content)
        content += "\n" + match.group(0)

with open("internal/storage/reviews_test.go", "w") as f:
    f.write(content)
