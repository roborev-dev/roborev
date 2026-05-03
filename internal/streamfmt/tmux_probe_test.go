//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || zos

package streamfmt

import (
	"bytes"
	"io"
	"testing"

	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
)

// fakeTMUXEnviron implements termenv.Environ and returns the configured
// TMUX/TERM values, defaulting to empty for everything else.
type fakeTMUXEnviron struct {
	vars map[string]string
}

func (f fakeTMUXEnviron) Environ() []string {
	out := make([]string, 0, len(f.vars))
	for k, v := range f.vars {
		out = append(out, k+"="+v)
	}
	return out
}

func (f fakeTMUXEnviron) Getenv(key string) string {
	return f.vars[key]
}

// captureTTY satisfies termenv.File (io.ReadWriter + Fd) and records
// every byte termenv tries to write to the terminal. Reads always return
// io.EOF so we never block waiting for a response, and so writes are not
// silently consumed (bytes.Buffer.Read would drain bytes that Write just
// appended, masking the bug we are testing for).
type captureTTY struct {
	written bytes.Buffer
}

func (c *captureTTY) Write(p []byte) (int, error) {
	return c.written.Write(p)
}

func (c *captureTTY) Read(p []byte) (int, error) {
	return 0, io.EOF
}

func (c *captureTTY) Fd() uintptr { return 0 }

// TestVendoredTermenvSuppressesTMUXProbes verifies that the vendored
// termenv (wired in via the go.mod replace directive) suppresses
// background-color and cursor-position probes whenever $TMUX is set,
// even when $TERM is xterm-256color (the common true-color tmux setup).
//
// This is the regression test for the bug where `roborev remap --quiet`
// run by the post-rewrite git hook would dump terminal escape responses
// like `^[]11;rgb:1515/1111/1010^[\` and `^[[18;91R` into the user's
// shell during `git rebase`.
//
// If this test fails, either:
//   - the go.mod replace directive is missing or broken, or
//   - the TMUX guard in internal/third_party/termenv/termenv_unix.go was
//     removed or weakened — see PATCHES.md in that directory.
func TestVendoredTermenvSuppressesTMUXProbes(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "tmux + xterm-256color",
			env: map[string]string{
				"TMUX": "/tmp/tmux-1000/default,123,4",
				"TERM": "xterm-256color",
			},
		},
		{
			name: "tmux + truecolor wezterm",
			env: map[string]string{
				"TMUX":      "/tmp/tmux-1000/default,123,4",
				"TERM":      "wezterm",
				"COLORTERM": "truecolor",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tty := &captureTTY{}
			out := termenv.NewOutput(tty,
				termenv.WithEnvironment(fakeTMUXEnviron{vars: tc.env}),
				termenv.WithUnsafe(),
			)

			_ = out.ForegroundColor()
			_ = out.BackgroundColor()

			written := tty.written.String()
			assert.NotContains(t, written, "\x1b]11;",
				"OSC 11 background-color probe leaked under tmux")
			assert.NotContains(t, written, "\x1b]10;",
				"OSC 10 foreground-color probe leaked under tmux")
			assert.NotContains(t, written, "\x1b[6n",
				"cursor-position probe leaked under tmux")
			assert.Empty(t, written,
				"expected zero bytes written under tmux, got %q", written)
		})
	}
}
