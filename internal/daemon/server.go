package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

// Server is the HTTP API server for the daemon
type Server struct {
	db            *storage.DB
	configWatcher *ConfigWatcher
	broadcaster   Broadcaster
	workerPool    *WorkerPool
	httpServer    *http.Server
	syncWorker    *storage.SyncWorker
	errorLog      *ErrorLog
	startTime     time.Time

	// Cached machine ID to avoid INSERT on every status request
	machineIDMu sync.Mutex
	machineID   string
}

// NewServer creates a new daemon server
func NewServer(db *storage.DB, cfg *config.Config, configPath string) *Server {
	// Always set for deterministic state - default to false (conservative)
	agent.SetAllowUnsafeAgents(cfg.AllowUnsafeAgents != nil && *cfg.AllowUnsafeAgents)
	agent.SetAnthropicAPIKey(cfg.AnthropicAPIKey)
	broadcaster := NewBroadcaster()

	// Initialize error log
	errorLog, err := NewErrorLog(DefaultErrorLogPath())
	if err != nil {
		log.Printf("Warning: failed to create error log: %v", err)
	}

	// Create config watcher for hot-reloading
	configWatcher := NewConfigWatcher(configPath, cfg, broadcaster)

	s := &Server{
		db:            db,
		configWatcher: configWatcher,
		broadcaster:   broadcaster,
		workerPool:    NewWorkerPool(db, configWatcher, cfg.MaxWorkers, broadcaster, errorLog),
		errorLog:      errorLog,
		startTime:     time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/enqueue", s.handleEnqueue)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/jobs", s.handleListJobs)
	mux.HandleFunc("/api/job/cancel", s.handleCancelJob)
	mux.HandleFunc("/api/job/rerun", s.handleRerunJob)
	mux.HandleFunc("/api/repos", s.handleListRepos)
	mux.HandleFunc("/api/review", s.handleGetReview)
	mux.HandleFunc("/api/review/address", s.handleAddressReview)
	mux.HandleFunc("/api/comment", s.handleAddComment)
	mux.HandleFunc("/api/comments", s.handleListComments)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stream/events", s.handleStreamEvents)
	mux.HandleFunc("/api/sync/now", s.handleSyncNow)
	mux.HandleFunc("/api/sync/status", s.handleSyncStatus)

	s.httpServer = &http.Server{
		Addr:    cfg.ServerAddr,
		Handler: mux,
	}

	return s
}

// Start begins the server and worker pool
func (s *Server) Start(ctx context.Context) error {
	// Clean up any zombie daemons first (there can be only one)
	if cleaned := CleanupZombieDaemons(); cleaned > 0 {
		log.Printf("Cleaned up %d zombie daemon(s)", cleaned)
	}

	// Check if a responsive daemon is still running after cleanup
	if info, err := GetAnyRunningDaemon(); err == nil && IsDaemonAlive(info.Addr) {
		return fmt.Errorf("daemon already running (pid %d on %s)", info.PID, info.Addr)
	}

	// Reset stale jobs from previous runs
	if err := s.db.ResetStaleJobs(); err != nil {
		log.Printf("Warning: failed to reset stale jobs: %v", err)
	}

	// Start config watcher for hot-reloading
	if err := s.configWatcher.Start(ctx); err != nil {
		log.Printf("Warning: failed to start config watcher: %v", err)
		// Continue without hot-reloading - not a fatal error
	}

	// Find available port
	cfg := s.configWatcher.Config()
	addr, port, err := FindAvailablePort(cfg.ServerAddr)
	if err != nil {
		s.configWatcher.Stop()
		return fmt.Errorf("find available port: %w", err)
	}
	s.httpServer.Addr = addr

	// Write runtime info so CLI can find us
	if err := WriteRuntime(addr, port, version.Version); err != nil {
		log.Printf("Warning: failed to write runtime info: %v", err)
	}

	// Start worker pool
	s.workerPool.Start()

	// Start HTTP server
	log.Printf("Starting HTTP server on %s", addr)
	if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
		s.configWatcher.Stop()
		s.workerPool.Stop()
		return err
	}
	return nil
}

