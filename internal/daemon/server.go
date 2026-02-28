package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/githook"
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
	ciPoller      *CIPoller
	hookRunner    *HookRunner
	errorLog      *ErrorLog
	activityLog   *ActivityLog
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

	// Initialize activity log
	activityLog, err := NewActivityLog(DefaultActivityLogPath())
	if err != nil {
		log.Printf("Warning: failed to create activity log: %v", err)
	}

	// Create config watcher for hot-reloading
	configWatcher := NewConfigWatcher(configPath, cfg, broadcaster, activityLog)

	// Create hook runner to fire hooks on review events
	hookRunner := NewHookRunner(configWatcher, broadcaster, log.Default())

	s := &Server{
		db:            db,
		configWatcher: configWatcher,
		broadcaster:   broadcaster,
		workerPool:    NewWorkerPool(db, configWatcher, cfg.MaxWorkers, broadcaster, errorLog, activityLog),
		hookRunner:    hookRunner,
		errorLog:      errorLog,
		activityLog:   activityLog,
		startTime:     time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/enqueue", s.handleEnqueue)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/jobs", s.handleListJobs)
	mux.HandleFunc("/api/job/cancel", s.handleCancelJob)
	mux.HandleFunc("/api/job/output", s.handleJobOutput)
	mux.HandleFunc("/api/job/log", s.handleJobLog)
	mux.HandleFunc("/api/job/rerun", s.handleRerunJob)
	mux.HandleFunc("/api/job/update-branch", s.handleUpdateJobBranch)
	mux.HandleFunc("/api/repos", s.handleListRepos)
	mux.HandleFunc("/api/repos/register", s.handleRegisterRepo)
	mux.HandleFunc("/api/branches", s.handleListBranches)
	mux.HandleFunc("/api/review", s.handleGetReview)
	mux.HandleFunc("/api/review/address", s.handleAddressReview)
	mux.HandleFunc("/api/comment", s.handleAddComment)
	mux.HandleFunc("/api/comments", s.handleListComments)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stream/events", s.handleStreamEvents)
	mux.HandleFunc("/api/jobs/batch", s.handleBatchJobs)
	mux.HandleFunc("/api/remap", s.handleRemap)
	mux.HandleFunc("/api/sync/now", s.handleSyncNow)
	mux.HandleFunc("/api/sync/status", s.handleSyncStatus)
	mux.HandleFunc("/api/job/fix", s.handleFixJob)
	mux.HandleFunc("/api/job/patch", s.handleGetPatch)
	mux.HandleFunc("/api/job/applied", s.handleMarkJobApplied)
	mux.HandleFunc("/api/job/rebased", s.handleMarkJobRebased)
	mux.HandleFunc("/api/activity", s.handleActivity)

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
		if s.activityLog != nil {
			s.activityLog.Log(
				"daemon.zombie_cleanup", "server",
				fmt.Sprintf("cleaned up %d zombie daemon(s)", cleaned),
				map[string]string{"count": strconv.Itoa(cleaned)},
			)
		}
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

	// Log daemon start
	if s.activityLog != nil {
		binary, _ := os.Executable()
		s.activityLog.Log(
			"daemon.started", "server",
			fmt.Sprintf("daemon started on %s", addr),
			map[string]string{
				"version": version.Version,
				"binary":  binary,
				"addr":    addr,
				"pid":     strconv.Itoa(os.Getpid()),
				"workers": strconv.Itoa(cfg.MaxWorkers),
			},
		)
	}

	// Start worker pool
	s.workerPool.Start()

	// Check for outdated hooks in registered repos (skip in CI mode
	// where repos are fetch-only and don't need local hooks).
	if s.ciPoller == nil {
		if repos, err := s.db.ListRepos(); err == nil {
			for _, repo := range repos {
				if githook.NeedsUpgrade(repo.RootPath, "post-commit", githook.PostCommitVersionMarker) {
					log.Printf("Warning: outdated post-commit hook in %s -- run 'roborev init' to upgrade", repo.RootPath)
				}
				if githook.NeedsUpgrade(repo.RootPath, "post-rewrite", githook.PostRewriteVersionMarker) ||
					githook.Missing(repo.RootPath, "post-rewrite") {
					log.Printf("Warning: missing or outdated post-rewrite hook in %s -- run 'roborev init' to install", repo.RootPath)
				}
			}
		}
	}

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
	// Log daemon stop before shutting down components
	if s.activityLog != nil {
		uptime := time.Since(s.startTime)
		s.activityLog.Log(
			"daemon.stopped", "server",
			"daemon stopped",
			map[string]string{"uptime": formatDuration(uptime)},
		)
	}

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

	// Stop CI poller
	if s.ciPoller != nil {
		s.ciPoller.Stop()
	}

	// Stop worker pool
	s.workerPool.Stop()

	// Stop hook runner
	if s.hookRunner != nil {
		s.hookRunner.Stop()
	}

	// Close error log
	if s.errorLog != nil {
		s.errorLog.Close()
	}

	// Close activity log
	if s.activityLog != nil {
		s.activityLog.Close()
	}

	return nil
}

