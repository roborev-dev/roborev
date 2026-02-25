import re
import sys

def modify_file(file_path):
    with open(file_path, 'r') as f:
        content = f.read()

    # 1. Add waitForLocalJobs to integrationEnv (right after newIntegrationEnv or openDB)
    wait_for_local_jobs = """
func (e *integrationEnv) waitForLocalJobs(db *DB, expected int, timeout time.Duration) {
	e.T.Helper()
	waitCondition(e.T, timeout, fmt.Sprintf("local job count %d", expected), func() (bool, error) {
		jobs, err := db.ListJobs("", "", 1000, 0)
		if err != nil {
			return false, err
		}
		return len(jobs) >= expected, nil
	})
}
"""
    if 'waitForLocalJobs' not in content:
        content = content.replace('// openDB creates a new SQLite database', wait_for_local_jobs.strip() + '\n\n// openDB creates a new SQLite database')

    # 2. Add setupNode to integrationEnv
    setup_node = """
type testNode struct {
	DB     *DB
	Repo   *Repo
	Worker *SyncWorker
}

func (e *integrationEnv) setupNode(name, repoIdentity, syncInterval string) testNode {
	e.T.Helper()
	db := e.openDB(name + ".db")
	repo, err := db.GetOrCreateRepo(filepath.Join(e.TmpDir, "repo_"+name), repoIdentity)
	if err != nil {
		e.T.Fatalf("%s: GetOrCreateRepo failed: %v", name, err)
	}
	worker := startSyncWorker(e.T, db, e.pgURL, name, syncInterval)
	return testNode{DB: db, Repo: repo, Worker: worker}
}
"""
    if 'setupNode' not in content:
        content = content.replace('// validPgTables is the allowlist', setup_node.strip() + '\n\n// validPgTables is the allowlist')

    # 3. Add tryCreateCompletedReviewWithoutCommit
    try_create_no_commit = """
// tryCreateCompletedReviewWithoutCommit creates a job and completes it without an underlying commit.
func tryCreateCompletedReviewWithoutCommit(db *DB, repoID int64) (*ReviewJob, error) {
	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repoID, CommitID: 0, GitRef: "HEAD", Agent: "test"})
	if err != nil {
		return nil, fmt.Errorf("EnqueueJob failed: %w", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID); err != nil {
		return nil, fmt.Errorf("failed to set job running: %w", err)
	}
	if err := db.CompleteJob(job.ID, "test", "prompt", "output"); err != nil {
		return nil, fmt.Errorf("CompleteJob failed: %w", err)
	}
	return job, nil
}
"""
    if 'tryCreateCompletedReviewWithoutCommit' not in content:
        content = content.replace('// createCompletedReview creates a repo, commit, enqueues', try_create_no_commit.strip() + '\n\n// createCompletedReview creates a repo, commit, enqueues')

    # 4. Refactor TestIntegration_FinalPush
    final_push_old = """	// Create 150 jobs without commits to match previous test behavior
	// (replacing createBatchReviews which forces commits)
	for i := 0; i < 150; i++ {
		job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: 0, GitRef: "HEAD", Agent: "test"})
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		// Update status and complete manually
		if _, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID); err != nil {
			t.Fatalf("failed to set job running: %v", err)
		}
		if err := db.CompleteJob(job.ID, "test", "prompt", "output"); err != nil {
			t.Fatalf("CompleteJob failed: %v", err)
		}
	}"""
    final_push_new = """	// Create 150 jobs without commits to match previous test behavior
	// (replacing createBatchReviews which forces commits)
	for i := 0; i < 150; i++ {
		if _, err := tryCreateCompletedReviewWithoutCommit(db, repo.ID); err != nil {
			t.Fatalf("tryCreateCompletedReviewWithoutCommit failed: %v", err)
		}
	}"""
    content = content.replace(final_push_old, final_push_new)

    final_push_no_commit_old = """	// Create a job without a commit (CommitID=0)
	job, err := db.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: 0, GitRef: "HEAD", Agent: "test"})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status = 'running', started_at = datetime('now') WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("failed to set job running: %v", err)
	}
	if err := db.CompleteJob(job.ID, "test", "prompt", "output"); err != nil {
		t.Fatalf("CompleteJob failed: %v", err)
	}"""
    final_push_no_commit_new = """	// Create a job without a commit (CommitID=0)
	_, err = tryCreateCompletedReviewWithoutCommit(db, repo.ID)
	if err != nil {
		t.Fatalf("tryCreateCompletedReviewWithoutCommit failed: %v", err)
	}"""
    content = content.replace(final_push_no_commit_old, final_push_no_commit_new)

    # 5. Add runConcurrentReviewsAndSync
    concurrent_sync = """
func runConcurrentReviewsAndSync(db *DB, repoID int64, worker *SyncWorker, prefix, author string, count int, results chan<- string, errs chan<- error, done chan<- bool) {
	go func() {
		defer func() { done <- true }()
		for i := 0; i < count; i++ {
			job, _, err := tryCreateCompletedReview(db, repoID, fmt.Sprintf("%s_%02d", prefix, i), author, fmt.Sprintf("%s concurrent %d", author, i), "prompt", fmt.Sprintf("Review %s-%d", prefix, i))
			if err != nil {
				errs <- err
				continue
			}
			results <- job.UUID
			if i%3 == 0 {
				if _, err := worker.SyncNow(); err != nil {
					errs <- fmt.Errorf("%s sync at job %d: %w", author, i, err)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}
"""
    if 'runConcurrentReviewsAndSync' not in content:
        content = content.replace('func TestIntegration_MultiplayerRealistic(t *testing.T) {', concurrent_sync.strip() + '\n\nfunc TestIntegration_MultiplayerRealistic(t *testing.T) {')

    # 6. Refactor Multiplayer checks
    # Remove checkJobs definition
    check_jobs_def = """	checkJobs := func(db *DB, count int) func() (bool, error) {
		return func() (bool, error) {
			jobs, err := db.ListJobs("", "", 1000, 0)
			if err != nil {
				return false, err
			}
			return len(jobs) >= count, nil
		}
	}

"""
    content = content.replace(check_jobs_def, "")
    
    # Wait conditions
    content = re.sub(r'waitCondition\(t, ([^,]+), "[^"]+", checkJobs\(([^,]+), ([^)]+)\)\)', r'env.waitForLocalJobs(\2, \3, \1)', content)

    # 7. Refactor setupNode in Multiplayer tests
    multiplayer_old = """	dbA := env.openDB("machine_a.db")
	dbB := env.openDB("machine_b.db")

	sharedRepoIdentity := "git@github.com:test/multiplayer-repo.git"

	repoA, err := dbA.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_a"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	jobA, reviewA := createCompletedReview(t, dbA, repoA.ID, "aaaa1111", "Alice", "Feature A", "prompt A", "Review from Machine A")

	repoB, err := dbB.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_b"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateRepo failed: %v", err)
	}
	jobB, reviewB := createCompletedReview(t, dbB, repoB.ID, "bbbb2222", "Bob", "Feature B", "prompt B", "Review from Machine B")

	workerA := startSyncWorker(t, dbA, env.pgURL, "machine-a", "100ms")
	workerB := startSyncWorker(t, dbB, env.pgURL, "machine-b", "100ms")"""
    
    multiplayer_new = """	sharedRepoIdentity := "git@github.com:test/multiplayer-repo.git"

	nodeA := env.setupNode("machine-a", sharedRepoIdentity, "100ms")
	nodeB := env.setupNode("machine-b", sharedRepoIdentity, "100ms")

	dbA, dbB := nodeA.DB, nodeB.DB
	workerA, workerB := nodeA.Worker, nodeB.Worker

	jobA, reviewA := createCompletedReview(t, dbA, nodeA.Repo.ID, "aaaa1111", "Alice", "Feature A", "prompt A", "Review from Machine A")
	jobB, reviewB := createCompletedReview(t, dbB, nodeB.Repo.ID, "bbbb2222", "Bob", "Feature B", "prompt B", "Review from Machine B")"""
    content = content.replace(multiplayer_old, multiplayer_new)

    samecommit_old = """	dbA := env.openDB("machine_a.db")
	dbB := env.openDB("machine_b.db")

	sharedRepoIdentity := "git@github.com:test/same-commit-repo.git"
	sharedCommitSHA := "cccc3333"

	repoA, err := dbA.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_a"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	jobA, reviewA := createCompletedReview(t, dbA, repoA.ID, sharedCommitSHA, "Charlie", "Shared commit", "prompt", "Machine A's review of shared commit")

	repoB, err := dbB.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_b"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateRepo failed: %v", err)
	}
	jobB, reviewB := createCompletedReview(t, dbB, repoB.ID, sharedCommitSHA, "Charlie", "Shared commit", "prompt", "Machine B's review of shared commit")

	if jobA.UUID == jobB.UUID {
		t.Fatal("Jobs from different machines should have different UUIDs")
	}

	workerA := startSyncWorker(t, dbA, env.pgURL, "machine-a", "100ms")
	workerB := startSyncWorker(t, dbB, env.pgURL, "machine-b", "100ms")"""

    samecommit_new = """	sharedRepoIdentity := "git@github.com:test/same-commit-repo.git"
	sharedCommitSHA := "cccc3333"

	nodeA := env.setupNode("machine-a", sharedRepoIdentity, "100ms")
	nodeB := env.setupNode("machine-b", sharedRepoIdentity, "100ms")
	dbA, dbB := nodeA.DB, nodeB.DB
	workerA, workerB := nodeA.Worker, nodeB.Worker

	jobA, reviewA := createCompletedReview(t, dbA, nodeA.Repo.ID, sharedCommitSHA, "Charlie", "Shared commit", "prompt", "Machine A's review of shared commit")
	jobB, reviewB := createCompletedReview(t, dbB, nodeB.Repo.ID, sharedCommitSHA, "Charlie", "Shared commit", "prompt", "Machine B's review of shared commit")

	if jobA.UUID == jobB.UUID {
		t.Fatal("Jobs from different machines should have different UUIDs")
	}"""
    content = content.replace(samecommit_old, samecommit_new)

    realistic_old = """	dbA := env.openDB("machine_a.db")
	dbB := env.openDB("machine_b.db")
	dbC := env.openDB("machine_c.db")

	sharedRepoIdentity := "git@github.com:team/shared-project.git"

	repoA, err := dbA.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_a"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine A: GetOrCreateRepo failed: %v", err)
	}
	repoB, err := dbB.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_b"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine B: GetOrCreateRepo failed: %v", err)
	}
	repoC, err := dbC.GetOrCreateRepo(filepath.Join(env.TmpDir, "repo_c"), sharedRepoIdentity)
	if err != nil {
		t.Fatalf("Machine C: GetOrCreateRepo failed: %v", err)
	}

	workerA := startSyncWorker(t, dbA, env.pgURL, "alice-laptop", "1h")
	workerB := startSyncWorker(t, dbB, env.pgURL, "bob-desktop", "1h")
	workerC := startSyncWorker(t, dbC, env.pgURL, "carol-workstation", "1h")"""

    realistic_new = """	sharedRepoIdentity := "git@github.com:team/shared-project.git"

	nodeA := env.setupNode("alice-laptop", sharedRepoIdentity, "1h")
	nodeB := env.setupNode("bob-desktop", sharedRepoIdentity, "1h")
	nodeC := env.setupNode("carol-workstation", sharedRepoIdentity, "1h")

	dbA, repoA, workerA := nodeA.DB, nodeA.Repo, nodeA.Worker
	dbB, repoB, workerB := nodeB.DB, nodeB.Repo, nodeB.Worker
	dbC, repoC, workerC := nodeC.DB, nodeC.Repo, nodeC.Worker"""
    content = content.replace(realistic_old, realistic_new)

    # 8. Refactor Concurrent block
    concurrent_old = """		go func() {
			for i := 0; i < 10; i++ {
				job, _, err := tryCreateCompletedReview(dbA, repoA.ID, fmt.Sprintf("a3_%02d", i), "Alice", fmt.Sprintf("Alice concurrent %d", i), "prompt", fmt.Sprintf("Review A3-%d", i))
				if err != nil {
					jobResultsA <- jobResult{err: err}
					continue
				}
				jobResultsA <- jobResult{uuid: job.UUID}
				if i%3 == 0 {
					if _, err := workerA.SyncNow(); err != nil {
						syncErrsA <- fmt.Errorf("Machine A round 3 sync at job %d: %w", i, err)
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			close(jobResultsA)
			close(syncErrsA)
			done <- true
		}()

		go func() {
			for i := 0; i < 10; i++ {
				job, _, err := tryCreateCompletedReview(dbB, repoB.ID, fmt.Sprintf("b3_%02d", i), "Bob", fmt.Sprintf("Bob concurrent %d", i), "prompt", fmt.Sprintf("Review B3-%d", i))
				if err != nil {
					jobResultsB <- jobResult{err: err}
					continue
				}
				jobResultsB <- jobResult{uuid: job.UUID}
				if i%3 == 0 {
					if _, err := workerB.SyncNow(); err != nil {
						syncErrsB <- fmt.Errorf("Machine B round 3 sync at job %d: %w", i, err)
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			close(jobResultsB)
			close(syncErrsB)
			done <- true
		}()

		go func() {
			for i := 0; i < 10; i++ {
				job, _, err := tryCreateCompletedReview(dbC, repoC.ID, fmt.Sprintf("c3_%02d", i), "Carol", fmt.Sprintf("Carol concurrent %d", i), "prompt", fmt.Sprintf("Review C3-%d", i))
				if err != nil {
					jobResultsC <- jobResult{err: err}
					continue
				}
				jobResultsC <- jobResult{uuid: job.UUID}
				if i%3 == 0 {
					if _, err := workerC.SyncNow(); err != nil {
						syncErrsC <- fmt.Errorf("Machine C round 3 sync at job %d: %w", i, err)
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			close(jobResultsC)
			close(syncErrsC)
			done <- true
		}()"""

    # We need to change chan type
    chan_type_old = """		type jobResult struct {
			uuid string
			err  error
		}
		jobResultsA := make(chan jobResult, 10)
		jobResultsB := make(chan jobResult, 10)
		jobResultsC := make(chan jobResult, 10)"""
    
    chan_type_new = """		jobResultsA := make(chan string, 10)
		jobResultsB := make(chan string, 10)
		jobResultsC := make(chan string, 10)"""
    content = content.replace(chan_type_old, chan_type_new)

    concurrent_new = """		runConcurrentReviewsAndSync(dbA, repoA.ID, workerA, "a3", "Alice", 10, jobResultsA, syncErrsA, done)
		runConcurrentReviewsAndSync(dbB, repoB.ID, workerB, "b3", "Bob", 10, jobResultsB, syncErrsB, done)
		runConcurrentReviewsAndSync(dbC, repoC.ID, workerC, "c3", "Carol", 10, jobResultsC, syncErrsC, done)"""
    content = content.replace(concurrent_old, concurrent_new)

    result_read_old = """		for r := range jobResultsA {
			if r.err != nil {
				t.Errorf("%v", r.err)
			} else {
				jobsCreatedByA = append(jobsCreatedByA, r.uuid)
			}
		}
		for r := range jobResultsB {
			if r.err != nil {
				t.Errorf("%v", r.err)
			} else {
				jobsCreatedByB = append(jobsCreatedByB, r.uuid)
			}
		}
		for r := range jobResultsC {
			if r.err != nil {
				t.Errorf("%v", r.err)
			} else {
				jobsCreatedByC = append(jobsCreatedByC, r.uuid)
			}
		}"""

    result_read_new = """		for uuid := range jobResultsA {
			jobsCreatedByA = append(jobsCreatedByA, uuid)
		}
		for uuid := range jobResultsB {
			jobsCreatedByB = append(jobsCreatedByB, uuid)
		}
		for uuid := range jobResultsC {
			jobsCreatedByC = append(jobsCreatedByC, uuid)
		}"""
    content = content.replace(result_read_old, result_read_new)

    with open(file_path, 'w') as f:
        f.write(content)

modify_file('internal/storage/postgres_integration_test.go')
