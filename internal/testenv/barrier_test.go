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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
}

func runtimeFilePath(dir string) string {
	return filepath.Join(dir, fmt.Sprintf("daemon.%d.json", os.Getpid()))
}

func assertDetected(t *testing.T, msg, reason string) {
	t.Helper()
	if msg == "" {
		t.Fatalf("barrier should detect %s", reason)
	}
}

func TestBarrierDetectsTestEventPollution(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")

	// Write a "pre-existing" prod entry.
	writeFile(t, logPath, `{"event":"job.completed","component":"worker"}`+"\n")

	barrier := NewProdLogBarrier(dir)

	// Append a test event.
	appendToFile(t, logPath, `{"event":"test","component":"test","message":"msg"}`)

	msg := barrier.Check()
	assertDetected(t, msg, "test event pollution")
}

func TestBarrierDetectsDevDaemonStart(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")

	barrier := NewProdLogBarrier(dir)

	appendToFile(t, logPath, `{"event":"daemon.started","details":{"version":"dev","pid":"999"}}`)

	msg := barrier.Check()
	assertDetected(t, msg, "dev daemon start")
}

func TestBarrierDetectsDaemonRuntimeFile(t *testing.T) {
	dir := t.TempDir()

	barrier := NewProdLogBarrier(dir)

	// Create a daemon.<our-pid>.json file.
	runtimePath := runtimeFilePath(dir)
	writeFile(t, runtimePath, "{}")

	msg := barrier.Check()
	assertDetected(t, msg, "daemon runtime file")
}

func TestBarrierDetectsErrorsLogPollution(t *testing.T) {
	dir := t.TempDir()
	errPath := filepath.Join(dir, "errors.log")

	barrier := NewProdLogBarrier(dir)

	appendToFile(t, errPath, `{"event":"test","component":"test","message":"err"}`)

	msg := barrier.Check()
	assertDetected(t, msg, "test pollution in errors.log")
	if !strings.Contains(msg, "errors.log") {
		t.Fatalf("message should mention errors.log, got: %s", msg)
	}
}

func TestBarrierIgnoresPreExistingRuntimeFile(t *testing.T) {
	dir := t.TempDir()

	// Create a daemon.<our-pid>.json BEFORE the barrier.
	runtimePath := runtimeFilePath(dir)
	writeFile(t, runtimePath, "{}")

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
	runtimePath := runtimeFilePath(dir)
	writeFile(t, runtimePath, "{}")

	barrier := NewProdLogBarrier(dir)

	// Modify the file after barrier creation.
	writeFile(t, runtimePath, `{"pid":999}`)

	msg := barrier.Check()
	assertDetected(t, msg, "modified runtime file")
	if !strings.Contains(msg, "modified") {
		t.Fatalf("message should mention modification, got: %s", msg)
	}
}

func TestBarrierPassesWhenClean(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")
	writeFile(t, logPath, `{"event":"job.completed","component":"worker"}`+"\n")

	barrier := NewProdLogBarrier(dir)

	// Append a legitimate prod entry (no test markers).
	appendToFile(t, logPath, `{"event":"job.completed","component":"worker","details":{"job_id":"6638"}}`)

	msg := barrier.Check()
	if msg != "" {
		t.Fatalf("barrier should pass for clean logs, got: %s", msg)
	}
}