// Close shuts down the server and releases its resources.
// It is primarily provided for ease of use in test cleanup.
func (s *Server) Close() error {
	return s.Stop()
}

// ConfigWatcher returns the server's config watcher (for use by external components)
func (s *Server) ConfigWatcher() *ConfigWatcher {
	return s.configWatcher
}

// Broadcaster returns the server's event broadcaster (for use by external components)
func (s *Server) Broadcaster() Broadcaster {
	return s.broadcaster
}

// SetSyncWorker sets the sync worker for triggering manual syncs
func (s *Server) SetSyncWorker(sw *storage.SyncWorker) {
	s.syncWorker = sw
}

// SetCIPoller sets the CI poller for status reporting and wires up
// the worker pool cancellation callback so the poller can kill running
// processes when superseding stale batches.
func (s *Server) SetCIPoller(cp *CIPoller) {
	s.ciPoller = cp
	cp.jobCancelFn = func(jobID int64) {
		s.workerPool.CancelJob(jobID)
	}
}

// handleBatchJobs fetches jobs and their reviews in a single request for the given job IDs
func (s *Server) handleBatchJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		JobIDs []int64 `json:"job_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.JobIDs) == 0 {
		writeError(w, http.StatusBadRequest, "job_ids is required")
		return
	}

	const maxBatchSize = 100
	if len(req.JobIDs) > maxBatchSize {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("too many job IDs (max %d)", maxBatchSize))
		return
	}

	results, err := s.db.GetJobsWithReviewsByIDs(req.JobIDs)
	if err != nil {
		s.writeInternalError(w, fmt.Sprintf("batch fetch: %v", err))
		return
	}

	writeJSON(w, map[string]any{"results": results})
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

		stats, err := s.syncWorker.SyncNowWithProgress(func(p storage.SyncProgress) bool {
			if !writeNDJSON(w, map[string]any{
				"type":        "progress",
				"phase":       p.Phase,
				"batch":       p.BatchNum,
				"batch_jobs":  p.BatchJobs,
				"batch_revs":  p.BatchRevs,
				"batch_resps": p.BatchResps,
				"total_jobs":  p.TotalJobs,
				"total_revs":  p.TotalRevs,
				"total_resps": p.TotalResps,
			}) {
				return false
			}
			flusher.Flush()
			return true
		})

		if err != nil {
			if !writeNDJSON(w, map[string]any{
				"type":  "error",
				"error": err.Error(),
			}) {
				return
			}
			return
		}

		// Final result
		if !writeNDJSON(w, map[string]any{
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
		}) {
			return
		}
		return
	}

	// Non-streaming: wait for completion
	stats, err := s.syncWorker.SyncNow()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
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
		writeJSON(w, map[string]any{
			"enabled":   false,
			"connected": false,
			"message":   "sync not enabled",
		})
		return
	}

	healthy, message := s.syncWorker.HealthCheck()
	writeJSON(w, map[string]any{
		"enabled":   true,
		"connected": healthy,
		"message":   message,
	})
}

// API request/response types

type EnqueueRequest struct {
	RepoPath     string `json:"repo_path"`
	CommitSHA    string `json:"commit_sha,omitempty"` // Single commit (for backwards compat)
	GitRef       string `json:"git_ref,omitempty"`    // Single commit, range like "abc..def", or "dirty"
	Branch       string `json:"branch,omitempty"`     // Branch name at time of job creation
	Agent        string `json:"agent,omitempty"`
	Model        string `json:"model,omitempty"`         // Model to use (for opencode: provider/model format)
	DiffContent  string `json:"diff_content,omitempty"`  // Pre-captured diff for dirty reviews
	Reasoning    string `json:"reasoning,omitempty"`     // Reasoning level: thorough, standard, fast
	ReviewType   string `json:"review_type,omitempty"`   // Review type (e.g., "security") — changes system prompt
	CustomPrompt string `json:"custom_prompt,omitempty"` // Custom prompt for ad-hoc agent work
	Agentic      bool   `json:"agentic,omitempty"`       // Enable agentic mode (allow file edits)
	OutputPrefix string `json:"output_prefix,omitempty"` // Prefix to prepend to review output
	JobType      string `json:"job_type,omitempty"`      // Explicit job type (review/range/dirty/task/compact)
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONWithStatus(w, http.StatusOK, v)
}

func writeCreatedJSON(w http.ResponseWriter, v any) {
	writeJSONWithStatus(w, http.StatusCreated, v)
}

func writeJSONWithStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSONWithStatus(w, status, ErrorResponse{Error: msg})
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

	// Limit request body size to prevent DoS via large payloads.
	// Derive from configured max prompt size + 50KB overhead for JSON envelope.
	maxPromptSize := config.DefaultMaxPromptSize
	if cfg := s.configWatcher.Config(); cfg != nil && cfg.DefaultMaxPromptSize > 0 {
		maxPromptSize = cfg.DefaultMaxPromptSize
	}
	maxBodySize := int64(maxPromptSize) + 50*1024
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	var req EnqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Use errors.As for reliable detection of MaxBytesReader errors
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body too large (max %dKB)", maxBodySize/1024))
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

	// Validate and normalize review_type (empty means "default")
	if req.ReviewType == "" {
		req.ReviewType = config.ReviewTypeDefault
	}
	canonical, err := config.ValidateReviewTypes(
		[]string{req.ReviewType})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.ReviewType = canonical[0]

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
		writeJSON(w, map[string]any{
			"skipped": true,
			"reason":  fmt.Sprintf("branch %q is excluded from reviews", currentBranch),
		})
		return
	}

	// Fall back to detected branch when client didn't send one
	if req.Branch == "" {
		req.Branch = currentBranch
	}

	// Resolve repo identity for sync
	repoIdentity := config.ResolveRepoIdentity(repoRoot, nil)

	// Get or create repo with identity
	repo, err := s.db.GetOrCreateRepo(repoRoot, repoIdentity)
	if err != nil {
		s.writeInternalError(w, fmt.Sprintf("get repo: %v", err))
		return
	}

	// Resolve reasoning level first (needed for agent/model resolution)
	reasoning, err := config.ResolveReviewReasoning(req.Reasoning, repoRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Map review_type to config workflow for agent/model resolution.
	// "default" uses the standard "review" workflow; others use their own name.
	workflow := "review"
	if !config.IsDefaultReviewType(req.ReviewType) {
		workflow = req.ReviewType
	}

	// Resolve agent for workflow at this reasoning level
	agentName := config.ResolveAgentForWorkflow(req.Agent, repoRoot, s.configWatcher.Config(), workflow, reasoning)

	// Resolve to an installed agent: if the configured agent isn't available,
	// fall back through the chain (codex -> claude-code -> gemini -> ...).
	// Fail fast with 503 if nothing is installed at all.
	if resolved, err := agent.GetAvailable(agentName); err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("no review agent available: %v", err))
		return
	} else {
		agentName = resolved.Name()
	}

	// Resolve model for workflow at this reasoning level
	model := config.ResolveModelForWorkflow(req.Model, repoRoot, s.configWatcher.Config(), workflow, reasoning)

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
		job, err = s.db.EnqueueJob(storage.EnqueueOpts{
			RepoID:       repo.ID,
			Branch:       req.Branch,
			Agent:        agentName,
			Model:        model,
			Reasoning:    reasoning,
			ReviewType:   req.ReviewType,
			Prompt:       req.CustomPrompt,
			OutputPrefix: req.OutputPrefix,
			Agentic:      req.Agentic,
			Label:        gitRef, // Use git_ref as TUI label (run, analyze type, custom)
			JobType:      req.JobType,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("enqueue prompt job: %v", err))
			return
		}
	} else if isDirty {
		// Dirty review - use pre-captured diff
		job, err = s.db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      repo.ID,
			GitRef:      gitRef,
			Branch:      req.Branch,
			Agent:       agentName,
			Model:       model,
			Reasoning:   reasoning,
			ReviewType:  req.ReviewType,
			DiffContent: req.DiffContent,
		})
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
			// If the start ref is <sha>^ and resolution failed, the commit
			// may be the root commit (no parent). Use the empty tree SHA so
			// the range includes the root commit's changes.
			if before, ok := strings.CutSuffix(parts[0], "^"); ok {
				base := before
				if _, resolveErr := git.ResolveSHA(gitCwd, base+"^{commit}"); resolveErr == nil {
					startSHA = git.EmptyTreeSHA
					err = nil
				}
			}
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid start commit: %v", err))
				return
			}
		}
		endSHA, err := git.ResolveSHA(gitCwd, parts[1])
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid end commit: %v", err))
			return
		}

		// Store as full SHA range
		fullRef := startSHA + ".." + endSHA
		job, err = s.db.EnqueueJob(storage.EnqueueOpts{
			RepoID:     repo.ID,
			GitRef:     fullRef,
			Branch:     req.Branch,
			Agent:      agentName,
			Model:      model,
			Reasoning:  reasoning,
			ReviewType: req.ReviewType,
		})
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

		patchID := git.GetPatchID(gitCwd, sha)

		job, err = s.db.EnqueueJob(storage.EnqueueOpts{
			RepoID:     repo.ID,
			CommitID:   commit.ID,
			GitRef:     sha,
			Branch:     req.Branch,
			Agent:      agentName,
			Model:      model,
			Reasoning:  reasoning,
			ReviewType: req.ReviewType,
			PatchID:    patchID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("enqueue job: %v", err))
			return
		}
		job.CommitSubject = commit.Subject
	}

	// Fill in joined fields
	job.RepoPath = repo.RootPath
	job.RepoName = repo.Name

	// Log the enqueue activity
	if s.activityLog != nil {
		s.activityLog.Log(
			"job.enqueued", "server",
			fmt.Sprintf("job %d enqueued for %s", job.ID, job.GitRef),
			map[string]string{
				"job_id":      strconv.FormatInt(job.ID, 10),
				"repo":        repo.Name,
				"ref":         gitRef,
				"agent":       agentName,
				"review_type": req.ReviewType,
			},
		)
	}

	writeCreatedJSON(w, job)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Support fetching a single job by ID
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		jobID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id parameter")
			return
		}
		job, err := s.db.GetJobByID(jobID)
		if err != nil {
			// Distinguish "not found" from actual DB errors
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
				return
			}
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("database error: %v", err))
			return
		}
		job.Patch = nil // Patch is only served via /api/job/patch
		writeJSON(w, map[string]any{
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

	var listOpts []storage.ListJobsOption
	if gitRef != "" {
		listOpts = append(listOpts, storage.WithGitRef(gitRef))
	}
	if branch := r.URL.Query().Get("branch"); branch != "" {
		if r.URL.Query().Get("branch_include_empty") == "true" {
			listOpts = append(listOpts, storage.WithBranchOrEmpty(branch))
		} else {
			listOpts = append(listOpts, storage.WithBranch(branch))
		}
	}
	if addrStr := r.URL.Query().Get("addressed"); addrStr == "true" || addrStr == "false" {
		listOpts = append(listOpts, storage.WithAddressed(addrStr == "true"))
	}
	if jobType := r.URL.Query().Get("job_type"); jobType != "" {
		listOpts = append(listOpts, storage.WithJobType(jobType))
	}
	if exJobType := r.URL.Query().Get("exclude_job_type"); exJobType != "" {
		listOpts = append(listOpts, storage.WithExcludeJobType(exJobType))
	}

	jobs, err := s.db.ListJobs(status, repo, fetchLimit, offset, listOpts...)
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

	// Compute aggregate stats using same repo/branch filters (ignoring addressed filter and pagination)
	var statsOpts []storage.ListJobsOption
	if branch := r.URL.Query().Get("branch"); branch != "" {
		if r.URL.Query().Get("branch_include_empty") == "true" {
			statsOpts = append(statsOpts, storage.WithBranchOrEmpty(branch))
		} else {
			statsOpts = append(statsOpts, storage.WithBranch(branch))
		}
	}
	stats, statsErr := s.db.CountJobStats(repo, statsOpts...)
	if statsErr != nil {
		log.Printf("Warning: failed to count job stats: %v", statsErr)
	}

	writeJSON(w, map[string]any{
		"jobs":     jobs,
		"has_more": hasMore,
		"stats":    stats,
	})
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Optional branch filter
	branch := r.URL.Query().Get("branch")

	var repos []storage.RepoWithCount
	var totalCount int
	var err error

	if branch != "" {
		repos, totalCount, err = s.db.ListReposWithReviewCountsByBranch(branch)
	} else {
		repos, totalCount, err = s.db.ListReposWithReviewCounts()
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list repos: %v", err))
		return
	}

	writeJSON(w, map[string]any{
		"repos":       repos,
		"total_count": totalCount,
	})
}

func (s *Server) handleRegisterRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		RepoPath string `json:"repo_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.RepoPath == "" {
		writeError(w, http.StatusBadRequest, "repo_path is required")
		return
	}

	// Resolve to main repo root (handles worktrees)
	repoRoot, err := git.GetMainRepoRoot(req.RepoPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("not a git repository: %v", err))
		return
	}

	// Resolve repo identity for sync
	repoIdentity := config.ResolveRepoIdentity(repoRoot, nil)

	// Persist (idempotent — UNIQUE on root_path)
	repo, err := s.db.GetOrCreateRepo(repoRoot, repoIdentity)
	if err != nil {
		s.writeInternalError(w, fmt.Sprintf("register repo: %v", err))
		return
	}

	writeJSON(w, repo)
}

