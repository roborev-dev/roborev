// ABOUTME: Unit tests for compact job metadata handling
// ABOUTME: Tests reading, deleting, and validating compact metadata files
package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func setupTestEnv(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)
	return tmpDir
}

func TestReadCompactMetadata(t *testing.T) {
	tests := []struct {
		name     string
		jobID    int64
		mockFile []byte
		wantIDs  []int64
		wantErr  bool
	}{
		{
			name:     "valid_metadata",
			jobID:    123,
			mockFile: []byte(`{"source_job_ids":[100, 200, 300]}`),
			wantIDs:  []int64{100, 200, 300},
			wantErr:  false,
		},
		{
			name:     "missing_file",
			jobID:    999,
			mockFile: nil,
			wantIDs:  nil,
			wantErr:  true,
		},
		{
			name:     "invalid_json",
			jobID:    456,
			mockFile: []byte("{invalid json}"),
			wantIDs:  nil,
			wantErr:  true,
		},
		{
			name:     "empty_source_ids",
			jobID:    789,
			mockFile: []byte(`{"source_job_ids":[]}`),
			wantIDs:  []int64{},
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)

			if tt.mockFile != nil {
				path := compactMetadataPath(tt.jobID)
				if err := os.WriteFile(path, tt.mockFile, 0644); err != nil {
					t.Fatalf("Setup failed: %v", err)
				}
			}

			got, err := ReadCompactMetadata(tt.jobID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadCompactMetadata() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if !slices.Equal(got.SourceJobIDs, tt.wantIDs) {
					t.Errorf("ReadCompactMetadata() = %v, want %v", got.SourceJobIDs, tt.wantIDs)
				}
			}
		})
	}
}

func TestDeleteCompactMetadata(t *testing.T) {
	t.Run("delete_existing_file", func(t *testing.T) {
		setupTestEnv(t)
		jobID := int64(123)

		// Create a metadata file
		path := compactMetadataPath(jobID)
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
		setupTestEnv(t)
		jobID := int64(999)

		// Try to delete non-existent file (should not error)
		err := DeleteCompactMetadata(jobID)
		if err != nil {
			t.Errorf("DeleteCompactMetadata should not error on missing file, got: %v", err)
		}
	})
}

func TestCompactMetadataPath(t *testing.T) {
	tmpDir := setupTestEnv(t)

	jobID := int64(123)

	path := compactMetadataPath(jobID)

	expected := filepath.Join(tmpDir, "compact-123.json")
	if path != expected {
		t.Errorf("compactMetadataPath(123) = %q, want %q", path, expected)
	}
}
