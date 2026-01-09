package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config holds the daemon configuration
type Config struct {
	ServerAddr         string `toml:"server_addr"`
	MaxWorkers         int    `toml:"max_workers"`
	ReviewContextCount int    `toml:"review_context_count"`
	DefaultAgent       string `toml:"default_agent"`
	JobTimeoutMinutes  int    `toml:"job_timeout_minutes"`

	// Agent commands
	CodexCmd      string `toml:"codex_cmd"`
	ClaudeCodeCmd string `toml:"claude_code_cmd"`
}

// RepoConfig holds per-repo overrides
type RepoConfig struct {
	Agent              string `toml:"agent"`
	ReviewContextCount int    `toml:"review_context_count"`
	ReviewGuidelines   string `toml:"review_guidelines"`
	JobTimeoutMinutes  int    `toml:"job_timeout_minutes"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{
		ServerAddr:         "127.0.0.1:7373",
		MaxWorkers:         4,
		ReviewContextCount: 3,
		DefaultAgent:       "codex",
		JobTimeoutMinutes:  30,
		CodexCmd:           "codex",
		ClaudeCodeCmd:      "claude",
	}
}

// DataDir returns the roborev data directory.
// Uses ROBOREV_DATA_DIR env var if set, otherwise ~/.roborev
func DataDir() string {
	if dir := os.Getenv("ROBOREV_DATA_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".roborev")
}

// GlobalConfigPath returns the path to the global config file
func GlobalConfigPath() string {
	return filepath.Join(DataDir(), "config.toml")
}

// LoadGlobal loads the global configuration from the default path
func LoadGlobal() (*Config, error) {
	return LoadGlobalFrom(GlobalConfigPath())
}

// LoadGlobalFrom loads the global configuration from a specific path
func LoadGlobalFrom(path string) (*Config, error) {
	cfg := DefaultConfig()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadRepoConfig loads per-repo config from .roborev.toml
func LoadRepoConfig(repoPath string) (*RepoConfig, error) {
	path := filepath.Join(repoPath, ".roborev.toml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil // No repo config
	}

	var cfg RepoConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// ResolveAgent determines which agent to use based on config priority:
// 1. Explicit agent parameter (if non-empty)
// 2. Per-repo config
// 3. Global config
// 4. Default ("codex")
func ResolveAgent(explicit string, repoPath string, globalCfg *Config) string {
	if explicit != "" {
		return explicit
	}

	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.Agent != "" {
		return repoCfg.Agent
	}

	if globalCfg != nil && globalCfg.DefaultAgent != "" {
		return globalCfg.DefaultAgent
	}

	return "codex"
}

// ResolveJobTimeout determines job timeout based on config priority:
// 1. Per-repo config (if set and > 0)
// 2. Global config (if set and > 0)
// 3. Default (30 minutes)
func ResolveJobTimeout(repoPath string, globalCfg *Config) int {
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.JobTimeoutMinutes > 0 {
		return repoCfg.JobTimeoutMinutes
	}

	if globalCfg != nil && globalCfg.JobTimeoutMinutes > 0 {
		return globalCfg.JobTimeoutMinutes
	}

	return 30 // Default: 30 minutes
}

// SaveGlobal saves the global configuration
func SaveGlobal(cfg *Config) error {
	path := GlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewEncoder(f).Encode(cfg)
}