func (s *Server) handleListBranches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Optional repo filter (by path) - supports multiple values
	// Filter out empty strings to treat ?repo= as no filter
	var repoPaths []string
	for _, p := range r.URL.Query()["repo"] {
		if p != "" {
			repoPaths = append(repoPaths, p)
		}
	}

	result, err := s.db.ListBranchesWithCounts(repoPaths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list branches: %v", err))
		return
	}

	writeJSON(w, map[string]any{
		"branches":        result.Branches,
		"total_count":     result.TotalCount,
		"nulls_remaining": result.NullsRemaining,
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

	writeJSON(w, map[string]any{"success": true})
}

// JobOutputResponse is the response for /api/job/output
type JobOutputResponse struct {
	JobID   int64        `json:"job_id"`
	Status  string       `json:"status"`
	Lines   []OutputLine `json:"lines"`
	HasMore bool         `json:"has_more"`
}

func (s *Server) handleJobOutput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	jobIDStr := r.URL.Query().Get("job_id")
	if jobIDStr == "" {
		writeError(w, http.StatusBadRequest, "job_id required")
		return
	}

	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job_id")
		return
	}

	// Check job exists
	job, err := s.db.GetJobByID(jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	// Check if streaming mode requested
	stream := r.URL.Query().Get("stream") == "1"

	if !stream {
		// Return current buffer (polling mode)
		lines := s.workerPool.GetJobOutput(jobID)
		if lines == nil {
			lines = []OutputLine{}
		}

		resp := JobOutputResponse{
			JobID:   jobID,
			Status:  string(job.Status),
			Lines:   lines,
			HasMore: job.Status == storage.JobStatusRunning,
		}
		writeJSON(w, resp)
		return
	}

	// Streaming mode via SSE
	// Don't stream for non-running jobs - they have no active buffer producer
	// and would hang forever waiting for data
	if job.Status != storage.JobStatusRunning {
		w.Header().Set("Content-Type", "application/x-ndjson")
		if !writeNDJSON(w, map[string]any{
			"type":   "complete",
			"status": string(job.Status),
		}) {
			return
		}
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Subscribe to output
	initial, ch, cancel := s.workerPool.SubscribeJobOutput(jobID)
	defer cancel()

	// Send initial lines
	for _, line := range initial {
		if !writeNDJSON(w, map[string]any{
			"type":      "line",
			"ts":        line.Timestamp.Format(time.RFC3339Nano),
			"text":      line.Text,
			"line_type": line.Type,
		}) {
			return
		}
	}
	flusher.Flush()

	// Stream new lines until job completes or client disconnects
	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				// Job finished - channel closed, fetch actual status
				finalStatus := "done"
				if finalJob, err := s.db.GetJobByID(jobID); err == nil {
					finalStatus = string(finalJob.Status)
				}
				if !writeNDJSON(w, map[string]any{
					"type":   "complete",
					"status": finalStatus,
				}) {
					return
				}
				flusher.Flush()
				return
			}
			if !writeNDJSON(w, map[string]any{
				"type":      "line",
				"ts":        line.Timestamp.Format(time.RFC3339Nano),
				"text":      line.Text,
				"line_type": line.Type,
			}) {
				return
			}
			flusher.Flush()
		}
	}
}

