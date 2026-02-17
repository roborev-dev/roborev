// ABOUTME: Compact job metadata handling for tracking source job IDs.
// ABOUTME: Used by worker to mark source jobs as addressed when compact jobs complete.

package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/roborev-dev/roborev/internal/config"
)

// CompactMetadata stores source job IDs for a compact job
type CompactMetadata struct {
	SourceJobIDs []int64 `json:"source_job_ids"`
}

// ReadCompactMetadata retrieves source job IDs for a compact job
func ReadCompactMetadata(jobID int64) (*CompactMetadata, error) {
	path := compactMetadataPath(jobID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read metadata file: %w", err)
	}

	var metadata CompactMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("parse metadata JSON: %w", err)
	}

	return &metadata, nil
}

// DeleteCompactMetadata removes the metadata file after processing
func DeleteCompactMetadata(jobID int64) error {
	path := compactMetadataPath(jobID)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete metadata file: %w", err)
	}
	return nil
}

// compactMetadataPath returns the file path for compact job metadata
func compactMetadataPath(jobID int64) string {
	return filepath.Join(config.DataDir(), fmt.Sprintf("compact-%d.json", jobID))
}

// IsValidCompactOutput checks whether compact agent output looks like
// a valid consolidated review (vs. an error or garbage).
func IsValidCompactOutput(output string) bool {
	output = strings.TrimSpace(output)
	if output == "" {
		return false
	}

	lower := strings.ToLower(output)

	// "All previous findings have been addressed" is the success case
	if strings.Contains(lower, "all previous findings have been addressed") {
		return true
	}

	// Reject obvious agent error patterns at line starts
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.ToLower(line))
		if strings.HasPrefix(trimmed, "error:") ||
			strings.HasPrefix(trimmed, "exception:") ||
			strings.HasPrefix(trimmed, "traceback") {
			return false
		}
	}

	// Require severity indicators AND structural markers
	hasSeverity := strings.Contains(lower, "severity") ||
		strings.Contains(lower, "critical") ||
		strings.Contains(lower, "high") ||
		strings.Contains(lower, "medium") ||
		strings.Contains(lower, "low")

	hasStructure := strings.Contains(output, "##") ||
		strings.Contains(output, "###") ||
		strings.Contains(lower, "verified") ||
		strings.Contains(lower, "findings") ||
		strings.Contains(lower, "issues")

	if hasSeverity && hasStructure {
		return true
	}

	// File references + structure is also acceptable
	hasFileRef := strings.Contains(output, ".go:") ||
		strings.Contains(output, ".py:") ||
		strings.Contains(output, ".js:") ||
		strings.Contains(output, ".ts:")

	return hasFileRef && hasStructure
}