// Stop gracefully shuts down the server
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Remove runtime info
	RemoveRuntime()

	// Stop config watcher
	s.configWatcher.Stop()

	// Stop HTTP server
	if err := s.httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	// Stop worker pool
	s.workerPool.Stop()

	// Close error log
	if s.errorLog != nil {
		s.errorLog.Close()
	}

	return nil
}

// SetSyncWorker sets the sync worker for triggering manual syncs
func (s *Server) SetSyncWorker(sw *storage.SyncWorker) {
	s.syncWorker = sw
}

// handleSyncNow triggers an immediate sync cycle
func (s *Server) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.syncWorker == nil {
		http.Error(w, "Sync not enabled", http.StatusNotFound)
		return
	}

	// Check if client wants streaming progress
	stream := r.URL.Query().Get("stream") == "1"

	if stream {
		// Stream progress as newline-delimited JSON
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		stats, err := s.syncWorker.SyncNowWithProgress(func(p storage.SyncProgress) {
			line, _ := json.Marshal(map[string]interface{}{
				"type":        "progress",
				"phase":       p.Phase,
				"batch":       p.BatchNum,
				"batch_jobs":  p.BatchJobs,
				"batch_revs":  p.BatchRevs,
				"batch_resps": p.BatchResps,
				"total_jobs":  p.TotalJobs,
				"total_revs":  p.TotalRevs,
				"total_resps": p.TotalResps,
			})
			w.Write(line)
			w.Write([]byte("\n"))
			flusher.Flush()
		})

		if err != nil {
			line, _ := json.Marshal(map[string]interface{}{
				"type":  "error",
				"error": err.Error(),
			})
			w.Write(line)
			w.Write([]byte("\n"))
			return
		}

		// Final result
		line, _ := json.Marshal(map[string]interface{}{
			"type":    "complete",
			"message": "Sync completed",
			"pushed": map[string]int{
				"jobs":      stats.PushedJobs,
				"reviews":   stats.PushedReviews,
				"responses": stats.PushedResponses,
			},
			"pulled": map[string]int{
				"jobs":      stats.PulledJobs,
				"reviews":   stats.PulledReviews,
				"responses": stats.PulledResponses,
			},
		})
		w.Write(line)
		w.Write([]byte("\n"))
		return
	}

	// Non-streaming: wait for completion
	stats, err := s.syncWorker.SyncNow()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Sync completed",
		"pushed": map[string]int{
			"jobs":      stats.PushedJobs,
			"reviews":   stats.PushedReviews,
			"responses": stats.PushedResponses,
		},
		"pulled": map[string]int{
			"jobs":      stats.PulledJobs,
			"reviews":   stats.PulledReviews,
			"responses": stats.PulledResponses,
		},
	})
}

// handleSyncStatus returns the current sync worker health status
func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if s.syncWorker == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":   false,
			"connected": false,
			"message":   "sync not enabled",
		})
		return
	}

	healthy, message := s.syncWorker.HealthCheck()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled":   true,
		"connected": healthy,
		"message":   message,
	})
}

// API request/response types

type EnqueueRequest struct {
	RepoPath     string `json:"repo_path"`
	CommitSHA    string `json:"commit_sha,omitempty"`    // Single commit (for backwards compat)
	GitRef       string `json:"git_ref,omitempty"`       // Single commit, range like "abc..def", or "dirty"
	Agent        string `json:"agent,omitempty"`
	DiffContent  string `json:"diff_content,omitempty"`  // Pre-captured diff for dirty reviews
	Reasoning    string `json:"reasoning,omitempty"`     // Reasoning level: thorough, standard, fast
	CustomPrompt string `json:"custom_prompt,omitempty"` // Custom prompt for ad-hoc agent work
	Agentic      bool   `json:"agentic,omitempty"`       // Enable agentic mode (allow file edits)
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

// writeInternalError writes an internal error response and logs it
func (s *Server) writeInternalError(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusInternalServerError, msg)
	if s.errorLog != nil {
		s.errorLog.LogError("server", msg, 0)
	}
}

