package prompt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

// setupTestRepo creates a git repo with multiple commits and returns the repo path and commit SHAs
func setupTestRepo(t *testing.T) (string, []string) {
	t.Helper()
	tmpDir := t.TempDir()

	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")

	var commits []string

	// Create 6 commits so we can test with 5 previous commits
	for i := 1; i <= 6; i++ {
		filename := filepath.Join(tmpDir, "file.txt")
		content := strings.Repeat("x", i) // Different content each time
		if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "commit "+string(rune('0'+i)))

		sha := runGit("rev-parse", "HEAD")
		commits = append(commits, sha)
	}

	return tmpDir, commits
}

func TestBuildPromptWithoutContext(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Build prompt without database (no previous reviews)
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain system prompt
	if !strings.Contains(prompt, "You are a code reviewer") {
		t.Error("Prompt should contain system prompt")
	}

	// Should contain the 5 review criteria
	expectedCriteria := []string{"Bugs", "Security", "Testing gaps", "Regressions", "Code quality"}
	for _, criteria := range expectedCriteria {
		if !strings.Contains(prompt, criteria) {
			t.Errorf("Prompt should contain %q", criteria)
		}
	}

	// Should contain current commit section
	if !strings.Contains(prompt, "## Current Commit") {
		t.Error("Prompt should contain current commit section")
	}

	// Should contain short SHA
	shortSHA := targetSHA[:7]
	if !strings.Contains(prompt, shortSHA) {
		t.Errorf("Prompt should contain short SHA %s", shortSHA)
	}

	// Should NOT contain previous reviews section (no db)
	if strings.Contains(prompt, "## Previous Reviews") {
		t.Error("Prompt should not contain previous reviews section without db")
	}
}

