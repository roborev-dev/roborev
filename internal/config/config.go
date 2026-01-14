package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config holds the daemon configuration
type Config struct {
	ServerAddr         string `toml:"server_addr"`
	MaxWorkers         int    `toml:"max_workers"`
	ReviewContextCount int    `toml:"review_context_count"`
	DefaultAgent       string `toml:"default_agent"`
	JobTimeoutMinutes  int    `toml:"job_timeout_minutes"`
	AllowUnsafeAgents  bool   `toml:"allow_unsafe_agents"`

	// Agent commands
	CodexCmd      string `toml:"codex_cmd"`
	ClaudeCodeCmd string `toml:"claude_code_cmd"`
}

// RepoConfig holds per-repo overrides
type RepoConfig struct {
	Agent              string   `toml:"agent"`
	ReviewContextCount int      `toml:"review_context_count"`
	ReviewGuidelines   string   `toml:"review_guidelines"`
	JobTimeoutMinutes  int      `toml:"job_timeout_minutes"`
	ExcludedBranches   []string `toml:"excluded_branches"`
	DisplayName        string   `toml:"display_name"`
	ReviewReasoning    string   `toml:"review_reasoning"` // Reasoning level for reviews: thorough, standard, fast
	RefineReasoning    string   `toml:"refine_reasoning"` // Reasoning level for refine: thorough, standard, fast
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

// IsBranchExcluded checks if a branch should be excluded from reviews
func IsBranchExcluded(repoPath, branch string) bool {
	repoCfg, err := LoadRepoConfig(repoPath)
	if err != nil || repoCfg == nil {
		return false
	}

	for _, excluded := range repoCfg.ExcludedBranches {
		if excluded == branch {
			return true
		}
	}
	return false
}

// GetDisplayName returns the display name for a repo, or empty if not set
func GetDisplayName(repoPath string) string {
	repoCfg, err := LoadRepoConfig(repoPath)
	if err != nil || repoCfg == nil {
		return ""
	}
	return repoCfg.DisplayName
}

func normalizeReasoning(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "", nil
	}

	switch normalized {
	case "thorough", "high":
		return "thorough", nil
	case "standard", "medium":
		return "standard", nil
	case "fast", "low":
		return "fast", nil
	default:
		return "", fmt.Errorf("invalid reasoning level: %q", value)
	}
}

// ResolveReviewReasoning determines reasoning level for reviews.
// Priority: explicit > per-repo config > default (thorough)
func ResolveReviewReasoning(explicit string, repoPath string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return normalizeReasoning(explicit)
	}

	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && strings.TrimSpace(repoCfg.ReviewReasoning) != "" {
		return normalizeReasoning(repoCfg.ReviewReasoning)
	}

	return "thorough", nil // Default for reviews: deep analysis
}

// ResolveRefineReasoning determines reasoning level for refine.
// Priority: explicit > per-repo config > default (standard)
func ResolveRefineReasoning(explicit string, repoPath string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return normalizeReasoning(explicit)
	}

	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && strings.TrimSpace(repoCfg.RefineReasoning) != "" {
		return normalizeReasoning(repoCfg.RefineReasoning)
	}

	return "standard", nil // Default for refine: balanced analysis
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
