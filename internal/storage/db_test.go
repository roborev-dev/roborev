package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAndClose(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	require.NoError(t, err, "Open failed")
	defer db.Close()

	// Verify file exists
	_, err = os.Stat(dbPath)
	require.NoError(t, err, "Database file was not created")
}