func (s *Server) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Limit request body size to prevent DoS via large payloads
	// 250KB allows for 200KB diff content plus JSON overhead
	const maxBodySize = 250 * 1024
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var req EnqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Use errors.As for reliable detection of MaxBytesReader errors
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large (max 250KB)")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Support both git_ref and commit_sha (backwards compat)
	gitRef := req.GitRef
	if gitRef == "" {
		gitRef = req.CommitSHA
	}

	if req.RepoPath == "" || gitRef == "" {
		writeError(w, http.StatusBadRequest, "repo_path and git_ref (or commit_sha) are required")
		return
	}

	// Get the working directory root for git commands (may be a worktree)
	// This is needed to resolve refs like HEAD correctly in the worktree context
	gitCwd, err := git.GetRepoRoot(req.RepoPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("not a git repository: %v", err))
		return
	}

	// Get the main repo root for database storage
	// This ensures worktrees are associated with their main repository
	repoRoot, err := git.GetMainRepoRoot(req.RepoPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("not a git repository: %v", err))
		return
	}

	// Check if branch is excluded from reviews
	currentBranch := git.GetCurrentBranch(gitCwd)
	if currentBranch != "" && config.IsBranchExcluded(repoRoot, currentBranch) {
		// Silently skip excluded branches - return 200 OK with skipped flag
		writeJSON(w, http.StatusOK, map[string]any{
			"skipped": true,
			"reason":  fmt.Sprintf("branch %q is excluded from reviews", currentBranch),
		})
		return
	}

	// Resolve repo identity for sync
	repoIdentity := config.ResolveRepoIdentity(repoRoot, nil)

	// Get or create repo with identity
	repo, err := s.db.GetOrCreateRepo(repoRoot, repoIdentity)
	if err != nil {
		s.writeInternalError(w, fmt.Sprintf("get repo: %v", err))
		return
	}

	// Resolve agent (uses main repo root for config lookup)
	agentName := config.ResolveAgent(req.Agent, repoRoot, s.configWatcher.Config())

	// Resolve reasoning level (uses main repo root for config lookup)
	reasoning, err := config.ResolveReviewReasoning(req.Reasoning, repoRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Check if this is a custom prompt, dirty review, range, or single commit
	// Note: isPrompt is determined by whether custom_prompt is provided, not git_ref value
	// This allows reviewing a branch literally named "prompt" without collision
	isPrompt := req.CustomPrompt != ""
	isDirty := !isPrompt && gitRef == "dirty"
	isRange := !isPrompt && !isDirty && strings.Contains(gitRef, "..")

	// Validate dirty review has diff content
	if isDirty && req.DiffContent == "" {
		writeError(w, http.StatusBadRequest, "diff_content required for dirty review")
		return
	}

	// Server-side size validation for dirty diffs (200KB max)
	const maxDiffSize = 200 * 1024
	if isDirty && len(req.DiffContent) > maxDiffSize {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("diff_content too large (%d bytes, max %d)", len(req.DiffContent), maxDiffSize))
		return
	}

	var job *storage.ReviewJob
	if isPrompt {
		// Custom prompt job - use provided prompt directly
		job, err = s.db.EnqueuePromptJob(repo.ID, agentName, reasoning, req.CustomPrompt, req.Agentic)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("enqueue prompt job: %v", err))
			return
		}
	} else if isDirty {
		// Dirty review - use pre-captured diff
		job, err = s.db.EnqueueDirtyJob(repo.ID, gitRef, agentName, reasoning, req.DiffContent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("enqueue dirty job: %v", err))
			return
		}
	} else if isRange {
		// For ranges, resolve both endpoints and create range job
		// Use gitCwd to resolve refs correctly in worktree context
		parts := strings.SplitN(gitRef, "..", 2)
		startSHA, err := git.ResolveSHA(gitCwd, parts[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid start commit: %v", err))
			return
		}
		endSHA, err := git.ResolveSHA(gitCwd, parts[1])
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid end commit: %v", err))
			return
		}

		// Store as full SHA range
		fullRef := startSHA + ".." + endSHA
		job, err = s.db.EnqueueRangeJob(repo.ID, fullRef, agentName, reasoning)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("enqueue job: %v", err))
			return
		}
	} else {
		// Single commit - use gitCwd to resolve refs correctly in worktree context
		sha, err := git.ResolveSHA(gitCwd, gitRef)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid commit: %v", err))
			return
		}

		// Get commit info (SHA is absolute, so main repo root works fine)
		info, err := git.GetCommitInfo(repoRoot, sha)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("get commit info: %v", err))
			return
		}

		// Get or create commit
		commit, err := s.db.GetOrCreateCommit(repo.ID, sha, info.Author, info.Subject, info.Timestamp)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("get commit: %v", err))
			return
		}

		job, err = s.db.EnqueueJob(repo.ID, commit.ID, sha, agentName, reasoning)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("enqueue job: %v", err))
			return
		}
		job.CommitSubject = commit.Subject
	}

	// Fill in joined fields
	job.RepoPath = repo.RootPath
	job.RepoName = repo.Name

	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Support fetching a single job by ID
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		var jobID int64
		if _, err := fmt.Sscanf(idStr, "%d", &jobID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid id parameter")
			return
		}
		job, err := s.db.GetJobByID(jobID)
		if err != nil {
			// Distinguish "not found" from actual DB errors
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
				return
			}
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("database error: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"jobs":     []storage.ReviewJob{*job},
			"has_more": false,
		})
		return
	}

	status := r.URL.Query().Get("status")
	repo := r.URL.Query().Get("repo")
	gitRef := r.URL.Query().Get("git_ref")

	// Parse limit from query, default to 50, 0 means no limit
	// Clamp to valid range: 0 (unlimited) or 1-10000
	const maxLimit = 10000
	limit := 50
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if _, err := fmt.Sscanf(limitStr, "%d", &limit); err != nil {
			limit = 50
		}
	}
	// Clamp negative to 0, and cap at maxLimit (0 = unlimited is allowed)
	if limit < 0 {
		limit = 0
	} else if limit > maxLimit {
		limit = maxLimit
	}

	// Parse offset from query, default to 0
	// Offset is ignored when limit=0 (unlimited) since OFFSET requires LIMIT in SQL
	offset := 0
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if _, err := fmt.Sscanf(offsetStr, "%d", &offset); err != nil {
			offset = 0
		}
	}
	if offset < 0 || limit == 0 {
		offset = 0
	}

	// Fetch one extra to determine if there are more results
	fetchLimit := limit
	if limit > 0 {
		fetchLimit = limit + 1
	}

	jobs, err := s.db.ListJobs(status, repo, fetchLimit, offset, gitRef)
	if err != nil {
		s.writeInternalError(w, fmt.Sprintf("list jobs: %v", err))
		return
	}

	// Determine if there are more results
	hasMore := false
	if limit > 0 && len(jobs) > limit {
		hasMore = true
		jobs = jobs[:limit] // Trim to requested limit
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"jobs":     jobs,
		"has_more": hasMore,
	})
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	repos, totalCount, err := s.db.ListReposWithReviewCounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list repos: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"repos":       repos,
		"total_count": totalCount,
	})
}

