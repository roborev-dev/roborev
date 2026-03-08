package agent

import (
	"context"
	"errors"
	"io/fs"
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
	t.Fatal("expected closer to be closed after context cancellation")
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
		t.Fatalf("closer should not be closed after stop(), got %d", got)
	}
}

func TestCloseOnContextDoneBackgroundIsNoop(t *testing.T) {
	closer := &countingCloser{}
	stop := closeOnContextDone(context.Background(), closer)
	stop()

	if got := closer.closed.Load(); got != 0 {
		t.Fatalf("background context should not close the closer, got %d", got)
	}
}

func TestContextProcessError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := contextProcessError(ctx, errors.New("agent failed"), nil); got != nil {
		t.Fatalf("real subprocess error should be preserved, got context error %v", got)
	}
	if got := contextProcessError(ctx, exec.ErrWaitDelay, nil); !errors.Is(got, context.Canceled) {
		t.Fatalf("expected context cancellation for wait delay, got %v", got)
	}
	if got := contextProcessError(ctx, nil, fs.ErrClosed); !errors.Is(got, context.Canceled) {
		t.Fatalf("expected context cancellation for closed pipe parse error, got %v", got)
	}
}
