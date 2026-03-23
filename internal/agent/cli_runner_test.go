package agent

import (
	"context"
	"io"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunStreamingCLIPreservesWaitErrWhenContextCancelsAfterParse(t *testing.T) {
	skipIfWindows(t)

	cmdPath := writeTempCommand(t, "#!/bin/sh\ncase \"$1\" in *etxtbsy*) exit 0;; esac\nprintf 'ok\\n'\nexit 1\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := runStreamingCLI(ctx, streamingCLISpec{
		Name:    "test",
		Command: cmdPath,
		Parse: func(r io.Reader, sw *syncWriter) (string, error) {
			data, readErr := io.ReadAll(r)
			require.NoError(t, readErr)
			cancel()
			return string(data), fs.ErrClosed
		},
	})

	require.NoError(t, err)
	require.Error(t, result.WaitErr)
	assert.Equal(t, "ok\n", result.Result)
	assert.ErrorIs(t, result.ParseErr, fs.ErrClosed)
	assert.Contains(t, result.WaitErr.Error(), "exit status 1")
}