// handleJobLog serves the raw JSONL log file for a job.
// The TUI and CLI use this to render formatted agent output.
//
// Supports incremental fetching via the "offset" query param
// (byte offset into the log file). The response includes an
// X-Log-Offset header with the end position — the client passes
// this as offset on the next poll to get only new content.
//
// For running jobs, the end position is snapped to the last
// newline boundary to avoid serving partial JSONL lines.
func (s *Server) handleJobLog(
	w http.ResponseWriter, r *http.Request,
) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	jobIDStr := r.URL.Query().Get("job_id")
	if jobIDStr == "" {
		writeError(w, http.StatusBadRequest, "job_id required")
		return
	}

	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job_id")
		return
	}

	var offset int64
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.ParseInt(v, 10, 64)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset")
			return
		}
	}

	job, err := s.db.GetJobByID(jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	f, err := os.Open(JobLogPath(jobID))
	if err != nil {
		// Running job with no log file yet (startup race):
		// return empty 200 so the TUI keeps polling.
		if errors.Is(err, os.ErrNotExist) &&
			job.Status == storage.JobStatusRunning {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("X-Job-Status", string(job.Status))
			w.Header().Set("X-Log-Offset", "0")
			return
		}
		writeError(w, http.StatusNotFound, "no log file for this job")
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		writeError(
			w, http.StatusInternalServerError,
			"stat log file",
		)
		return
	}
	fileSize := fi.Size()

	// Clamp offset if beyond file size (truncated/rotated log).
	if offset > fileSize {
		offset = 0
	}

	endPos := fileSize
	if job.Status == storage.JobStatusRunning {
		endPos = jobLogSafeEnd(f, fileSize)
	}

	// Clamp offset to endPos.
	if offset > endPos {
		offset = endPos
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		writeError(
			w, http.StatusInternalServerError,
			"seek log file",
		)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Job-Status", string(job.Status))
	w.Header().Set(
		"X-Log-Offset", strconv.FormatInt(endPos, 10),
	)

	n := endPos - offset
	if n > 0 {
		if _, err := io.CopyN(w, f, n); err != nil {
			log.Printf(
				"handleJobLog: write error for job %d: %v",
				jobID, err,
			)
		}
	}
}

// jobLogSafeEnd returns the byte position of the last complete
// JSONL line in the file (i.e. up to and including the last '\n').
// For completed jobs this equals fileSize; for running jobs it
// avoids serving a partial line being written concurrently.
func jobLogSafeEnd(f *os.File, fileSize int64) int64 {
	if fileSize == 0 {
		return 0
	}

	// Check if last byte is newline — common case.
	var last [1]byte
	if _, err := f.ReadAt(last[:], fileSize-1); err != nil {
		return fileSize
	}
	if last[0] == '\n' {
		return fileSize
	}

	// Scan backwards in 64KB chunks to find last newline.
	const chunkSize = 64 * 1024
	buf := make([]byte, chunkSize)
	pos := fileSize
	for pos > 0 {
		readStart := max(pos-chunkSize, 0)
		readLen := pos - readStart
		n, err := f.ReadAt(buf[:readLen], readStart)
		if err != nil && err != io.EOF {
			return fileSize
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				return readStart + int64(i) + 1
			}
		}
		pos = readStart
	}

	// Entire file has no newline — serve nothing to avoid
	// a partial line.
	return 0
}