func TestBuildPromptWithPreviousReviews(t *testing.T) {
	repoPath, commits := setupTestRepo(t)

	// Setup test database
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	// Create repo and commits in DB
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create reviews for commits 2, 3, and 4 (leaving 1 and 5 without reviews)
	reviewTexts := map[int]string{
		1: "Review for commit 2: Found a bug in error handling",
		2: "Review for commit 3: No issues found",
		3: "Review for commit 4: Security issue - missing input validation",
	}

	for i, sha := range commits[:5] { // First 5 commits (parents of commit 6)
		commit, err := db.GetOrCreateCommit(repo.ID, sha, "Test", "commit message", time.Now())
		if err != nil {
			t.Fatalf("GetOrCreateCommit failed: %v", err)
		}

		// Create review for some commits
		if reviewText, ok := reviewTexts[i]; ok {
			job, err := db.EnqueueJob(repo.ID, commit.ID, sha, "", "test", "", "")
			if err != nil {
				t.Fatalf("EnqueueJob failed: %v", err)
			}
			_, err = db.ClaimJob("test-worker")
			if err != nil {
				t.Fatalf("ClaimJob failed: %v", err)
			}
			err = db.CompleteJob(job.ID, "test", "test prompt", reviewText)
			if err != nil {
				t.Fatalf("CompleteJob failed: %v", err)
			}
		}
	}

	// Also add commit 6 to DB (the target commit)
	_, err = db.GetOrCreateCommit(repo.ID, commits[5], "Test", "commit message", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Build prompt with 5 previous commits context
	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, commits[5], repo.ID, 5, "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain previous reviews section
	if !strings.Contains(prompt, "## Previous Reviews") {
		t.Error("Prompt should contain previous reviews section")
	}

	// Should contain the review texts we added
	for _, reviewText := range reviewTexts {
		if !strings.Contains(prompt, reviewText) {
			t.Errorf("Prompt should contain review text: %s", reviewText)
		}
	}

	// Should contain "No review available" for commits without reviews
	if !strings.Contains(prompt, "No review available") {
		t.Error("Prompt should contain 'No review available' for commits without reviews")
	}

	// Should contain delimiters with short SHAs
	if !strings.Contains(prompt, "--- Review for commit") {
		t.Error("Prompt should contain review delimiters")
	}

	// Verify chronological order (oldest first)
	// The oldest parent (commit 1) should appear before the newest parent (commit 5)
	commit1Pos := strings.Index(prompt, commits[0][:7])
	commit5Pos := strings.Index(prompt, commits[4][:7])
	if commit1Pos == -1 || commit5Pos == -1 {
		t.Error("Prompt should contain short SHAs of parent commits")
	} else if commit1Pos > commit5Pos {
		t.Error("Commits should be in chronological order (oldest first)")
	}
}

func TestBuildPromptWithPreviousReviewsAndResponses(t *testing.T) {
	repoPath, commits := setupTestRepo(t)

	// Setup test database
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	// Create repo
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Create review for commit 3 (parent of commit 6) with responses
	parentSHA := commits[2] // commit 3
	commit3, _ := db.GetOrCreateCommit(repo.ID, parentSHA, "Test", "commit 3", time.Now())
	job, _ := db.EnqueueJob(repo.ID, commit3.ID, parentSHA, "", "test", "", "")
	db.ClaimJob("test-worker")
	db.CompleteJob(job.ID, "test", "prompt", "Found potential memory leak in connection pool")

	// Add comments to the previous review
	_, err = db.AddCommentToJob(job.ID, "alice", "Known issue, will fix in next sprint")
	if err != nil {
		t.Fatalf("AddCommentToJob failed: %v", err)
	}
	_, err = db.AddCommentToJob(job.ID, "bob", "Added to tech debt backlog")
	if err != nil {
		t.Fatalf("AddCommentToJob failed: %v", err)
	}

	// Also add commits 4 and 5 to DB
	for _, sha := range commits[3:5] {
		db.GetOrCreateCommit(repo.ID, sha, "Test", "commit", time.Now())
	}

	// Build prompt for commit 6 with context from previous 5 commits
	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, commits[5], repo.ID, 5, "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain previous reviews section
	if !strings.Contains(prompt, "## Previous Reviews") {
		t.Error("Prompt should contain previous reviews section")
	}

	// Should contain the review text
	if !strings.Contains(prompt, "memory leak in connection pool") {
		t.Error("Prompt should contain the previous review text")
	}

	// Should contain comments on the previous review
	if !strings.Contains(prompt, "Comments on this review:") {
		t.Error("Prompt should contain comments section for previous review")
	}

	if !strings.Contains(prompt, "alice") {
		t.Error("Prompt should contain commenter 'alice'")
	}

	if !strings.Contains(prompt, "Known issue, will fix in next sprint") {
		t.Error("Prompt should contain alice's comment text")
	}

	if !strings.Contains(prompt, "bob") {
		t.Error("Prompt should contain commenter 'bob'")
	}

	if !strings.Contains(prompt, "Added to tech debt backlog") {
		t.Error("Prompt should contain bob's comment text")
	}
}

func TestBuildPromptWithNoParentCommits(t *testing.T) {
	// Create a repo with just one commit
	tmpDir := t.TempDir()

	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "initial commit")
	sha := runGit("rev-parse", "HEAD")

	// Setup test database
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	// Build prompt - should work even with no parent commits
	builder := NewBuilder(db)
	prompt, err := builder.Build(tmpDir, sha, 0, 5, "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain system prompt and current commit
	if !strings.Contains(prompt, "You are a code reviewer") {
		t.Error("Prompt should contain system prompt")
	}
	if !strings.Contains(prompt, "## Current Commit") {
		t.Error("Prompt should contain current commit section")
	}

	// Should NOT contain previous reviews (no parents exist)
	if strings.Contains(prompt, "## Previous Reviews") {
		t.Error("Prompt should not contain previous reviews section when no parents exist")
	}
}

func TestPromptContainsExpectedFormat(t *testing.T) {
	repoPath, commits := setupTestRepo(t)

	// Setup test database with one review
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	repo, _ := db.GetOrCreateRepo(repoPath)
	commit, _ := db.GetOrCreateCommit(repo.ID, commits[4], "Test", "test", time.Now())
	job, _ := db.EnqueueJob(repo.ID, commit.ID, commits[4], "", "test", "", "")
	db.ClaimJob("test-worker")
	db.CompleteJob(job.ID, "test", "prompt", "Found 1 issue:\n1. pkg/cache/store.go:112 - Race condition")

	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, commits[5], repo.ID, 3, "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Print the prompt for visual inspection
	t.Logf("Generated prompt:\n%s", prompt)

	// Verify structure
	sections := []string{
		"You are a code reviewer",
		"## Previous Reviews",
		"--- Review for commit",
		"## Current Commit",
	}

	for _, section := range sections {
		if !strings.Contains(prompt, section) {
			t.Errorf("Prompt missing section: %s", section)
		}
	}
}

func TestBuildPromptWithProjectGuidelines(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create .roborev.toml with review guidelines as multi-line string
	configContent := `
agent = "codex"
review_guidelines = """
We are not doing database migrations because there are no production databases yet.
Prefer composition over inheritance.
All public APIs must have documentation comments.
"""
`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Build prompt without database
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain project guidelines section
	if !strings.Contains(prompt, "## Project Guidelines") {
		t.Error("Prompt should contain project guidelines section")
	}

	// Should contain the guidelines text
	if !strings.Contains(prompt, "database migrations") {
		t.Error("Prompt should contain guidelines about database migrations")
	}
	if !strings.Contains(prompt, "composition over inheritance") {
		t.Error("Prompt should contain guidelines about composition")
	}
	if !strings.Contains(prompt, "documentation comments") {
		t.Error("Prompt should contain guidelines about documentation")
	}

	// Print prompt for inspection
	t.Logf("Generated prompt with guidelines:\n%s", prompt)
}

func TestBuildPromptWithoutProjectGuidelines(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create .roborev.toml WITHOUT review guidelines
	configContent := `agent = "codex"`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Build prompt
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should NOT contain project guidelines section
	if strings.Contains(prompt, "## Project Guidelines") {
		t.Error("Prompt should not contain project guidelines section when none configured")
	}
}

func TestBuildPromptNoConfig(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Build prompt
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should NOT contain project guidelines section
	if strings.Contains(prompt, "## Project Guidelines") {
		t.Error("Prompt should not contain project guidelines section when no config file")
	}

	// Should still contain standard sections
	if !strings.Contains(prompt, "You are a code reviewer") {
		t.Error("Prompt should contain system prompt")
	}
	if !strings.Contains(prompt, "## Current Commit") {
		t.Error("Prompt should contain current commit section")
	}
}

func TestBuildPromptGuidelinesOrder(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Create .roborev.toml with review guidelines
	configContent := `review_guidelines = "Test guideline"`
	configPath := filepath.Join(repoPath, ".roborev.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Build prompt
	prompt, err := BuildSimple(repoPath, targetSHA, "")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Guidelines should appear after system prompt but before current commit
	systemPromptPos := strings.Index(prompt, "You are a code reviewer")
	guidelinesPos := strings.Index(prompt, "## Project Guidelines")
	currentCommitPos := strings.Index(prompt, "## Current Commit")

	if guidelinesPos == -1 {
		t.Fatal("Guidelines section not found")
	}

	if systemPromptPos > guidelinesPos {
		t.Error("System prompt should come before guidelines")
	}

	if guidelinesPos > currentCommitPos {
		t.Error("Guidelines should come before current commit section")
	}
}

func TestBuildPromptWithPreviousAttempts(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[5] // Last commit

	// Setup test database
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	// Create repo and commit in DB
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	commit, err := db.GetOrCreateCommit(repo.ID, targetSHA, "Test", "commit message", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}

	// Create two previous reviews for the SAME commit (simulating re-reviews)
	reviewTexts := []string{
		"First review: Found missing error handling",
		"Second review: Still missing error handling, also found SQL injection",
	}

	for _, reviewText := range reviewTexts {
		job, err := db.EnqueueJob(repo.ID, commit.ID, targetSHA, "", "test", "", "")
		if err != nil {
			t.Fatalf("EnqueueJob failed: %v", err)
		}
		_, err = db.ClaimJob("test-worker")
		if err != nil {
			t.Fatalf("ClaimJob failed: %v", err)
		}
		err = db.CompleteJob(job.ID, "test", "test prompt", reviewText)
		if err != nil {
			t.Fatalf("CompleteJob failed: %v", err)
		}
	}

	// Build prompt - should include previous attempts for the same commit
	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, targetSHA, repo.ID, 0, "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain previous review attempts section
	if !strings.Contains(prompt, "## Previous Review Attempts") {
		t.Error("Prompt should contain previous review attempts section")
	}

	// Should contain both review texts
	for _, reviewText := range reviewTexts {
		if !strings.Contains(prompt, reviewText) {
			t.Errorf("Prompt should contain review text: %s", reviewText)
		}
	}

	// Should contain attempt numbers
	if !strings.Contains(prompt, "Review Attempt 1") {
		t.Error("Prompt should contain 'Review Attempt 1'")
	}
	if !strings.Contains(prompt, "Review Attempt 2") {
		t.Error("Prompt should contain 'Review Attempt 2'")
	}
}

func TestBuildPromptWithPreviousAttemptsAndResponses(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[5]

	// Setup test database
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer db.Close()

	repo, _ := db.GetOrCreateRepo(repoPath)
	commit, _ := db.GetOrCreateCommit(repo.ID, targetSHA, "Test", "test", time.Now())

	// Create a previous review
	job, _ := db.EnqueueJob(repo.ID, commit.ID, targetSHA, "", "test", "", "")
	db.ClaimJob("test-worker")
	db.CompleteJob(job.ID, "test", "prompt", "Found issue: missing null check")

	// Add a response to the previous review
	_, err = db.AddCommentToJob(job.ID, "developer", "This is intentional, the value is never null here")
	if err != nil {
		t.Fatalf("AddCommentToJob failed: %v", err)
	}

	// Build prompt for a new review of the same commit
	builder := NewBuilder(db)
	prompt, err := builder.Build(repoPath, targetSHA, repo.ID, 0, "")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should contain the previous review
	if !strings.Contains(prompt, "## Previous Review Attempts") {
		t.Error("Prompt should contain previous review attempts section")
	}

	if !strings.Contains(prompt, "missing null check") {
		t.Error("Prompt should contain the previous review text")
	}

	// Should contain the comment
	if !strings.Contains(prompt, "Comments on this review:") {
		t.Error("Prompt should contain comments section")
	}

	if !strings.Contains(prompt, "This is intentional") {
		t.Error("Prompt should contain the comment text")
	}

	if !strings.Contains(prompt, "developer") {
		t.Error("Prompt should contain the commenter name")
	}
}

func TestBuildPromptWithGeminiAgent(t *testing.T) {
	repoPath, commits := setupTestRepo(t)
	targetSHA := commits[len(commits)-1]

	// Build prompt for Gemini agent
	prompt, err := BuildSimple(repoPath, targetSHA, "gemini")
	if err != nil {
		t.Fatalf("BuildSimple failed: %v", err)
	}

	// Should contain Gemini-specific instructions
	if !strings.Contains(prompt, "extremely concise and professional") {
		t.Error("Prompt should contain Gemini-specific instruction")
	}
	if !strings.Contains(prompt, "Summary") {
		t.Error("Prompt should contain 'Summary' section")
	}

	// Should NOT contain default system prompt text
	if strings.Contains(prompt, "Brief explanation of the problem and suggested fix") {
		t.Error("Prompt should not contain default system prompt text")
	}
}