type CancelJobRequest struct {
	JobID int64 `json:"job_id"`
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req CancelJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.JobID == 0 {
		writeError(w, http.StatusBadRequest, "job_id is required")
		return
	}

	// Cancel in DB first (marks as canceled)
	if err := s.db.CancelJob(req.JobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "job not found or not cancellable")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cancel job: %v", err))
		return
	}

	// Also cancel the running worker if job was running (kills subprocess)
	s.workerPool.CancelJob(req.JobID)

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type RerunJobRequest struct {
	JobID int64 `json:"job_id"`
}

func (s *Server) handleRerunJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req RerunJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.JobID == 0 {
		writeError(w, http.StatusBadRequest, "job_id is required")
		return
	}

	if err := s.db.ReenqueueJob(req.JobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "job not found or not rerunnable")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("rerun job: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) handleGetReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var review *storage.Review
	var err error

	// Support lookup by job_id (preferred) or sha
	if jobIDStr := r.URL.Query().Get("job_id"); jobIDStr != "" {
		var jobID int64
		if _, err := fmt.Sscanf(jobIDStr, "%d", &jobID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid job_id")
			return
		}
		review, err = s.db.GetReviewByJobID(jobID)
	} else if sha := r.URL.Query().Get("sha"); sha != "" {
		review, err = s.db.GetReviewByCommitSHA(sha)
	} else {
		writeError(w, http.StatusBadRequest, "job_id or sha parameter required")
		return
	}

	if err != nil {
		writeError(w, http.StatusNotFound, "review not found")
		return
	}

	writeJSON(w, http.StatusOK, review)
}