func writeNDJSON(w http.ResponseWriter, v any) bool {
	line, err := json.Marshal(v)
	if err != nil {
		log.Printf("failed to marshal NDJSON response: %v", err)
		return false
	}
	if _, err := w.Write(line); err != nil {
		return false
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		return false
	}
	return true
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

	writeJSON(w, map[string]any{"success": true})
}

func (s *Server) handleUpdateJobBranch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		JobID  int64  `json:"job_id"`
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.JobID == 0 {
		writeError(w, http.StatusBadRequest, "job_id is required")
		return
	}
	if req.Branch == "" {
		writeError(w, http.StatusBadRequest, "branch is required")
		return
	}

	rowsAffected, err := s.db.UpdateJobBranch(req.JobID, req.Branch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("update branch: %v", err))
		return
	}

	writeJSON(w, map[string]any{
		"success": true,
		"updated": rowsAffected > 0,
	})
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
		jobID, parseErr := strconv.ParseInt(jobIDStr, 10, 64)
		if parseErr != nil {
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

	writeJSON(w, review)
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

	writeCreatedJSON(w, resp)
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
		jobID, parseErr := strconv.ParseInt(jobIDStr, 10, 64)
		if parseErr != nil {
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

	writeJSON(w, map[string]any{"responses": responses})
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

	queued, running, done, failed, canceled, applied, rebased, err := s.db.GetJobCounts()
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
		AppliedJobs:         applied,
		RebasedJobs:         rebased,
		ActiveWorkers:       s.workerPool.ActiveWorkers(),
		MaxWorkers:          s.workerPool.MaxWorkers(),
		MachineID:           s.getMachineID(),
		ConfigReloadedAt:    configReloadedAt,
		ConfigReloadCounter: configReloadCounter,
	}

	writeJSON(w, status)
}

