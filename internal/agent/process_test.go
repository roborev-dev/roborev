package agent

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"io/fs"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

type countingCloser struct {
	closed atomic.Int32
}

func (c *countingCloser) Close() error {
	c.closed.Add(1)
	return nil
}

func TestCloseOnContextDoneClosesOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	closer := &countingCloser{}
	stop := closeOnContextDone(ctx, closer)
	defer stop()

	cancel()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if closer.closed.Load() == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	require.Equal(t, int32(1), closer.closed.Load(), "expected closer to be closed after context cancellation")
}

func TestCloseOnContextDoneStopPreventsClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	closer := &countingCloser{}
	stop := closeOnContextDone(ctx, closer)
	stop()
	cancel()
	time.Sleep(20 * time.Millisecond)

	if got := closer.closed.Load(); got != 0 {
		require.Equal(t, int32(0), got, "closer should not be closed after stop(), got %d", got)
	}
}

func TestCloseOnContextDoneBackgroundIsNoop(t *testing.T) {
	closer := &countingCloser{}
	stop := closeOnContextDone(context.Background(), closer)
	stop()

	if got := closer.closed.Load(); got != 0 {
		require.Equal(t, int32(0), got, "background context should not close the closer, got %d", got)
	}
}

func TestContextProcessError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tracker := &subprocessTracker{}
	tracker.canceledByContext.Store(true)

	if got := contextProcessError(ctx, tracker, errors.New("agent failed"), nil); got != nil {
		require.NoError(t, got, "real subprocess error should be preserved, got context error %v", got)
	}
	if got := contextProcessError(ctx, tracker, exec.ErrWaitDelay, nil); !errors.Is(got, context.Canceled) {
		require.ErrorIs(t, got, context.Canceled, "expected context cancellation for wait delay, got %v", got)
	}
	if got := contextProcessError(ctx, tracker, nil, fs.ErrClosed); !errors.Is(got, context.Canceled) {
		require.ErrorIs(t, got, context.Canceled, "expected context cancellation for closed pipe parse error, got %v", got)
	}
	if got := contextProcessError(ctx, tracker, errors.New("agent failed"), fs.ErrClosed); got != nil {
		require.NoError(t, got, "real subprocess error should not be masked by closed pipe parse error, got %v", got)
	}
}

func TestContextProcessErrorRunPathCancellation(t *testing.T) {
	skipIfWindows(t)

	cmdPath := writeTempCommand(t, "#!/bin/sh\nsleep 5\n")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, cmdPath)
	tracker := configureSubprocess(cmd)

	err := cmd.Run()
	require.Error(t, err, "expected command cancellation")

	if got := contextProcessError(ctx, tracker, err, nil); !errors.Is(got, context.DeadlineExceeded) {
		require.EqualError(t, got, context.DeadlineExceeded.Error(), "expected deadline exceeded, got %v (run err: %v)", got, err)
	}
}

func TestContextProcessErrorDoesNotMaskSignalExitAfterContextDone(t *testing.T) {
	skipIfWindows(t)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "kill -KILL $$")
	tracker := configureSubprocess(cmd)

	err := cmd.Run()
	require.Error(t, err, "expected signal exit")

	cancel()

	if got := contextProcessError(ctx, tracker, err, nil); got != nil {
		require.Same(t, err, got, "signal exit should not be rewritten as context error, got %v (run err: %v)", got, err)
	}
}

func TestConfigureSubprocessDoesNotMarkCanceledWhenProcessAlreadyExited(t *testing.T) {
	skipIfWindows(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", "exit 0")
	tracker := configureSubprocess(cmd)

	if err := cmd.Run(); err != nil {
		require.NoError(t, err, "Run: %v")
	}
	require.NotNil(t, cmd.Cancel, "expected wrapped cancel")

	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		require.ErrorIs(t, err, os.ErrProcessDone, "expected os.ErrProcessDone, got %v", err)
	}
	require.False(t, tracker.canceledByContext.Load(), "tracker should stay false when cancel runs after process exit")

}
