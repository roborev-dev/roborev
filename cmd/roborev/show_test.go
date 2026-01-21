package main

// Tests for the show command

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

func TestShowCommandSHADisambiguation(t *testing.T) {
	t.Run("numeric ref resolvable in repo treated as SHA not job ID", func(t *testing.T) {
		// Create a git repo with a numeric tag to test the disambiguation path
		repoDir := t.TempDir()
		runGit := func(args ...string) string {
			cmd := exec.Command("git", args...)
			cmd.Dir = repoDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			} else if len(out) > 0 {
				return strings.TrimSpace(string(out))
			}
			return ""
		}

		runGit("init")
		runGit("symbolic-ref", "HEAD", "refs/heads/main")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "initial commit")

		// Create a numeric tag - "12345" is numeric and could be mistaken for a job ID
		runGit("tag", "12345")
		tagSHA := runGit("rev-parse", "12345")

		var receivedQuery string
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
			if r.URL.Path == "/api/review" && r.Method == "GET" {
				receivedQuery = r.URL.RawQuery
				json.NewEncoder(w).Encode(storage.Review{
					ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
				})
				return
			}
		}))
		defer cleanup()

		origDir, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origDir)

		// Pass "12345" which is numeric but resolvable as a git tag
		// Should be treated as SHA, not job ID
		cmd := showCmd()
		cmd.SetArgs([]string{"12345"})
		_ = captureStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		// Should query by SHA (resolved from tag), not job_id
		if !strings.Contains(receivedQuery, "sha=") {
			t.Errorf("expected sha= in query, got: %s", receivedQuery)
		}
		if strings.Contains(receivedQuery, "job_id=") {
			t.Errorf("numeric ref '12345' should resolve as SHA, not job_id; got: %s", receivedQuery)
		}

		// Verify the resolved SHA was sent (not the literal "12345")
		if !strings.Contains(receivedQuery, tagSHA[:7]) {
			t.Errorf("expected query to contain resolved SHA %s, got: %s", tagSHA[:7], receivedQuery)
		}
	})

	t.Run("numeric non-resolvable treated as job ID", func(t *testing.T) {
		// Create a git repo where "99999" is not a valid ref
		repoDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = repoDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			}
		}

		runGit("init")
		runGit("symbolic-ref", "HEAD", "refs/heads/main")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "initial commit")

		var receivedQuery string
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
			if r.URL.Path == "/api/review" && r.Method == "GET" {
				receivedQuery = r.URL.RawQuery
				json.NewEncoder(w).Encode(storage.Review{
					ID: 1, JobID: 99999, Output: "LGTM", Agent: "test",
				})
				return
			}
		}))
		defer cleanup()

		origDir, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origDir)

		// "99999" is numeric and won't resolve as a git ref
		cmd := showCmd()
		cmd.SetArgs([]string{"99999"})
		_ = captureStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		// Should query by job_id since it's numeric and not a valid git ref
		if !strings.Contains(receivedQuery, "job_id=99999") {
			t.Errorf("expected job_id=99999 in query, got: %s", receivedQuery)
		}
		if strings.Contains(receivedQuery, "sha=") {
			t.Errorf("did not expect sha= in query, got: %s", receivedQuery)
		}
	})

	t.Run("non-numeric argument treated as SHA", func(t *testing.T) {
		// Create a git repo
		repoDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = repoDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			}
		}

		runGit("init")
		runGit("symbolic-ref", "HEAD", "refs/heads/main")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "initial commit")

		var receivedQuery string
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
			if r.URL.Path == "/api/review" && r.Method == "GET" {
				receivedQuery = r.URL.RawQuery
				json.NewEncoder(w).Encode(storage.Review{
					ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
				})
				return
			}
		}))
		defer cleanup()

		origDir, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origDir)

		// "abc123def" is not numeric, so it's treated as SHA even if not resolvable
		cmd := showCmd()
		cmd.SetArgs([]string{"abc123def"})
		_ = captureStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		// Should query by sha (not job_id)
		if !strings.Contains(receivedQuery, "sha=abc123def") {
			t.Errorf("expected sha=abc123def in query, got: %s", receivedQuery)
		}
		if strings.Contains(receivedQuery, "job_id=") {
			t.Errorf("did not expect job_id= in query, got: %s", receivedQuery)
		}
	})
}