type AddressReviewRequest struct {
	JobID     int64 `json:"job_id"`
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

	if req.JobID == 0 {
		writeError(w, http.StatusBadRequest, "job_id is required")
		return
	}

	if err := s.db.MarkReviewAddressedByJobID(req.JobID, req.Addressed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "review not found for job")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("mark addressed: %v", err))
		return
	}

	writeJSON(w, map[string]any{"success": true})
}

// RemapRequest is the request body for POST /api/remap.
type RemapRequest struct {
	RepoPath string         `json:"repo_path"`
	Mappings []RemapMapping `json:"mappings"`
}

// RemapMapping maps a pre-rewrite SHA to its post-rewrite replacement.
type RemapMapping struct {
	OldSHA    string `json:"old_sha"`
	NewSHA    string `json:"new_sha"`
	PatchID   string `json:"patch_id"`
	Author    string `json:"author"`
	Subject   string `json:"subject"`
	Timestamp string `json:"timestamp"` // RFC3339
}

func (s *Server) handleRemap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Cap body size: ~200 bytes per mapping, 1000 max → 1MB is generous.
	const maxRemapBody = 1 << 20 // 1MB
	r.Body = http.MaxBytesReader(w, r.Body, maxRemapBody)

	var req RemapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge,
				"request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	const maxMappings = 1000
	if len(req.Mappings) > maxMappings {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("too many mappings (%d, max %d)",
				len(req.Mappings), maxMappings))
		return
	}

	if req.RepoPath == "" {
		writeError(w, http.StatusBadRequest, "repo_path is required")
		return
	}

	repoRoot, err := git.GetMainRepoRoot(req.RepoPath)
	if err != nil {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("not a git repository: %s", req.RepoPath))
		return
	}

	repo, err := s.db.GetRepoByPath(repoRoot)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound,
			fmt.Sprintf("unknown repo: %s", repoRoot))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Sprintf("lookup repo: %v", err))
		return
	}

	// Validate all timestamps upfront before modifying any state.
	timestamps := make([]time.Time, len(req.Mappings))
	for i, m := range req.Mappings {
		ts, err := time.Parse(time.RFC3339, m.Timestamp)
		if err != nil {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("invalid timestamp %q: %v", m.Timestamp, err))
			return
		}
		timestamps[i] = ts
	}

	var remapped, skipped int
	for i, m := range req.Mappings {
		n, err := s.db.RemapJob(
			repo.ID, m.OldSHA, m.NewSHA, m.PatchID,
			m.Author, m.Subject, timestamps[i],
		)
		if err != nil {
			skipped++
			continue
		}
		remapped += n
		if n == 0 {
			skipped++
		}
	}

	if remapped > 0 {
		s.broadcaster.Broadcast(Event{
			Type: "review.remapped",
			TS:   time.Now(),
			Repo: repo.RootPath,
		})
	}

	writeJSON(w, map[string]int{
		"remapped": remapped, "skipped": skipped,
	})
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

	writeJSON(w, status)
}

