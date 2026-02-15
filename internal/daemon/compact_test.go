// ABOUTME: Unit tests for compact job metadata handling
// ABOUTME: Tests reading, deleting, and validating compact metadata files
package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/roborev-dev/roborev/internal/config"
)

func TestReadCompactMetadata(t *testing.T) {
	// Save original env and restore after test
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

	tmpDir := t.TempDir()
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)

	t.Run("valid_metadata", func(t *testing.T) {
		jobID := int64(123)
		sourceIDs := []int64{100, 200, 300}

		// Write valid metadata
		metadata := CompactMetadata{SourceJobIDs: sourceIDs}
		data, err := json.Marshal(metadata)
		if err != nil {
			t.Fatalf("Failed to marshal metadata: %v", err)
		}

		path := filepath.Join(tmpDir, "compact-123.json")
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("Failed to write metadata file: %v", err)
		}

		// Read it back
		got, err := ReadCompactMetadata(jobID)
		if err != nil {
			t.Fatalf("ReadCompactMetadata failed: %v", err)
		}

		if len(got.SourceJobIDs) != len(sourceIDs) {
			t.Errorf("Expected %d source job IDs, got %d", len(sourceIDs), len(got.SourceJobIDs))
		}

		for i, id := range got.SourceJobIDs {
			if id != sourceIDs[i] {
				t.Errorf("SourceJobIDs[%d] = %d, want %d", i, id, sourceIDs[i])
			}
		}
	})

	t.Run("missing_file", func(t *testing.T) {
		jobID := int64(999)

		// Try to read non-existent file
		_, err := ReadCompactMetadata(jobID)
		if err == nil {
			t.Error("Expected error for missing file, got nil")
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		jobID := int64(456)

		// Write invalid JSON
		path := filepath.Join(tmpDir, "compact-456.json")
		if err := os.WriteFile(path, []byte("{invalid json}"), 0644); err != nil {
			t.Fatalf("Failed to write invalid JSON: %v", err)
		}

		// Try to read it
		_, err := ReadCompactMetadata(jobID)
		if err == nil {
			t.Error("Expected error for invalid JSON, got nil")
		}
	})

	t.Run("empty_source_ids", func(t *testing.T) {
		jobID := int64(789)

		// Write metadata with empty source IDs
		metadata := CompactMetadata{SourceJobIDs: []int64{}}
		data, err := json.Marshal(metadata)
		if err != nil {
			t.Fatalf("Failed to marshal metadata: %v", err)
		}

		path := filepath.Join(tmpDir, "compact-789.json")
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("Failed to write metadata file: %v", err)
		}

		// Read it back
		got, err := ReadCompactMetadata(jobID)
		if err != nil {
			t.Fatalf("ReadCompactMetadata failed: %v", err)
		}

		if len(got.SourceJobIDs) != 0 {
			t.Errorf("Expected 0 source job IDs, got %d", len(got.SourceJobIDs))
		}
	})
}

func TestDeleteCompactMetadata(t *testing.T) {
	// Save original env and restore after test
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

	tmpDir := t.TempDir()
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)

	t.Run("delete_existing_file", func(t *testing.T) {
		jobID := int64(123)

		// Create a metadata file
		path := filepath.Join(tmpDir, "compact-123.json")
		if err := os.WriteFile(path, []byte(`{"source_job_ids":[1,2,3]}`), 0644); err != nil {
			t.Fatalf("Failed to write metadata file: %v", err)
		}

		// Delete it
		err := DeleteCompactMetadata(jobID)
		if err != nil {
			t.Errorf("DeleteCompactMetadata failed: %v", err)
		}

		// Verify it's gone
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("Metadata file should be deleted")
		}
	})

	t.Run("delete_nonexistent_file", func(t *testing.T) {
		jobID := int64(999)

		// Try to delete non-existent file (should not error)
		err := DeleteCompactMetadata(jobID)
		if err != nil {
			t.Errorf("DeleteCompactMetadata should not error on missing file, got: %v", err)
		}
	})
}

func TestCompactMetadataPath(t *testing.T) {
	// Save original env and restore after test
	origDataDir := os.Getenv("ROBOREV_DATA_DIR")
	defer func() {
		if origDataDir != "" {
			os.Setenv("ROBOREV_DATA_DIR", origDataDir)
		} else {
			os.Unsetenv("ROBOREV_DATA_DIR")
		}
	}()

	tmpDir := t.TempDir()
	os.Setenv("ROBOREV_DATA_DIR", tmpDir)

	jobID := int64(123)
	path := compactMetadataPath(jobID)

	expected := filepath.Join(tmpDir, "compact-123.json")
	if path != expected {
		t.Errorf("compactMetadataPath(123) = %q, want %q", path, expected)
	}

	// Verify it uses config.DataDir() correctly
	dataDir := config.DataDir()
	if dataDir != tmpDir {
		t.Errorf("config.DataDir() = %q, want %q", dataDir, tmpDir)
	}
}
