package agent

import (
	"context"
	"io"
	"os/exec"
	"time"
)

var subprocessWaitDelay = 5 * time.Second

func configureSubprocess(cmd *exec.Cmd) {
	cmd.WaitDelay = subprocessWaitDelay
}

func closeOnContextDone(ctx context.Context, c io.Closer) {
	if c == nil {
		return
	}
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()
}