func TestShowJobFlag(t *testing.T) {
	t.Run("--job forces job ID interpretation even when ref is resolvable", func(t *testing.T) {
		// Create a git repo with a numeric tag to prove --job bypasses resolution
		repoDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = repoDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			}
		}

		runGit("init")
		runGit("symbolic-ref", "HEAD", "refs/heads/main")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "initial commit")

		// Create a numeric tag - without --job, "12345" would resolve to this commit
		runGit("tag", "12345")

		var receivedQuery string
		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
			if r.URL.Path == "/api/review" && r.Method == "GET" {
				receivedQuery = r.URL.RawQuery
				json.NewEncoder(w).Encode(storage.Review{
					ID: 1, JobID: 12345, Output: "LGTM", Agent: "test",
				})
				return
			}
		}))
		defer cleanup()

		origDir, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origDir)

		// With --job, "12345" should be treated as job ID even though
		// it's resolvable as a git tag
		cmd := showCmd()
		cmd.SetArgs([]string{"--job", "12345"})
		_ = captureStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		// Should query by job_id
		if !strings.Contains(receivedQuery, "job_id=12345") {
			t.Errorf("expected job_id=12345 in query, got: %s", receivedQuery)
		}
		if strings.Contains(receivedQuery, "sha=") {
			t.Errorf("did not expect sha= in query, got: %s", receivedQuery)
		}
	})
}

func TestShowOutputFormat(t *testing.T) {
	t.Run("job ID shows 'Review for job X (by agent)'", func(t *testing.T) {
		repoDir := t.TempDir()
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = repoDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			}
		}

		runGit("init")
		runGit("symbolic-ref", "HEAD", "refs/heads/main")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "initial commit")

		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
			if r.URL.Path == "/api/review" && r.Method == "GET" {
				json.NewEncoder(w).Encode(storage.Review{
					ID: 1, JobID: 42, Output: "Test review output", Agent: "codex",
				})
				return
			}
		}))
		defer cleanup()

		origDir, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origDir)

		// Query by job ID
		cmd := showCmd()
		cmd.SetArgs([]string{"--job", "42"})
		output := captureStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		// Should show "Review for job 42 (by codex)" - no redundant "job 42"
		if !strings.Contains(output, "Review for job 42 (by codex)") {
			t.Errorf("expected 'Review for job 42 (by codex)' in output, got: %s", output)
		}
		// Should NOT show "(job 42, by codex)" which would be redundant
		if strings.Contains(output, "(job 42, by") {
			t.Errorf("did not expect redundant job ID in output, got: %s", output)
		}
	})

	t.Run("SHA shows 'Review for abc123 (job X, by agent)'", func(t *testing.T) {
		repoDir := t.TempDir()
		runGit := func(args ...string) string {
			cmd := exec.Command("git", args...)
			cmd.Dir = repoDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, out)
			} else if len(out) > 0 {
				return strings.TrimSpace(string(out))
			}
			return ""
		}

		runGit("init")
		runGit("symbolic-ref", "HEAD", "refs/heads/main")
		runGit("config", "user.email", "test@test.com")
		runGit("config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit("add", "file.txt")
		runGit("commit", "-m", "initial commit")

		commitSHA := runGit("rev-parse", "HEAD")
		shortSHA := commitSHA[:7]

		_, cleanup := setupMockDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" {
				json.NewEncoder(w).Encode(map[string]interface{}{"version": version.Version})
				return
			}
			if r.URL.Path == "/api/review" && r.Method == "GET" {
				json.NewEncoder(w).Encode(storage.Review{
					ID: 1, JobID: 42, Output: "Test review output", Agent: "codex",
				})
				return
			}
		}))
		defer cleanup()

		origDir, _ := os.Getwd()
		os.Chdir(repoDir)
		defer os.Chdir(origDir)

		// Query by SHA
		cmd := showCmd()
		cmd.SetArgs([]string{commitSHA})
		output := captureStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})

		// Should show "Review for abc123 (job 42, by codex)"
		expectedPattern := "Review for " + shortSHA + " (job 42, by codex)"
		if !strings.Contains(output, expectedPattern) {
			t.Errorf("expected '%s' in output, got: %s", expectedPattern, output)
		}
	})
}
