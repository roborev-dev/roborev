package tokens

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatSummary(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  string
	}{
		{"zero", Usage{}, ""},
		{
			"small counts",
			Usage{PeakContextTokens: 500, OutputTokens: 120},
			"500 ctx · 120 out",
		},
		{
			"thousands",
			Usage{PeakContextTokens: 45200, OutputTokens: 3900},
			"45.2k ctx · 3.9k out",
		},
		{
			"millions",
			Usage{PeakContextTokens: 2_500_000, OutputTokens: 15_000},
			"2.5M ctx · 15.0k out",
		},
		{
			"output only",
			Usage{OutputTokens: 800},
			"0 ctx · 800 out",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage.FormatSummary()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseJSON(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		assert.Nil(t, ParseJSON(""))
	})

	t.Run("valid json", func(t *testing.T) {
		u := ParseJSON(
			`{"peak_context_tokens":1000,"total_output_tokens":200}`,
		)
		require.NotNil(t, u)
		assert.Equal(t, int64(1000), u.PeakContextTokens)
		assert.Equal(t, int64(200), u.OutputTokens)
	})

	t.Run("all zeros", func(t *testing.T) {
		assert.Nil(t, ParseJSON(
			`{"peak_context_tokens":0,"total_output_tokens":0}`,
		))
	})

	t.Run("invalid json", func(t *testing.T) {
		assert.Nil(t, ParseJSON(`{invalid`))
	})
}

func TestCheckVersion(t *testing.T) {
	// Create a fake agentsview script that prints the given output.
	makeScript := func(t *testing.T, output string) string {
		t.Helper()
		dir := t.TempDir()
		bin := filepath.Join(dir, "agentsview")
		script := "#!/bin/sh\necho '" + output + "'\n"
		require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
		return bin
	}

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			"exact minimum",
			"agentsview v0.15.0 (commit abc, built 2026-01-01)",
			true,
		},
		{
			"newer patch",
			"agentsview v0.15.1 (commit abc, built 2026-01-01)",
			true,
		},
		{
			"newer minor",
			"agentsview v0.16.0 (commit abc, built 2026-01-01)",
			true,
		},
		{
			"newer major",
			"agentsview v1.0.0 (commit abc, built 2026-01-01)",
			true,
		},
		{
			"dev suffix",
			"agentsview v0.15.0-1-g891cb62 (commit 891cb62, built 2026-03-18)",
			true,
		},
		{
			"too old",
			"agentsview v0.14.9 (commit abc, built 2026-01-01)",
			false,
		},
		{
			"very old",
			"agentsview v0.10.0 (commit abc, built 2026-01-01)",
			false,
		},
		{
			"unparseable",
			"something unexpected",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := makeScript(t, tt.output)
			got := checkVersion(context.Background(), bin)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("missing binary", func(t *testing.T) {
		got := checkVersion(
			context.Background(), "/nonexistent/agentsview",
		)
		assert.False(t, got)
	})
}

func TestFetchForSessionSkipsOldVersion(t *testing.T) {
	// Verify FetchForSession returns nil when agentsview is too old,
	// rather than invoking token-use (which could spawn a server).
	ResetVersionCache()
	t.Cleanup(ResetVersionCache)

	dir := t.TempDir()
	bin := filepath.Join(dir, "agentsview")
	// Script that prints old version for "version", and would fail
	// loudly for any other subcommand.
	script := `#!/bin/sh
if [ "$1" = "version" ]; then
  echo "agentsview v0.14.0 (commit abc, built 2026-01-01)"
  exit 0
fi
echo "ERROR: should not be called" >&2
exit 99
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+":"+origPath)

	// Confirm our fake is found.
	_, err := exec.LookPath("agentsview")
	require.NoError(t, err)

	usage, err := FetchForSession(
		context.Background(), "test-session-id",
	)
	require.NoError(t, err)
	assert.Nil(t, usage)
}

func TestResolveAgentsviewRetriesAfterTransientFailure(t *testing.T) {
	ResetVersionCache()
	t.Cleanup(ResetVersionCache)

	// First call: agentsview not on PATH → transient failure.
	t.Setenv("PATH", t.TempDir())
	_, ok := resolveAgentsview(context.Background())
	assert.False(t, ok, "should fail when binary is absent")

	// Install a valid agentsview and retry — should succeed.
	dir := t.TempDir()
	bin := filepath.Join(dir, "agentsview")
	script := `#!/bin/sh
if [ "$1" = "version" ]; then
  echo "agentsview v0.15.0 (commit abc, built 2026-01-01)"
  exit 0
fi
if [ "$1" = "token-use" ]; then
  echo '{"session_id":"s","agent":"a","project":"p","total_output_tokens":100,"peak_context_tokens":200}'
  exit 0
fi
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+":"+origPath)

	path, ok := resolveAgentsview(context.Background())
	assert.True(t, ok, "should succeed after binary appears")
	assert.Equal(t, bin, path)
}

func TestResolveAgentsviewCachesTooOldPermanently(t *testing.T) {
	ResetVersionCache()
	t.Cleanup(ResetVersionCache)

	dir := t.TempDir()
	bin := filepath.Join(dir, "agentsview")
	script := `#!/bin/sh
echo "agentsview v0.14.0 (commit abc, built 2026-01-01)"
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+":"+origPath)

	_, ok := resolveAgentsview(context.Background())
	assert.False(t, ok)

	// Even if we "upgrade" the script, the too-old result is cached.
	script2 := `#!/bin/sh
echo "agentsview v0.15.0 (commit abc, built 2026-01-01)"
`
	require.NoError(t, os.WriteFile(bin, []byte(script2), 0o755))

	_, ok = resolveAgentsview(context.Background())
	assert.False(t, ok, "too-old should be cached permanently")
}

func TestToJSON(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		assert.Empty(t, ToJSON(nil))
	})

	t.Run("round trip", func(t *testing.T) {
		orig := &Usage{
			PeakContextTokens: 5000,
			OutputTokens:      300,
		}
		s := ToJSON(orig)
		got := ParseJSON(s)
		require.NotNil(t, got)
		assert.Equal(t, orig.PeakContextTokens, got.PeakContextTokens)
		assert.Equal(t, orig.OutputTokens, got.OutputTokens)
	})
}