// handleFixJob creates a background fix job for a review.
// It fetches the parent review, builds a fix prompt, and enqueues a new
// fix job that will run in an isolated worktree.
func (s *Server) handleFixJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 50<<20) // 50MB limit
	var req struct {
		ParentJobID int64  `json:"parent_job_id"`
		Prompt      string `json:"prompt,omitempty"`       // Optional custom prompt override
		GitRef      string `json:"git_ref,omitempty"`      // Optional: explicit ref for worktree (user-confirmed for compact jobs)
		StaleJobID  int64  `json:"stale_job_id,omitempty"` // Optional: server looks up patch from this job for rebase
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ParentJobID == 0 {
		writeError(w, http.StatusBadRequest, "parent_job_id is required")
		return
	}

	// Fetch the parent job — must be a review (not a fix job)
	parentJob, err := s.db.GetJobByID(req.ParentJobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "parent job not found")
		return
	}
	if parentJob.IsFixJob() {
		writeError(w, http.StatusBadRequest, "parent job must be a review, not a fix job")
		return
	}

	// Build the fix prompt
	fixPrompt := ""
	if req.StaleJobID > 0 {
		// Server-side rebase: look up stale patch from DB and build rebase prompt
		staleJob, err := s.db.GetJobByID(req.StaleJobID)
		if err != nil {
			writeError(w, http.StatusNotFound, "stale job not found")
			return
		}
		if staleJob.JobType != storage.JobTypeFix {
			writeError(w, http.StatusBadRequest, "stale job is not a fix job")
			return
		}
		if staleJob.RepoID != parentJob.RepoID {
			writeError(w, http.StatusBadRequest, "stale job belongs to a different repo")
			return
		}
		// Verify stale job is linked to the same parent
		if staleJob.ParentJobID == nil || *staleJob.ParentJobID != req.ParentJobID {
			writeError(w, http.StatusBadRequest, "stale job is not linked to the specified parent")
			return
		}
		// Require terminal status with a usable patch
		switch staleJob.Status {
		case storage.JobStatusDone, storage.JobStatusApplied, storage.JobStatusRebased:
			// OK
		default:
			writeError(w, http.StatusBadRequest, "stale job is not in a terminal state")
			return
		}
		if staleJob.Patch == nil || *staleJob.Patch == "" {
			writeError(w, http.StatusBadRequest, "stale job has no patch to rebase from")
			return
		}
		fixPrompt = buildRebasePrompt(staleJob.Patch)
	}
	if fixPrompt == "" {
		// Fetch the review output for the parent job
		review, err := s.db.GetReviewByJobID(req.ParentJobID)
		if err != nil || review == nil {
			writeError(w, http.StatusBadRequest, "parent job has no review to fix")
			return
		}
		if req.Prompt != "" {
			fixPrompt = buildFixPromptWithInstructions(review.Output, req.Prompt)
		} else {
			fixPrompt = buildFixPrompt(review.Output)
		}
	}

	// Resolve agent for fix workflow
	cfg := s.configWatcher.Config()
	reasoning := "standard"
	agentName := config.ResolveAgentForWorkflow("", parentJob.RepoPath, cfg, "fix", reasoning)
	if resolved, err := agent.GetAvailable(agentName); err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("no agent available: %v", err))
		return
	} else {
		agentName = resolved.Name()
	}
	model := config.ResolveModelForWorkflow("", parentJob.RepoPath, cfg, "fix", reasoning)

	// Normalize and validate user-provided git ref to prevent option
	// injection (e.g. " --something") when passed to git worktree add.
	req.GitRef = strings.TrimSpace(req.GitRef)
	if req.GitRef != "" && !isValidGitRef(req.GitRef) {
		writeError(w, http.StatusBadRequest, "invalid git_ref")
		return
	}

	// Resolve the git ref for the fix worktree.
	// Range refs (sha..sha) and empty refs (compact jobs) are not valid for
	// git worktree add, so fall back to branch then "HEAD".
	// An explicit git_ref from the request (user-confirmed via TUI) takes precedence.
	fixGitRef := req.GitRef
	if fixGitRef == "" && !strings.Contains(parentJob.GitRef, "..") {
		fixGitRef = parentJob.GitRef
	}
	if fixGitRef == "" {
		fixGitRef = parentJob.Branch
	}
	if fixGitRef == "" {
		fixGitRef = "HEAD"
		log.Printf("fix job for parent %d: no git ref or branch available, falling back to HEAD", req.ParentJobID)
	}

	// Carry over commit linkage when the parent is commit-based so
	// fix jobs retain commit metadata (e.g. subject) in list views.
	var commitID int64
	if parentJob.CommitID != nil {
		commitID = *parentJob.CommitID
	}

	// Enqueue the fix job
	job, err := s.db.EnqueueJob(storage.EnqueueOpts{
		RepoID:      parentJob.RepoID,
		CommitID:    commitID,
		GitRef:      fixGitRef,
		Branch:      parentJob.Branch,
		Agent:       agentName,
		Model:       model,
		Reasoning:   reasoning,
		Prompt:      fixPrompt,
		Agentic:     true,
		Label:       fmt.Sprintf("fix #%d", req.ParentJobID),
		JobType:     storage.JobTypeFix,
		ParentJobID: req.ParentJobID,
	})
	if err != nil {
		s.writeInternalError(w, fmt.Sprintf("enqueue fix job: %v", err))
		return
	}
	if commitID > 0 {
		job.CommitSubject = parentJob.CommitSubject
	}

	writeJSONWithStatus(w, http.StatusCreated, job)
}

