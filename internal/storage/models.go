package storage

import "time"

type Repo struct {
	ID        int64     `json:"id"`
	RootPath  string    `json:"root_path"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Identity  string    `json:"identity,omitempty"` // Unique identity for sync (git remote URL, .roborev-id, or local path)
}

type Commit struct {
	ID        int64     `json:"id"`
	RepoID    int64     `json:"repo_id"`
	SHA       string    `json:"sha"`
	Author    string    `json:"author"`
	Subject   string    `json:"subject"`
	Timestamp time.Time `json:"timestamp"`
	CreatedAt time.Time `json:"created_at"`
}

type JobStatus string

const (
	JobStatusQueued   JobStatus = "queued"
	JobStatusRunning  JobStatus = "running"
	JobStatusDone     JobStatus = "done"
	JobStatusFailed   JobStatus = "failed"
	JobStatusCanceled JobStatus = "canceled"
)

type ReviewJob struct {
	ID         int64      `json:"id"`
	RepoID     int64      `json:"repo_id"`
	CommitID   *int64     `json:"commit_id,omitempty"` // nil for ranges
	GitRef     string     `json:"git_ref"`             // SHA or "start..end" for ranges
	Branch     string     `json:"branch,omitempty"`    // Branch name at time of job creation
	Agent      string     `json:"agent"`
	Model      string     `json:"model,omitempty"`     // Model to use (for opencode: provider/model format)
	Reasoning  string     `json:"reasoning,omitempty"` // thorough, standard, fast (default: thorough)
	Status     JobStatus  `json:"status"`
	EnqueuedAt time.Time  `json:"enqueued_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	WorkerID   string     `json:"worker_id,omitempty"`
	Error      string     `json:"error,omitempty"`
	Prompt      string     `json:"prompt,omitempty"`
	RetryCount  int        `json:"retry_count"`
	DiffContent *string    `json:"diff_content,omitempty"` // For dirty reviews (uncommitted changes)
	Agentic     bool       `json:"agentic"`                // Enable agentic mode (allow file edits)

	// Sync fields
	UUID            string     `json:"uuid,omitempty"`              // Globally unique identifier for sync
	SourceMachineID string     `json:"source_machine_id,omitempty"` // Machine that created this job
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`        // Last modification time
	SyncedAt        *time.Time `json:"synced_at,omitempty"`         // Last sync time

	// Joined fields for convenience
	RepoPath      string  `json:"repo_path,omitempty"`
	RepoName      string  `json:"repo_name,omitempty"`
	CommitSubject string  `json:"commit_subject,omitempty"` // empty for ranges
	Addressed     *bool   `json:"addressed,omitempty"`      // nil if no review yet
	Verdict       *string `json:"verdict,omitempty"`        // P/F parsed from review output
}

type Review struct {
	ID        int64     `json:"id"`
	JobID     int64     `json:"job_id"`
	Agent     string    `json:"agent"`
	Prompt    string    `json:"prompt"`
	Output    string    `json:"output"`
	CreatedAt time.Time `json:"created_at"`
	Addressed bool      `json:"addressed"`

	// Sync fields
	UUID               string     `json:"uuid,omitempty"`                  // Globally unique identifier for sync
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`            // Last modification time
	UpdatedByMachineID string     `json:"updated_by_machine_id,omitempty"` // Machine that last modified this review
	SyncedAt           *time.Time `json:"synced_at,omitempty"`             // Last sync time

	// Joined fields
	Job *ReviewJob `json:"job,omitempty"`
}

type Response struct {
	ID        int64     `json:"id"`
	CommitID  *int64    `json:"commit_id,omitempty"` // For commit-based responses (legacy)
	JobID     *int64    `json:"job_id,omitempty"`    // For job/review-based responses
	Responder string    `json:"responder"`
	Response  string    `json:"response"`
	CreatedAt time.Time `json:"created_at"`

	// Sync fields
	UUID            string     `json:"uuid,omitempty"`              // Globally unique identifier for sync
	SourceMachineID string     `json:"source_machine_id,omitempty"` // Machine that created this response
	SyncedAt        *time.Time `json:"synced_at,omitempty"`         // Last sync time
}

type DaemonStatus struct {
	Version          string `json:"version"`
	QueuedJobs       int    `json:"queued_jobs"`
	RunningJobs      int    `json:"running_jobs"`
	CompletedJobs    int    `json:"completed_jobs"`
	FailedJobs       int    `json:"failed_jobs"`
	CanceledJobs     int    `json:"canceled_jobs"`
	ActiveWorkers    int    `json:"active_workers"`
	MaxWorkers       int    `json:"max_workers"`
	MachineID           string `json:"machine_id,omitempty"`            // Local machine ID for remote job detection
	ConfigReloadedAt    string `json:"config_reloaded_at,omitempty"`    // Last config reload timestamp (RFC3339Nano)
	ConfigReloadCounter uint64 `json:"config_reload_counter,omitempty"` // Monotonic reload counter (for sub-second detection)
}

// HealthStatus represents the overall daemon health
type HealthStatus struct {
	Healthy      bool              `json:"healthy"`
	Uptime       string            `json:"uptime"`
	Version      string            `json:"version"`
	Components   []ComponentHealth `json:"components"`
	RecentErrors []ErrorEntry      `json:"recent_errors"`
	ErrorCount   int               `json:"error_count_24h"`
}

// ComponentHealth represents the health of a single component
type ComponentHealth struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
}

// ErrorEntry represents a single error log entry (mirrors daemon.ErrorEntry for API)
type ErrorEntry struct {
	Timestamp time.Time `json:"ts"`
	Level     string    `json:"level"`
	Component string    `json:"component"`
	Message   string    `json:"message"`
	JobID     int64     `json:"job_id,omitempty"`
}
