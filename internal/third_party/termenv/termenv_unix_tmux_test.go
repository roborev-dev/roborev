//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || zos

package termenv

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// fakeEnviron is a controllable Environ used to drive the tmux/$TMUX guard
// in termStatusReport without touching the real process environment.
type fakeEnviron struct {
	vars map[string]string
}

func (f fakeEnviron) Environ() []string {
	out := make([]string, 0, len(f.vars))
	for k, v := range f.vars {
		out = append(out, k+"="+v)
	}
	return out
}

func (f fakeEnviron) Getenv(key string) string {
	return f.vars[key]
}

// captureTTY satisfies the File interface (io.ReadWriter + Fd) so termenv
// progresses past the TTY-nil short circuit. Reads always return EOF so we
// never block, and writes are captured separately so they cannot be
// silently consumed by a follow-up read (as would happen with bytes.Buffer
// where Read drains bytes that Write just appended).
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

// TestTermStatusReportTMUXGuard verifies the roborev-local patch that
// suppresses OSC / cursor-position probes whenever $TMUX is set, even when
// $TERM is misleadingly xterm-256color (the common true-color tmux setup).
func TestTermStatusReportTMUXGuard(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantWrite bool
	}{
		{
			name: "tmux set with xterm-256color suppresses probe",
			env: map[string]string{
				"TMUX": "/tmp/tmux-1000/default,123,4",
				"TERM": "xterm-256color",
			},
			wantWrite: false,
		},
		{
			name: "tmux set with empty TERM suppresses probe",
			env: map[string]string{
				"TMUX": "/tmp/tmux-1000/default,123,4",
				"TERM": "",
			},
			wantWrite: false,
		},
		{
			name: "TERM=screen suppresses probe even without TMUX",
			env: map[string]string{
				"TERM": "screen-256color",
			},
			wantWrite: false,
		},
		{
			name: "TERM=tmux suppresses probe even without TMUX",
			env: map[string]string{
				"TERM": "tmux-256color",
			},
			wantWrite: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cw := &captureTTY{}
			o := NewOutput(cw,
				WithEnvironment(fakeEnviron{vars: tc.env}),
				WithUnsafe(),
			)

			res, err := o.termStatusReport(11)
			if !errors.Is(err, ErrStatusReport) {
				t.Fatalf("expected ErrStatusReport, got err=%v res=%q", err, res)
			}
			if res != "" {
				t.Errorf("expected empty response, got %q", res)
			}
			if got := cw.written.Len(); got != 0 {
				t.Errorf("expected no bytes written to TTY, got %d bytes: %q",
					got, cw.written.String())
			}
		})
	}
}

// TestForegroundBackgroundColorTMUXNoProbe verifies the public APIs that
// trigger termStatusReport (foreground/background color queries) do not
// emit any escape sequences when $TMUX is set. This is the user-visible
// guarantee for the roborev tmux fix.
func TestForegroundBackgroundColorTMUXNoProbe(t *testing.T) {
	cw := &captureTTY{}
	o := NewOutput(cw,
		WithEnvironment(fakeEnviron{vars: map[string]string{
			"TMUX": "/tmp/tmux-1000/default,123,4",
			"TERM": "xterm-256color",
		}}),
		WithUnsafe(),
	)

	_ = o.ForegroundColor()
	_ = o.BackgroundColor()

	if got := cw.written.Len(); got != 0 {
		t.Errorf("expected no probe writes under tmux, got %d bytes: %q",
			got, cw.written.String())
	}
}
