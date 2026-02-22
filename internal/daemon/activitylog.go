package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

// ActivityEntry represents a single activity log entry
type ActivityEntry struct {
	Timestamp time.Time         `json:"ts"`
	Event     string            `json:"event"`
	Component string            `json:"component"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
}

// ActivityLog manages activity logging to a JSONL file and maintains
// an in-memory ring buffer. Follows the same pattern as ErrorLog.
type ActivityLog struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	recent    []ActivityEntry
	maxRecent int
	writeIdx  int
	count     int
}

const activityLogCapacity = 500

// NewActivityLog creates a new activity log writer
func NewActivityLog(path string) (*ActivityLog, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(
		path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644,
	)
	if err != nil {
		return nil, err
	}

	return &ActivityLog{
		file:      file,
		path:      path,
		recent:    make([]ActivityEntry, activityLogCapacity),
		maxRecent: activityLogCapacity,
	}, nil
}

// DefaultActivityLogPath returns the default path for the activity log
func DefaultActivityLogPath() string {
	return filepath.Join(config.DataDir(), "activity.log")
}

// Log writes an activity entry to both file and in-memory buffer
func (a *ActivityLog) Log(
	event, component, message string,
	details map[string]string,
) {
	entry := ActivityEntry{
		Timestamp: time.Now(),
		Event:     event,
		Component: component,
		Message:   message,
		Details:   details,
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.file != nil {
		data, err := json.Marshal(entry)
		if err == nil {
			_, _ = a.file.Write(data)
			_, _ = a.file.Write([]byte("\n"))
		}
	}

	a.recent[a.writeIdx] = entry
	a.writeIdx = (a.writeIdx + 1) % a.maxRecent
	if a.count < a.maxRecent {
		a.count++
	}
}

// Recent returns all activity entries in the ring buffer (newest first)
func (a *ActivityLog) Recent() []ActivityEntry {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.count == 0 {
		return nil
	}

	result := make([]ActivityEntry, a.count)
	readIdx := (a.writeIdx - 1 + a.maxRecent) % a.maxRecent
	for i := range a.count {
		result[i] = a.recent[readIdx]
		readIdx = (readIdx - 1 + a.maxRecent) % a.maxRecent
	}
	return result
}

// RecentN returns up to n most recent entries (newest first)
func (a *ActivityLog) RecentN(n int) []ActivityEntry {
	all := a.Recent()
	if len(all) <= n {
		return all
	}
	return all[:n]
}

// Close closes the activity log file
func (a *ActivityLog) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.file != nil {
		err := a.file.Close()
		a.file = nil
		return err
	}
	return nil
}
