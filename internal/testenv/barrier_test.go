package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func appendToFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	require.NoError(t, err, "failed to open file")
	defer f.Close()
	_, err = fmt.Fprintln(f, content)
	require.NoError(t, err, "failed to write to file")
}

func TestBarrierDetectsTestEventPollution(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")

	// Write a "pre-existing" prod entry.
	err := os.WriteFile(logPath, []byte(
		`{"event":"job.completed","component":"worker"}`+"\n",
	), 0644)
	require.NoError(t, err, "failed to write initial file")

	barrier := NewProdLogBarrier(dir)

	// Append a test event.
	appendToFile(t, logPath, `{"event":"test","component":"test","message":"msg"}`)

	msg := barrier.Check()
	require.NotEmpty(t, msg, "barrier should detect test event pollution")
}

func TestBarrierDetectsDevDaemonStart(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")

	barrier := NewProdLogBarrier(dir)

	appendToFile(t, logPath, `{"event":"daemon.started","details":{"version":"dev","pid":"999"}}`)

	msg := barrier.Check()
	require.NotEmpty(t, msg, "barrier should detect dev daemon start")
}

func TestBarrierDetectsDaemonRuntimeFile(t *testing.T) {
	dir := t.TempDir()

	barrier := NewProdLogBarrier(dir)

	// Create a daemon.<our-pid>.json file.
	runtimePath := filepath.Join(dir,
		fmt.Sprintf("daemon.%d.json", os.Getpid()))
	err := os.WriteFile(runtimePath, []byte("{}"), 0644)
	require.NoError(t, err, "failed to write initial file")

	msg := barrier.Check()
	require.NotEmpty(t, msg, "barrier should detect daemon runtime file")
}

func TestBarrierDetectsErrorsLogPollution(t *testing.T) {
	dir := t.TempDir()
	errPath := filepath.Join(dir, "errors.log")

	barrier := NewProdLogBarrier(dir)

	appendToFile(t, errPath, `{"event":"test","component":"test","message":"err"}`)

	msg := barrier.Check()
	require.NotEmpty(t, msg, "barrier should detect test pollution in errors.log")
	assert.Contains(t, msg, "errors.log",
		"message should mention errors.log, got: %s", msg)
}

func TestBarrierIgnoresPreExistingRuntimeFile(t *testing.T) {
	dir := t.TempDir()

	// Create a daemon.<our-pid>.json BEFORE the barrier.
	runtimePath := filepath.Join(dir,
		fmt.Sprintf("daemon.%d.json", os.Getpid()))
	err := os.WriteFile(runtimePath, []byte("{}"), 0644)
	require.NoError(t, err, "failed to write initial file")

	barrier := NewProdLogBarrier(dir)

	// File still exists but was there before tests.
	msg := barrier.Check()
	assert.Empty(t, msg, "barrier should ignore pre-existing runtime file, got: %s", msg)
}

func TestBarrierDetectsPreExistingRuntimeMutation(t *testing.T) {
	dir := t.TempDir()

	// Create a daemon.<our-pid>.json BEFORE the barrier.
	runtimePath := filepath.Join(dir,
		fmt.Sprintf("daemon.%d.json", os.Getpid()))
	err := os.WriteFile(runtimePath, []byte("{}"), 0644)
	require.NoError(t, err, "failed to write initial file")

	barrier := NewProdLogBarrier(dir)

	// Modify the file after barrier creation.
	err = os.WriteFile(runtimePath, []byte(`{"pid":999}`), 0644)
	require.NoError(t, err, "failed to modify file")

	msg := barrier.Check()
	require.NotEmpty(t, msg, "barrier should detect modified runtime file")
	assert.Contains(t, msg, "modified",
		"message should mention modification, got: %s", msg)
}

func TestBarrierPassesWhenClean(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.log")
	err := os.WriteFile(logPath, []byte(
		`{"event":"job.completed","component":"worker"}`+"\n",
	), 0644)
	require.NoError(t, err, "failed to write initial file")

	barrier := NewProdLogBarrier(dir)

	// Append a legitimate prod entry (no test markers).
	appendToFile(t, logPath, `{"event":"job.completed","component":"worker","details":{"job_id":"6638"}}`)

	msg := barrier.Check()
	assert.Empty(t, msg, "barrier should pass for clean logs, got: %s", msg)
}