// isValidGitRef checks that a user-provided git ref is safe to pass
// to git commands. Rejects empty strings, leading dashes (option
// injection), and control characters.
func isValidGitRef(ref string) bool {
	if ref == "" || ref[0] == '-' {
		return false
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// handleGetPatch returns the stored patch for a completed fix job.
func (s *Server) handleGetPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	jobIDStr := r.URL.Query().Get("job_id")
	if jobIDStr == "" {
		writeError(w, http.StatusBadRequest, "job_id parameter required")
		return
	}

	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job_id")
		return
	}

	job, err := s.db.GetJobByID(jobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if !job.HasViewableOutput() || job.Patch == nil {
		writeError(w, http.StatusNotFound, "no patch available for this job")
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(*job.Patch))
}

func (s *Server) handleMarkJobApplied(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		JobID int64 `json:"job_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.JobID == 0 {
		writeError(w, http.StatusBadRequest, "job_id is required")
		return
	}

	if err := s.db.MarkJobApplied(req.JobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "job not found or not in done state")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("mark applied: %v", err))
		return
	}

	writeJSON(w, map[string]string{"status": "applied"})
}

func (s *Server) handleMarkJobRebased(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		JobID int64 `json:"job_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.JobID == 0 {
		writeError(w, http.StatusBadRequest, "job_id is required")
		return
	}

	if err := s.db.MarkJobRebased(req.JobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "job not found or not in done state")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("mark rebased: %v", err))
		return
	}

	writeJSON(w, map[string]string{"status": "rebased"})
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if s.activityLog == nil {
		writeJSON(w, map[string]any{"entries": []ActivityEntry{}})
		return
	}

	// Parse optional limit (default 50, max 500)
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > activityLogCapacity {
		limit = activityLogCapacity
	}

	entries := s.activityLog.RecentN(limit)
	if entries == nil {
		entries = []ActivityEntry{}
	}
	writeJSON(w, map[string]any{"entries": entries})
}

// buildFixPrompt constructs a prompt for fixing review findings.
func buildFixPrompt(reviewOutput string) string {
	return buildFixPromptWithInstructions(reviewOutput, "")
}

// buildFixPromptWithInstructions constructs a fix prompt that includes the review
// findings and optional user-provided instructions.
func buildFixPromptWithInstructions(reviewOutput, userInstructions string) string {
	prompt := "# Fix Request\n\n" +
		"An analysis was performed and produced the following findings:\n\n" +
		"## Analysis Findings\n\n" +
		reviewOutput + "\n\n"
	if userInstructions != "" {
		prompt += "## Additional Instructions\n\n" +
			userInstructions + "\n\n"
	}
	prompt += "## Instructions\n\n" +
		"Please apply the suggested changes from the analysis above. " +
		"Make the necessary edits to address each finding. " +
		"Focus on the highest priority items first.\n\n" +
		"After making changes:\n" +
		"1. Verify the code still compiles/passes linting\n" +
		"2. Run any relevant tests to ensure nothing is broken\n" +
		"3. Stage the changes with git add but do NOT commit — the changes will be captured as a patch\n"
	return prompt
}

// buildRebasePrompt constructs a prompt for re-applying a stale patch to current HEAD.
func buildRebasePrompt(stalePatch *string) string {
	prompt := "# Rebase Fix Request\n\n" +
		"A previous fix attempt produced a patch that no longer applies cleanly to the current HEAD.\n" +
		"Your task is to achieve the same changes but adapted to the current state of the code.\n\n"
	if stalePatch != nil && *stalePatch != "" {
		prompt += "## Previous Patch (stale)\n\n`````diff\n" + *stalePatch + "\n`````\n\n"
	}
	prompt += "## Instructions\n\n" +
		"1. Review the intent of the previous patch\n" +
		"2. Apply equivalent changes to the current codebase\n" +
		"3. Resolve any conflicts with recent changes\n" +
		"4. Verify the code compiles and tests pass\n" +
		"5. Stage the changes with git add but do NOT commit\n"
	return prompt
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