type AddCommentRequest struct {
	SHA       string `json:"sha,omitempty"`    // Legacy: link to commit by SHA
	JobID     int64  `json:"job_id,omitempty"` // Preferred: link to job
	Commenter string `json:"commenter"`
	Comment   string `json:"comment"`
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req AddCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Commenter == "" || req.Comment == "" {
		writeError(w, http.StatusBadRequest, "commenter and comment are required")
		return
	}

	// Must provide either job_id or sha
	if req.JobID == 0 && req.SHA == "" {
		writeError(w, http.StatusBadRequest, "job_id or sha is required")
		return
	}

	var resp *storage.Response
	var err error

	if req.JobID != 0 {
		// Link to job (preferred method)
		resp, err = s.db.AddCommentToJob(req.JobID, req.Commenter, req.Comment)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "job not found")
				return
			}
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("add comment: %v", err))
			return
		}
	} else {
		// Legacy: link to commit by SHA
		commit, err := s.db.GetCommitBySHA(req.SHA)
		if err != nil {
			writeError(w, http.StatusNotFound, "commit not found")
			return
		}

		resp, err = s.db.AddComment(commit.ID, req.Commenter, req.Comment)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("add comment: %v", err))
			return
		}
	}

	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var responses []storage.Response
	var err error

	// Support lookup by job_id (preferred) or sha (legacy)
	if jobIDStr := r.URL.Query().Get("job_id"); jobIDStr != "" {
		var jobID int64
		if _, scanErr := fmt.Sscanf(jobIDStr, "%d", &jobID); scanErr != nil {
			writeError(w, http.StatusBadRequest, "invalid job_id")
			return
		}
		responses, err = s.db.GetCommentsForJob(jobID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("get responses: %v", err))
			return
		}
	} else if sha := r.URL.Query().Get("sha"); sha != "" {
		responses, err = s.db.GetCommentsForCommitSHA(sha)
		if err != nil {
			writeError(w, http.StatusNotFound, "commit not found")
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "job_id or sha parameter required")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"responses": responses})
}

// getMachineID returns the cached machine ID, fetching it on first successful call.
// Retries on each call until successful to handle transient DB errors.
func (s *Server) getMachineID() string {
	s.machineIDMu.Lock()
	defer s.machineIDMu.Unlock()

	if s.machineID != "" {
		return s.machineID
	}

	// Try to fetch - only cache on success
	if id, err := s.db.GetMachineID(); err == nil && id != "" {
		s.machineID = id
	}
	return s.machineID
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	queued, running, done, failed, canceled, err := s.db.GetJobCounts()
	if err != nil {
		s.writeInternalError(w, fmt.Sprintf("get counts: %v", err))
		return
	}

	// Get config reload time and counter
	configReloadedAt := ""
	if t := s.configWatcher.LastReloadedAt(); !t.IsZero() {
		configReloadedAt = t.Format(time.RFC3339Nano)
	}
	configReloadCounter := s.configWatcher.ReloadCounter()

	status := storage.DaemonStatus{
		Version:             version.Version,
		QueuedJobs:          queued,
		RunningJobs:         running,
		CompletedJobs:       done,
		FailedJobs:          failed,
		CanceledJobs:        canceled,
		ActiveWorkers:       s.workerPool.ActiveWorkers(),
		MaxWorkers:          s.workerPool.MaxWorkers(),
		MachineID:           s.getMachineID(),
		ConfigReloadedAt:    configReloadedAt,
		ConfigReloadCounter: configReloadCounter,
	}

	writeJSON(w, http.StatusOK, status)
}

