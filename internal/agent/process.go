package agent

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var subprocessWaitDelay = 5 * time.Second

func configureSubprocess(cmd *exec.Cmd) {
	cmd.WaitDelay = subprocessWaitDelay
}

func closeOnContextDone(ctx context.Context, c io.Closer) func() {
	if c == nil || ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	var stopped atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
			if stopped.Load() {
				return
			}
			_ = c.Close()
		case <-done:
		}
	}()
	return func() {
		stopped.Store(true)
		once.Do(func() {
			close(done)
		})
	}
}

func contextProcessError(
	ctx context.Context, runErr error, parseErr error,
) error {
	ctxErr := ctx.Err()
	if ctxErr == nil {
		return nil
	}
	if runErr != nil {
		if errors.Is(runErr, ctxErr) || errors.Is(runErr, exec.ErrWaitDelay) {
			return ctxErr
		}
	}
	if parseErr != nil && parseErrIndicatesClosedPipe(parseErr) {
		return ctxErr
	}
	return nil
}

func parseErrIndicatesClosedPipe(err error) bool {
	return errors.Is(err, fs.ErrClosed) ||
		strings.Contains(err.Error(), "file already closed")
}
