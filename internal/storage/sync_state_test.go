package storage

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/config"
)

func TestSyncState(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("get nonexistent key returns empty", func(t *testing.T) {
		val, err := db.GetSyncState("nonexistent")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if val != "" {
			t.Errorf("Expected empty string, got %q", val)
		}
	})

	t.Run("set and get", func(t *testing.T) {
		err := db.SetSyncState("test_key", "test_value")
		if err != nil {
			t.Fatalf("SetSyncState failed: %v", err)
		}

		val, err := db.GetSyncState("test_key")
		if err != nil {
			t.Fatalf("GetSyncState failed: %v", err)
		}
		if val != "test_value" {
			t.Errorf("Expected 'test_value', got %q", val)
		}
	})

	t.Run("upsert overwrites", func(t *testing.T) {
		err := db.SetSyncState("upsert_key", "first")
		if err != nil {
			t.Fatalf("SetSyncState failed: %v", err)
		}

		err = db.SetSyncState("upsert_key", "second")
		if err != nil {
			t.Fatalf("SetSyncState upsert failed: %v", err)
		}

		val, err := db.GetSyncState("upsert_key")
		if err != nil {
			t.Fatalf("GetSyncState failed: %v", err)
		}
		if val != "second" {
			t.Errorf("Expected 'second', got %q", val)
		}
	})
}

func TestGetMachineID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// UUID pattern
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	t.Run("generates new ID on first call", func(t *testing.T) {
		id, err := db.GetMachineID()
		if err != nil {
			t.Fatalf("GetMachineID failed: %v", err)
		}
		if !uuidPattern.MatchString(id) {
			t.Errorf("Machine ID %q is not a valid UUID", id)
		}
	})

	t.Run("returns same ID on subsequent calls", func(t *testing.T) {
		id1, err := db.GetMachineID()
		if err != nil {
			t.Fatalf("First GetMachineID failed: %v", err)
		}

		id2, err := db.GetMachineID()
		if err != nil {
			t.Fatalf("Second GetMachineID failed: %v", err)
		}

		if id1 != id2 {
			t.Errorf("Machine IDs differ: %q vs %q", id1, id2)
		}
	})
}

func TestGetMachineID_EmptyValueRegeneration(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Insert an empty machine ID (simulating manual edit or past bug)
	_, err := db.Exec(`INSERT OR REPLACE INTO sync_state (key, value) VALUES (?, '')`, SyncStateMachineID)
	if err != nil {
		t.Fatalf("Failed to insert empty machine ID: %v", err)
	}

	// GetMachineID should regenerate when value is empty
	id, err := db.GetMachineID()
	if err != nil {
		t.Fatalf("GetMachineID failed: %v", err)
	}
	if id == "" {
		t.Error("Expected non-empty machine ID after regeneration")
	}

	// Verify it's now stored
	var stored string
	err = db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, SyncStateMachineID).Scan(&stored)
	if err != nil {
		t.Fatalf("Failed to query stored ID: %v", err)
	}
	if stored != id {
		t.Errorf("Stored ID %q doesn't match returned ID %q", stored, id)
	}
}

func TestSyncWorker_StartStopStart(t *testing.T) {
	// This test verifies that SyncWorker can be started, stopped, and restarted
	// without issues (channel reinitialization on restart).
	db := openTestDB(t)
	defer db.Close()

	// Note: Sync is disabled by default, so Start() will fail with "sync is not enabled"
	// which is expected. We're testing that the worker handles restarts correctly
	// when enabled.

	t.Run("Start fails when sync is disabled", func(t *testing.T) {
		worker := NewSyncWorker(db, config.SyncConfig{Enabled: false})
		err := worker.Start()
		if err == nil {
			t.Fatal("Expected error when starting with sync disabled")
		}
	})

	t.Run("Start fails without postgres_url", func(t *testing.T) {
		worker := NewSyncWorker(db, config.SyncConfig{
			Enabled:  true,
			Interval: "1s",
		})
		// This will start the goroutine which will fail to connect,
		// but Start() itself should succeed
		err := worker.Start()
		if err != nil {
			t.Fatalf("Expected Start to succeed (connection fails async): %v", err)
		}
		// Stop the worker
		worker.Stop()
	})

	t.Run("Start-Stop-Start cycle works", func(t *testing.T) {
		worker := NewSyncWorker(db, config.SyncConfig{
			Enabled:  true,
			Interval: "1s",
		})

		// First start
		err := worker.Start()
		if err != nil {
			t.Fatalf("First Start failed: %v", err)
		}

		// Stop
		worker.Stop()

		// Second start - this tests that channels are reinitialized
		err = worker.Start()
		if err != nil {
			t.Fatalf("Second Start failed (channel reinit issue?): %v", err)
		}

		// Stop again
		worker.Stop()
	})

	t.Run("Double start fails", func(t *testing.T) {
		worker := NewSyncWorker(db, config.SyncConfig{
			Enabled:  true,
			Interval: "1s",
		})

		err := worker.Start()
		if err != nil {
			t.Fatalf("First Start failed: %v", err)
		}
		defer worker.Stop()

		err = worker.Start()
		if err == nil {
			t.Fatal("Expected error on double start")
		}
	})
}

func TestSyncWorker_SyncNowReturnsErrorWhenNotRunning(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	cfg := config.SyncConfig{
		Enabled: true,
	}
	worker := NewSyncWorker(db, cfg)

	// SyncNow should return error when worker is not running
	_, err = worker.SyncNow()
	if err == nil {
		t.Fatal("Expected error from SyncNow when worker not running")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("Expected 'not running' error, got: %v", err)
	}
}

func TestSyncWorker_FinalPushReturnsNilWhenNotConnected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	cfg := config.SyncConfig{
		Enabled: true,
	}
	worker := NewSyncWorker(db, cfg)

	// FinalPush should return nil when not connected (nothing to push)
	err = worker.FinalPush()
	if err != nil {
		t.Fatalf("FinalPush should return nil when not connected, got: %v", err)
	}
}
