package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendToFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("failed to open file: %v", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, content); err != nil {
		t.Fatalf("failed to write to file: %v", err)
	}
}

func TestBarrierDetectsTestEventPollution(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")

	// Write a "pre-existing" prod entry.
	if err := os.WriteFile(logPath, []byte(
		`{"event":"job.completed","component":"worker"}`+"\n",
	), 0644); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	barrier := NewProdLogBarrier(dir)

	// Append a test event.
	appendToFile(t, logPath, `{"event":"test","component":"test","message":"msg"}`)

	msg := barrier.Check()
	if msg == "" {
		t.Fatal("barrier should detect test event pollution")
	}
}

func TestBarrierDetectsDevDaemonStart(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")

	barrier := NewProdLogBarrier(dir)

	appendToFile(t, logPath, `{"event":"daemon.started","details":{"version":"dev","pid":"999"}}`)

	msg := barrier.Check()
	if msg == "" {
		t.Fatal("barrier should detect dev daemon start")
	}
}

func TestBarrierDetectsDaemonRuntimeFile(t *testing.T) {
	dir := t.TempDir()

	barrier := NewProdLogBarrier(dir)

	// Create a daemon.<our-pid>.json file.
	runtimePath := filepath.Join(dir,
		fmt.Sprintf("daemon.%d.json", os.Getpid()))
	if err := os.WriteFile(runtimePath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	msg := barrier.Check()
	if msg == "" {
		t.Fatal("barrier should detect daemon runtime file")
	}
}

func TestBarrierDetectsErrorsLogPollution(t *testing.T) {
	dir := t.TempDir()
	errPath := filepath.Join(dir, "errors.log")

	barrier := NewProdLogBarrier(dir)

	appendToFile(t, errPath, `{"event":"test","component":"test","message":"err"}`)

	msg := barrier.Check()
	if msg == "" {
		t.Fatal("barrier should detect test pollution in errors.log")
	}
	if !strings.Contains(msg, "errors.log") {
		t.Fatalf("message should mention errors.log, got: %s", msg)
	}
}

func TestBarrierIgnoresPreExistingRuntimeFile(t *testing.T) {
	dir := t.TempDir()

	// Create a daemon.<our-pid>.json BEFORE the barrier.
	runtimePath := filepath.Join(dir,
		fmt.Sprintf("daemon.%d.json", os.Getpid()))
	if err := os.WriteFile(runtimePath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	barrier := NewProdLogBarrier(dir)

	// File still exists but was there before tests.
	msg := barrier.Check()
	if msg != "" {
		t.Fatalf("barrier should ignore pre-existing runtime file, got: %s", msg)
	}
}

func TestBarrierDetectsPreExistingRuntimeMutation(t *testing.T) {
	dir := t.TempDir()

	// Create a daemon.<our-pid>.json BEFORE the barrier.
	runtimePath := filepath.Join(dir,
		fmt.Sprintf("daemon.%d.json", os.Getpid()))
	if err := os.WriteFile(runtimePath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	barrier := NewProdLogBarrier(dir)

	// Modify the file after barrier creation.
	if err := os.WriteFile(runtimePath, []byte(`{"pid":999}`), 0644); err != nil {
		t.Fatalf("failed to modify file: %v", err)
	}

	msg := barrier.Check()
	if msg == "" {
		t.Fatal("barrier should detect modified runtime file")
	}
	if !strings.Contains(msg, "modified") {
		t.Fatalf("message should mention modification, got: %s", msg)
	}
}

func TestBarrierPassesWhenClean(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")
	if err := os.WriteFile(logPath, []byte(
		`{"event":"job.completed","component":"worker"}`+"\n",
	), 0644); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	barrier := NewProdLogBarrier(dir)

	// Append a legitimate prod entry (no test markers).
	appendToFile(t, logPath, `{"event":"job.completed","component":"worker","details":{"job_id":"6638"}}`)

	msg := barrier.Check()
	if msg != "" {
		t.Fatalf("barrier should pass for clean logs, got: %s", msg)
	}
}