type AddressReviewRequest struct {
	ReviewID  int64 `json:"review_id"`
	Addressed bool  `json:"addressed"`
}

func (s *Server) handleAddressReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req AddressReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ReviewID == 0 {
		writeError(w, http.StatusBadRequest, "review_id is required")
		return
	}

	if err := s.db.MarkReviewAddressed(req.ReviewID, req.Addressed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "review not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("mark addressed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) handleStreamEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Optional repo filter
	repoFilter := r.URL.Query().Get("repo")

	// Set headers for streaming
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Get flusher
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Subscribe to events
	subID, eventCh := s.broadcaster.Subscribe(repoFilter)
	defer s.broadcaster.Unsubscribe(subID)

	// Stream events until client disconnects
	encoder := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			// Client disconnected
			return
		case event, ok := <-eventCh:
			if !ok {
				// Channel closed (server shutdown)
				return
			}
			if err := encoder.Encode(event); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Calculate uptime
	uptime := time.Since(s.startTime)
	uptimeStr := formatDuration(uptime)

	// Check component health
	var components []storage.ComponentHealth
	allHealthy := true

	// Database health check
	dbHealthy := true
	dbMessage := ""
	if err := s.db.Ping(); err != nil {
		dbHealthy = false
		dbMessage = err.Error()
		allHealthy = false
	}
	components = append(components, storage.ComponentHealth{
		Name:    "database",
		Healthy: dbHealthy,
		Message: dbMessage,
	})

	// Worker health check - look for stalled jobs (running > 30 min)
	workersHealthy := true
	workersMessage := ""
	stalledCount, err := s.db.CountStalledJobs(30 * time.Minute)
	if err != nil {
		workersHealthy = false
		workersMessage = fmt.Sprintf("error checking stalled jobs: %v", err)
		allHealthy = false
	} else if stalledCount > 0 {
		workersHealthy = false
		workersMessage = fmt.Sprintf("%d stalled job(s) running > 30 min", stalledCount)
		allHealthy = false
	}
	components = append(components, storage.ComponentHealth{
		Name:    "workers",
		Healthy: workersHealthy,
		Message: workersMessage,
	})

	// Sync health check (if configured)
	if s.syncWorker != nil {
		syncHealthy, syncMessage := s.syncWorker.HealthCheck()
		if !syncHealthy {
			allHealthy = false
		}
		components = append(components, storage.ComponentHealth{
			Name:    "sync",
			Healthy: syncHealthy,
			Message: syncMessage,
		})
	}

	// Get recent errors
	var recentErrors []storage.ErrorEntry
	var errorCount24h int
	if s.errorLog != nil {
		for _, e := range s.errorLog.RecentN(10) {
			recentErrors = append(recentErrors, storage.ErrorEntry{
				Timestamp: e.Timestamp,
				Level:     e.Level,
				Component: e.Component,
				Message:   e.Message,
				JobID:     e.JobID,
			})
		}
		errorCount24h = s.errorLog.Count24h()
	}

	status := storage.HealthStatus{
		Healthy:      allHealthy,
		Uptime:       uptimeStr,
		Version:      version.Version,
		Components:   components,
		RecentErrors: recentErrors,
		ErrorCount:   errorCount24h,
	}

	writeJSON(w, http.StatusOK, status)
}

// formatDuration formats a duration in human-readable form (e.g., "2h 15m")
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
