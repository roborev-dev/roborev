package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBarrierDetectsTestEventPollution(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")

	// Write a "pre-existing" prod entry.
	os.WriteFile(logPath, []byte(
		`{"event":"job.completed","component":"worker"}`+"\n",
	), 0644)

	barrier := NewProdLogBarrier(dir)

	// Append a test event.
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	fmt.Fprintln(f, `{"event":"test","component":"test","message":"msg"}`)
	f.Close()

	msg := barrier.Check()
	if msg == "" {
		t.Fatal("barrier should detect test event pollution")
	}
}

func TestBarrierDetectsDevDaemonStart(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")
	os.WriteFile(logPath, nil, 0644)

	barrier := NewProdLogBarrier(dir)

	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	fmt.Fprintln(f,
		`{"event":"daemon.started","details":{"version":"dev","pid":"999"}}`,
	)
	f.Close()

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
	os.WriteFile(runtimePath, []byte("{}"), 0644)

	msg := barrier.Check()
	if msg == "" {
		t.Fatal("barrier should detect daemon runtime file")
	}
}

func TestBarrierDetectsErrorsLogPollution(t *testing.T) {
	dir := t.TempDir()
	errPath := filepath.Join(dir, "errors.log")
	os.WriteFile(errPath, nil, 0644)

	barrier := NewProdLogBarrier(dir)

	f, _ := os.OpenFile(errPath, os.O_APPEND|os.O_WRONLY, 0644)
	fmt.Fprintln(f, `{"event":"test","component":"test","message":"err"}`)
	f.Close()

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
	os.WriteFile(runtimePath, []byte("{}"), 0644)

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
	os.WriteFile(runtimePath, []byte("{}"), 0644)

	barrier := NewProdLogBarrier(dir)

	// Modify the file after barrier creation.
	os.WriteFile(runtimePath, []byte(`{"pid":999}`), 0644)

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
	os.WriteFile(logPath, []byte(
		`{"event":"job.completed","component":"worker"}`+"\n",
	), 0644)

	barrier := NewProdLogBarrier(dir)

	// Append a legitimate prod entry (no test markers).
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	fmt.Fprintln(f,
		`{"event":"job.completed","component":"worker","details":{"job_id":"6638"}}`,
	)
	f.Close()

	msg := barrier.Check()
	if msg != "" {
		t.Fatalf("barrier should pass for clean logs, got: %s", msg)
	}
}
