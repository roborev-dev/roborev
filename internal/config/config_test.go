package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/roborev-dev/roborev/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	require.Equal(t, "127.0.0.1:7373", cfg.ServerAddr, "Expected ServerAddr '127.0.0.1:7373'")
	require.Equal(t, 4, cfg.MaxWorkers, "Expected MaxWorkers 4")
	require.Equal(t, "codex", cfg.DefaultAgent, "Expected DefaultAgent 'codex'")
	require.True(t, cfg.MouseEnabled, "Expected MouseEnabled to default to true")
}

func TestDataDir(t *testing.T) {
	t.Run("default uses home directory", func(t *testing.T) {
		t.Setenv("ROBOREV_DATA_DIR", "")

		dir := DataDir()
		home, _ := os.UserHomeDir()
		expected := filepath.Join(home, ".roborev")
		require.Equal(t, expected, dir, "Expected %s, got %s", expected, dir)
	})

	t.Run("env var overrides default", func(t *testing.T) {
		t.Setenv("ROBOREV_DATA_DIR", "/custom/data/dir")

		dir := DataDir()
		require.Equal(t, "/custom/data/dir", dir, "Expected /custom/data/dir, got %s", dir)
	})

	t.Run("GlobalConfigPath uses DataDir", func(t *testing.T) {
		testDir := filepath.Join(os.TempDir(), "roborev-test")
		t.Setenv("ROBOREV_DATA_DIR", testDir)

		path := GlobalConfigPath()
		expected := filepath.Join(testDir, "config.toml")
		require.Equal(t, expected, path, "Expected %s, got %s", expected, path)
	})
}

func TestResolveAgent(t *testing.T) {
	cfg := DefaultConfig()
	tmpDir := t.TempDir()

	agent := ResolveAgent("claude-code", tmpDir, cfg)
	require.Equal(t, "claude-code", agent, "Expected 'claude-code'")

	agent = ResolveAgent("", tmpDir, cfg)
	require.Equal(t, "codex", agent, "Expected 'codex' (from global)")

	writeRepoConfigStr(t, tmpDir, `agent = "claude-code"`)

	agent = ResolveAgent("", tmpDir, cfg)
	require.Equal(t, "claude-code", agent, "Expected 'claude-code' (from repo config)")

	agent = ResolveAgent("codex", tmpDir, cfg)
	require.Equal(t, "codex", agent, "Expected 'codex' (explicit)")
}

func TestSaveAndLoadGlobal(t *testing.T) {
	testenv.SetDataDir(t)

	cfg := DefaultConfig()
	cfg.DefaultAgent = "claude-code"
	cfg.MaxWorkers = 8

	err := SaveGlobal(cfg)
	require.NoError(t, err, "SaveGlobal failed: %v")

	loaded, err := LoadGlobal()
	require.NoError(t, err, "LoadGlobal failed: %v")
	assert.Equal(t, "claude-code", loaded.DefaultAgent, "Expected DefaultAgent 'claude-code'")
	assert.Equal(t, 8, loaded.MaxWorkers, "Expected MaxWorkers 8")

}
