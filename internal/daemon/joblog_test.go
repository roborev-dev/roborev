package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestJobLogDir(t *testing.T) {
	tmpDir := setupTestEnv(t)
	got := JobLogDir()
	want := filepath.Join(tmpDir, "logs", "jobs")
	if got != want {
		t.Errorf("JobLogDir() = %q, want %q", got, want)
	}
}

func TestJobLogPath(t *testing.T) {
	tmpDir := setupTestEnv(t)
	got := JobLogPath(42)
	want := filepath.Join(tmpDir, "logs", "jobs", "42.log")
	if got != want {
		t.Errorf("JobLogPath(42) = %q, want %q", got, want)
	}
}

func TestOpenJobLog(t *testing.T) {
	setupTestEnv(t)

	f := openJobLog(99)
	if f == nil {
		t.Fatal("openJobLog returned nil")
	}
	defer f.Close()

	// Write some data and verify it lands on disk
	_, err := f.WriteString("hello\n")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	data, err := os.ReadFile(JobLogPath(99))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello\n" {
		t.Errorf("file contents = %q, want %q", data, "hello\n")
	}

	// Verify restrictive permissions (skip on Windows where
	// POSIX file modes don't apply).
	if runtime.GOOS != "windows" {
		info, err := os.Stat(JobLogPath(99))
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		perm := info.Mode().Perm()
		if perm&0o077 != 0 {
			t.Errorf(
				"log file permissions = %o, want 0600",
				perm,
			)
		}

		dirInfo, err := os.Stat(JobLogDir())
		if err != nil {
			t.Fatalf("stat dir: %v", err)
		}
		dirPerm := dirInfo.Mode().Perm()
		if dirPerm&0o077 != 0 {
			t.Errorf(
				"log dir permissions = %o, want 0700",
				dirPerm,
			)
		}
	}
}

func TestOpenJobLog_TightensPermissivePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions not applicable on Windows")
	}
	setupTestEnv(t)

	// Pre-create dir and file with permissive modes, simulating
	// an install upgraded from a version that used 0755/0644.
	dir := JobLogDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := JobLogPath(500)
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	f := openJobLog(500)
	if f == nil {
		t.Fatal("openJobLog returned nil")
	}
	f.Close()

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		t.Errorf(
			"dir permissions = %o, want 0700",
			dirInfo.Mode().Perm(),
		)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if fileInfo.Mode().Perm()&0o077 != 0 {
		t.Errorf(
			"file permissions = %o, want 0600",
			fileInfo.Mode().Perm(),
		)
	}
}

func TestCleanJobLogs(t *testing.T) {
	setupTestEnv(t)

	dir := JobLogDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create an "old" log file by writing and then back-dating its mtime
	oldPath := filepath.Join(dir, "1.log")
	if err := os.WriteFile(oldPath, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}

	// Create a "new" log file (default mtime = now)
	newPath := filepath.Join(dir, "2.log")
	if err := os.WriteFile(newPath, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a non-log file (should be ignored)
	txtPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txtPath, []byte("ignore"), 0644); err != nil {
		t.Fatal(err)
	}

	removed := CleanJobLogs(7 * 24 * time.Hour)
	if removed != 1 {
		t.Errorf("CleanJobLogs removed %d files, want 1", removed)
	}

	// Old file should be gone
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old log file should be removed")
	}

	// New file should remain
	if _, err := os.Stat(newPath); err != nil {
		t.Error("new log file should still exist")
	}

	// Non-log file should remain
	if _, err := os.Stat(txtPath); err != nil {
		t.Error("non-log file should still exist")
	}
}

func TestCleanJobLogs_NoDir(t *testing.T) {
	setupTestEnv(t)
	// No logs/jobs directory exists — should return 0 without error
	removed := CleanJobLogs(7 * 24 * time.Hour)
	if removed != 0 {
		t.Errorf("CleanJobLogs on missing dir = %d, want 0", removed)
	}
}

func TestSafeWriter(t *testing.T) {
	t.Run("normal_writes", func(t *testing.T) {
		setupTestEnv(t)
		f := openJobLog(200)
		if f == nil {
			t.Fatal("openJobLog returned nil")
		}
		defer f.Close()

		sw := &safeWriter{w: f}

		n, err := sw.Write([]byte("line 1\n"))
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 7 {
			t.Errorf("Write returned %d, want 7", n)
		}

		n, err = sw.Write([]byte("line 2\n"))
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 7 {
			t.Errorf("Write returned %d, want 7", n)
		}

		f.Close()
		data, _ := os.ReadFile(JobLogPath(200))
		if string(data) != "line 1\nline 2\n" {
			t.Errorf("contents = %q", data)
		}
	})

	t.Run("swallows_errors", func(t *testing.T) {
		setupTestEnv(t)
		f := openJobLog(201)
		if f == nil {
			t.Fatal("openJobLog returned nil")
		}

		// Close the file to force write errors
		f.Close()

		sw := &safeWriter{w: f}

		// First write should fail internally but report success
		n, err := sw.Write([]byte("data"))
		if err != nil {
			t.Fatalf("safeWriter should not return errors, got: %v", err)
		}
		if n != 4 {
			t.Errorf("Write returned %d, want 4", n)
		}

		// Subsequent writes should also silently succeed
		n, err = sw.Write([]byte("more data"))
		if err != nil {
			t.Fatalf("safeWriter should not return errors, got: %v", err)
		}
		if n != 9 {
			t.Errorf("Write returned %d, want 9", n)
		}
	})
}

func TestReadJobLog(t *testing.T) {
	setupTestEnv(t)

	t.Run("existing", func(t *testing.T) {
		f := openJobLog(300)
		if f == nil {
			t.Fatal("openJobLog returned nil")
		}
		_, _ = f.WriteString("log content")
		f.Close()

		data, err := ReadJobLog(300)
		if err != nil {
			t.Fatalf("ReadJobLog: %v", err)
		}
		if string(data) != "log content" {
			t.Errorf("contents = %q, want %q", data, "log content")
		}
	})

	t.Run("missing", func(t *testing.T) {
		_, err := ReadJobLog(999)
		if err == nil {
			t.Error("ReadJobLog should error for missing file")
		}
	})
}

func TestJobLogExists(t *testing.T) {
	setupTestEnv(t)

	if JobLogExists(400) {
		t.Error("should not exist before creation")
	}

	f := openJobLog(400)
	if f == nil {
		t.Fatal("openJobLog returned nil")
	}
	f.Close()

	if !JobLogExists(400) {
		t.Error("should exist after creation")
	}
}

func TestParseJobIDFromLogName(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantID int64
		wantOK bool
	}{
		{"valid", "42.log", 42, true},
		{"large", "12345.log", 12345, true},
		{"no_suffix", "42.txt", 0, false},
		{"not_numeric", "abc.log", 0, false},
		{"negative", "-1.log", 0, false},
		{"zero", "0.log", 0, false},
		{"empty", ".log", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := ParseJobIDFromLogName(tt.input)
			if id != tt.wantID || ok != tt.wantOK {
				t.Errorf("ParseJobIDFromLogName(%q) = (%d, %v), want (%d, %v)",
					tt.input, id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}
