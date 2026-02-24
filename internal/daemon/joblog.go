package daemon

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

// JobLogDir returns the directory for per-job log files.
func JobLogDir() string {
	return filepath.Join(config.DataDir(), "logs", "jobs")
}

// JobLogPath returns the log file path for a given job ID.
func JobLogPath(jobID int64) string {
	return filepath.Join(JobLogDir(), fmt.Sprintf("%d.log", jobID))
}

// openJobLog creates the log directory and opens a log file for the
// given job. Returns nil if the file cannot be created (logged, not
// fatal — the review still runs without disk logging).
func openJobLog(jobID int64) *os.File {
	dir := JobLogDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("Warning: cannot create job log dir %s: %v", dir, err)
		return nil
	}
	// Tighten pre-existing directories from older installs.
	if err := os.Chmod(dir, 0700); err != nil {
		log.Printf("Warning: cannot chmod job log dir: %v", err)
	}
	path := JobLogPath(jobID)
	f, err := os.OpenFile(
		path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600,
	)
	if err != nil {
		log.Printf("Warning: cannot create job log file for job %d: %v", jobID, err)
		return nil
	}
	// Tighten pre-existing files from older installs.
	if err := f.Chmod(0600); err != nil {
		log.Printf("Warning: cannot chmod job log file: %v", err)
	}
	return f
}

// CleanJobLogs removes log files older than maxAge and returns the
// number of files removed.
func CleanJobLogs(maxAge time.Duration) int {
	dir := JobLogDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if os.Remove(filepath.Join(dir, e.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}

// ReadJobLog returns the contents of the log file for a job, or an
// error if the file doesn't exist.
func ReadJobLog(jobID int64) ([]byte, error) {
	return os.ReadFile(JobLogPath(jobID))
}

// JobLogExists reports whether a log file exists for the given job.
func JobLogExists(jobID int64) bool {
	_, err := os.Stat(JobLogPath(jobID))
	return err == nil
}

// ParseJobIDFromLogName extracts the job ID from a log file name
// like "42.log". Returns 0, false if the name doesn't match.
func ParseJobIDFromLogName(name string) (int64, bool) {
	base, ok := strings.CutSuffix(name, ".log")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(base, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// safeWriter wraps an io.Writer and silently drops writes after the
// first error. This prevents a full-disk condition from killing the
// agent subprocess via a broken-pipe through io.MultiWriter.
type safeWriter struct {
	mu     sync.Mutex
	w      io.Writer
	failed bool
}

func (s *safeWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return len(p), nil
	}
	n, err := s.w.Write(p)
	if err != nil {
		s.failed = true
		log.Printf("Warning: job log write failed, disabling: %v", err)
		return len(p), nil
	}
	return n, nil
}
