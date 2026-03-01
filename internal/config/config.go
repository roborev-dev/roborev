package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/roborev-dev/roborev/internal/git"
)

// ConfigParseError is returned when .roborev.toml exists but
// contains invalid TOML. Callers can check with errors.As.
type ConfigParseError struct {
	Ref string
	Err error
}

func (e *ConfigParseError) Error() string {
	return fmt.Sprintf("parse .roborev.toml at %s: %v", e.Ref, e.Err)
}

func (e *ConfigParseError) Unwrap() error { return e.Err }

// IsConfigParseError reports whether err (or any error in its chain)
// is a ConfigParseError.
func IsConfigParseError(err error) bool {
	var pe *ConfigParseError
	return errors.As(err, &pe)
}

// HookConfig defines a hook that runs on review events
type HookConfig struct {
	Event   string `toml:"event"`   // "review.failed", "review.completed", "review.*"
	Command string `toml:"command"` // shell command with {var} templates
	Type    string `toml:"type"`    // "beads" for built-in, empty for command
}

// Config holds the daemon configuration
type Config struct {
	ServerAddr         string `toml:"server_addr"`
	MaxWorkers         int    `toml:"max_workers"`
	ReviewContextCount int    `toml:"review_context_count"`
	DefaultAgent       string `toml:"default_agent"`
	DefaultModel       string `toml:"default_model"` // Default model for agents (format varies by agent)
	DefaultBackupAgent string `toml:"default_backup_agent"`
	JobTimeoutMinutes  int    `toml:"job_timeout_minutes"`

	// Workflow-specific agent/model configuration
	ReviewAgent           string `toml:"review_agent"`
	ReviewAgentFast       string `toml:"review_agent_fast"`
	ReviewAgentStandard   string `toml:"review_agent_standard"`
	ReviewAgentThorough   string `toml:"review_agent_thorough"`
	RefineAgent           string `toml:"refine_agent"`
	RefineAgentFast       string `toml:"refine_agent_fast"`
	RefineAgentStandard   string `toml:"refine_agent_standard"`
	RefineAgentThorough   string `toml:"refine_agent_thorough"`
	ReviewModel           string `toml:"review_model"`
	ReviewModelFast       string `toml:"review_model_fast"`
	ReviewModelStandard   string `toml:"review_model_standard"`
	ReviewModelThorough   string `toml:"review_model_thorough"`
	RefineModel           string `toml:"refine_model"`
	RefineModelFast       string `toml:"refine_model_fast"`
	RefineModelStandard   string `toml:"refine_model_standard"`
	RefineModelThorough   string `toml:"refine_model_thorough"`
	FixAgent              string `toml:"fix_agent"`
	FixAgentFast          string `toml:"fix_agent_fast"`
	FixAgentStandard      string `toml:"fix_agent_standard"`
	FixAgentThorough      string `toml:"fix_agent_thorough"`
	FixModel              string `toml:"fix_model"`
	FixModelFast          string `toml:"fix_model_fast"`
	FixModelStandard      string `toml:"fix_model_standard"`
	FixModelThorough      string `toml:"fix_model_thorough"`
	SecurityAgent         string `toml:"security_agent"`
	SecurityAgentFast     string `toml:"security_agent_fast"`
	SecurityAgentStandard string `toml:"security_agent_standard"`
	SecurityAgentThorough string `toml:"security_agent_thorough"`
	SecurityModel         string `toml:"security_model"`
	SecurityModelFast     string `toml:"security_model_fast"`
	SecurityModelStandard string `toml:"security_model_standard"`
	SecurityModelThorough string `toml:"security_model_thorough"`
	DesignAgent           string `toml:"design_agent"`
	DesignAgentFast       string `toml:"design_agent_fast"`
	DesignAgentStandard   string `toml:"design_agent_standard"`
	DesignAgentThorough   string `toml:"design_agent_thorough"`
	DesignModel           string `toml:"design_model"`
	DesignModelFast       string `toml:"design_model_fast"`
	DesignModelStandard   string `toml:"design_model_standard"`
	DesignModelThorough   string `toml:"design_model_thorough"`

	// Backup agents for failover
	ReviewBackupAgent   string `toml:"review_backup_agent"`
	RefineBackupAgent   string `toml:"refine_backup_agent"`
	FixBackupAgent      string `toml:"fix_backup_agent"`
	SecurityBackupAgent string `toml:"security_backup_agent"`
	DesignBackupAgent   string `toml:"design_backup_agent"`

	AllowUnsafeAgents *bool `toml:"allow_unsafe_agents"` // nil = not set, allows commands to choose their own default

	// Agent commands
	CodexCmd      string `toml:"codex_cmd"`
	ClaudeCodeCmd string `toml:"claude_code_cmd"`
	CursorCmd     string `toml:"cursor_cmd"`

	// API keys (optional - agents use subscription auth by default)
	AnthropicAPIKey string `toml:"anthropic_api_key" sensitive:"true"`

	// Hooks configuration
	Hooks []HookConfig `toml:"hooks"`

	// Sync configuration for PostgreSQL
	Sync SyncConfig `toml:"sync"`

	// CI poller configuration
	CI CIConfig `toml:"ci"`

	// Analysis settings
	DefaultMaxPromptSize int `toml:"default_max_prompt_size"` // Max prompt size in bytes before falling back to paths (default: 200KB)

	// UI preferences
	HideAddressedByDefault bool     `toml:"hide_addressed_by_default"`
	AutoFilterRepo         bool     `toml:"auto_filter_repo"`
	TabWidth               int      `toml:"tab_width"`         // Tab expansion width for TUI rendering (default: 2)
	HiddenColumns          []string `toml:"hidden_columns"`    // Column names to hide in queue table (e.g. ["branch", "agent"])
	ColumnBorders          bool     `toml:"column_borders"`    // Show ▕ separators between columns
	ColumnOrder            []string `toml:"column_order"`      // Custom queue column display order
	TaskColumnOrder        []string `toml:"task_column_order"` // Custom task column display order
}

// GitHubAppConfig holds GitHub App authentication settings.
// Extracted from CIConfig for cohesion; embedded so TOML keys remain flat under [ci].
type GitHubAppConfig struct {
	GitHubAppID             int64  `toml:"github_app_id"`
	GitHubAppPrivateKey     string `toml:"github_app_private_key" sensitive:"true"` // PEM file path or inline; supports ${ENV_VAR}
	GitHubAppInstallationID int64  `toml:"github_app_installation_id"`

	// Multi-installation: map of owner → installation_id
	GitHubAppInstallations map[string]int64 `toml:"github_app_installations"`
}

// GitHubAppConfigured returns true if GitHub App authentication can be used.
// Requires app ID, private key, and at least one installation ID (singular or map).
func (c *GitHubAppConfig) GitHubAppConfigured() bool {
	return c.GitHubAppID != 0 && c.GitHubAppPrivateKey != "" &&
		(c.GitHubAppInstallationID != 0 || len(c.GitHubAppInstallations) > 0)
}

// InstallationIDForOwner returns the installation ID for a GitHub owner.
// Checks the normalized installations map first (skipping non-positive values),
// then falls back to the singular field. Owner comparison is case-insensitive.
func (c *GitHubAppConfig) InstallationIDForOwner(owner string) int64 {
	if id, ok := c.GitHubAppInstallations[strings.ToLower(owner)]; ok && id > 0 {
		return id
	}
	return c.GitHubAppInstallationID
}

// NormalizeInstallations lowercases all keys in GitHubAppInstallations
// so lookups are case-insensitive via direct map access.
// Returns an error if two keys collide after lowercasing (e.g., "wesm" and "Wesm").
func (c *GitHubAppConfig) NormalizeInstallations() error {
	if len(c.GitHubAppInstallations) == 0 {
		return nil
	}
	normalized := make(map[string]int64, len(c.GitHubAppInstallations))
	for k, v := range c.GitHubAppInstallations {
		lower := strings.ToLower(k)
		if _, exists := normalized[lower]; exists {
			return fmt.Errorf("case-colliding github_app_installations keys for %q", lower)
		}
		normalized[lower] = v
	}
	c.GitHubAppInstallations = normalized
	return nil
}

// GitHubAppPrivateKeyResolved expands env vars in the private key value,
// reads the file if it's a path, and returns the PEM content.
func (c *GitHubAppConfig) GitHubAppPrivateKeyResolved() (string, error) {
	val := os.ExpandEnv(c.GitHubAppPrivateKey)
	if val == "" {
		return "", fmt.Errorf("github_app_private_key is empty after expansion")
	}

	// If it looks like PEM content, return directly
	// TrimSpace handles leading whitespace/newlines in inline PEM content
	trimmed := strings.TrimSpace(val)
	if strings.HasPrefix(trimmed, "-----BEGIN") {
		return trimmed, nil
	}

	// Expand leading ~ to home directory
	if strings.HasPrefix(val, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home for github_app_private_key: %w", err)
		}
		val = home + val[1:]
	}

	// Otherwise treat as file path
	data, err := os.ReadFile(val)
	if err != nil {
		return "", fmt.Errorf("read private key file %s: %w", val, err)
	}
	return string(data), nil
}

// CIConfig holds configuration for the CI poller that watches GitHub PRs
type CIConfig struct {
	// Enabled enables the CI poller
	Enabled bool `toml:"enabled"`

	// PollInterval is how often to poll for PRs (e.g., "5m", "10m"). Default: 5m
	PollInterval string `toml:"poll_interval"`

	// Repos is the list of GitHub repos to poll in "owner/repo" format.
	// Supports glob patterns (e.g., "myorg/*", "myorg/api-*") using path.Match syntax.
	// The owner part must be literal — no wildcards before the "/".
	Repos []string `toml:"repos"`

	// ExcludeRepos is a list of glob patterns to exclude from the resolved repo list.
	// Applies to both exact entries and wildcard-expanded entries.
	ExcludeRepos []string `toml:"exclude_repos"`

	// MaxRepos is a safety cap on the total number of expanded repos. Default: 100.
	MaxRepos int `toml:"max_repos"`

	// ReviewTypes is the list of review types to run for each PR (e.g., ["security", "default"]).
	// Defaults to ["security"] if empty.
	ReviewTypes []string `toml:"review_types"`

	// Agents is the list of agents to run for each PR (e.g., ["codex", "gemini"]).
	// Defaults to auto-detection if empty.
	Agents []string `toml:"agents"`

	// Model overrides the model for CI reviews (empty = use workflow resolution)
	Model string `toml:"model"`

	// SynthesisAgent is the agent used to synthesize multiple review outputs into one comment.
	// Defaults to the first available agent.
	SynthesisAgent string `toml:"synthesis_agent"`

	// SynthesisModel overrides the model used for synthesis.
	SynthesisModel string `toml:"synthesis_model"`

	// MinSeverity filters out findings below this severity level during synthesis.
	// Valid values: critical, high, medium, low. Empty means no filter (include all).
	MinSeverity string `toml:"min_severity"`

	// GitHub App authentication (optional — comments appear as bot instead of personal account)
	GitHubAppConfig
}

// ResolvedReviewTypes returns the list of review types to use.
// Defaults to ["security"] if empty.
func (c *CIConfig) ResolvedReviewTypes() []string {
	if len(c.ReviewTypes) > 0 {
		return c.ReviewTypes
	}
	return []string{ReviewTypeSecurity}
}

// ResolvedAgents returns the list of agents to use.
// Defaults to [""] (empty = auto-detect) if empty.
func (c *CIConfig) ResolvedAgents() []string {
	if len(c.Agents) > 0 {
		return c.Agents
	}
	return []string{""}
}

// ResolvedMaxRepos returns the maximum number of repos to poll.
// Defaults to 100 if not set or non-positive.
func (c *CIConfig) ResolvedMaxRepos() int {
	if c.MaxRepos > 0 {
		return c.MaxRepos
	}
	return 100
}

// SyncConfig holds configuration for PostgreSQL sync
type SyncConfig struct {
	// Enabled enables sync to PostgreSQL
	Enabled bool `toml:"enabled"`

	// PostgresURL is the connection string for PostgreSQL.
	// Supports environment variable expansion via ${VAR} syntax.
	PostgresURL string `toml:"postgres_url" sensitive:"true"`

	// Interval is how often to sync (e.g., "5m", "1h"). Default: 1h
	Interval string `toml:"interval"`

	// MachineName is a friendly name for this machine (optional)
	MachineName string `toml:"machine_name"`

	// ConnectTimeout is the connection timeout (e.g., "5s"). Default: 5s
	ConnectTimeout string `toml:"connect_timeout"`

	// RepoNames provides custom display names for synced repos by identity.
	// Example: {"git@github.com:org/repo.git": "my-project"}
	RepoNames map[string]string `toml:"repo_names"`
}

// PostgresURLExpanded returns the PostgreSQL URL with environment variables expanded.
// Returns empty string if URL is not set.
func (c *SyncConfig) PostgresURLExpanded() string {
	if c.PostgresURL == "" {
		return ""
	}
	return os.ExpandEnv(c.PostgresURL)
}

// GetRepoDisplayName returns the configured display name for a repo identity,
// or empty string if no override is configured.
func (c *SyncConfig) GetRepoDisplayName(identity string) string {
	if c == nil || c.RepoNames == nil {
		return ""
	}
	return c.RepoNames[identity]
}

// Validate checks the sync configuration for common issues.
// Returns a list of warnings (non-fatal issues).
func (c *SyncConfig) Validate() []string {
	var warnings []string

	if !c.Enabled {
		return warnings
	}

	if c.PostgresURL == "" {
		warnings = append(warnings, "sync.enabled is true but sync.postgres_url is not set")
		return warnings
	}

	// Check for environment variable references where the var is not set
	// os.ExpandEnv replaces ${VAR} with empty string if VAR is not set
	if strings.Contains(c.PostgresURL, "${") {
		re := regexp.MustCompile(`\$\{([^}]+)\}`)
		matches := re.FindAllStringSubmatch(c.PostgresURL, -1)
		for _, match := range matches {
			if len(match) > 1 {
				varName := match[1]
				if os.Getenv(varName) == "" {
					warnings = append(warnings, "sync.postgres_url may contain unexpanded environment variables")
					break // Only one warning needed
				}
			}
		}
	}

	return warnings
}

// RepoCIConfig holds per-repo CI overrides (used by the CI poller for this repo).
// These override the global [ci] settings when reviewing this specific repo.
type RepoCIConfig struct {
	// Agents overrides the list of agents for CI reviews of this repo.
	Agents []string `toml:"agents"`

	// ReviewTypes overrides the list of review types for CI reviews of this repo.
	ReviewTypes []string `toml:"review_types"`

	// Reasoning overrides the reasoning level for CI reviews (thorough, standard, fast).
	Reasoning string `toml:"reasoning"`

	// MinSeverity overrides the minimum severity filter for CI synthesis.
	MinSeverity string `toml:"min_severity"`
}

// RepoConfig holds per-repo overrides
type RepoConfig struct {
	Agent              string   `toml:"agent"`
	Model              string   `toml:"model"` // Model for agents (format varies by agent)
	BackupAgent        string   `toml:"backup_agent"`
	ReviewContextCount int      `toml:"review_context_count"`
	ReviewGuidelines   string   `toml:"review_guidelines"`
	JobTimeoutMinutes  int      `toml:"job_timeout_minutes"`
	ExcludedBranches   []string `toml:"excluded_branches"`
	DisplayName        string   `toml:"display_name"`
	ReviewReasoning    string   `toml:"review_reasoning"` // Reasoning level for reviews: thorough, standard, fast
	RefineReasoning    string   `toml:"refine_reasoning"` // Reasoning level for refine: thorough, standard, fast
	FixReasoning       string   `toml:"fix_reasoning"`    // Reasoning level for fix: thorough, standard, fast

	// CI-specific overrides (used by CI poller for this repo)
	CI RepoCIConfig `toml:"ci"`

	// Workflow-specific agent/model configuration
	ReviewAgent           string `toml:"review_agent"`
	ReviewAgentFast       string `toml:"review_agent_fast"`
	ReviewAgentStandard   string `toml:"review_agent_standard"`
	ReviewAgentThorough   string `toml:"review_agent_thorough"`
	RefineAgent           string `toml:"refine_agent"`
	RefineAgentFast       string `toml:"refine_agent_fast"`
	RefineAgentStandard   string `toml:"refine_agent_standard"`
	RefineAgentThorough   string `toml:"refine_agent_thorough"`
	ReviewModel           string `toml:"review_model"`
	ReviewModelFast       string `toml:"review_model_fast"`
	ReviewModelStandard   string `toml:"review_model_standard"`
	ReviewModelThorough   string `toml:"review_model_thorough"`
	RefineModel           string `toml:"refine_model"`
	RefineModelFast       string `toml:"refine_model_fast"`
	RefineModelStandard   string `toml:"refine_model_standard"`
	RefineModelThorough   string `toml:"refine_model_thorough"`
	FixAgent              string `toml:"fix_agent"`
	FixAgentFast          string `toml:"fix_agent_fast"`
	FixAgentStandard      string `toml:"fix_agent_standard"`
	FixAgentThorough      string `toml:"fix_agent_thorough"`
	FixModel              string `toml:"fix_model"`
	FixModelFast          string `toml:"fix_model_fast"`
	FixModelStandard      string `toml:"fix_model_standard"`
	FixModelThorough      string `toml:"fix_model_thorough"`
	SecurityAgent         string `toml:"security_agent"`
	SecurityAgentFast     string `toml:"security_agent_fast"`
	SecurityAgentStandard string `toml:"security_agent_standard"`
	SecurityAgentThorough string `toml:"security_agent_thorough"`
	SecurityModel         string `toml:"security_model"`
	SecurityModelFast     string `toml:"security_model_fast"`
	SecurityModelStandard string `toml:"security_model_standard"`
	SecurityModelThorough string `toml:"security_model_thorough"`
	DesignAgent           string `toml:"design_agent"`
	DesignAgentFast       string `toml:"design_agent_fast"`
	DesignAgentStandard   string `toml:"design_agent_standard"`
	DesignAgentThorough   string `toml:"design_agent_thorough"`
	DesignModel           string `toml:"design_model"`
	DesignModelFast       string `toml:"design_model_fast"`
	DesignModelStandard   string `toml:"design_model_standard"`
	DesignModelThorough   string `toml:"design_model_thorough"`

	// Backup agents for failover
	ReviewBackupAgent   string `toml:"review_backup_agent"`
	RefineBackupAgent   string `toml:"refine_backup_agent"`
	FixBackupAgent      string `toml:"fix_backup_agent"`
	SecurityBackupAgent string `toml:"security_backup_agent"`
	DesignBackupAgent   string `toml:"design_backup_agent"`

	// Hooks configuration (per-repo)
	Hooks []HookConfig `toml:"hooks"`

	// Analysis settings
	MaxPromptSize int `toml:"max_prompt_size"` // Max prompt size in bytes before falling back to paths (overrides global default)
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
		CursorCmd:          "agent",
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

	if err := cfg.CI.NormalizeInstallations(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
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

// LoadRepoConfigFromRef loads per-repo config from .roborev.toml at a
// specific git ref (e.g., a commit SHA or "origin/main"). Returns
// (nil, nil) if the file doesn't exist at that ref. Returns an error
// for unexpected git failures (bad repo, corrupted objects, etc.).
func LoadRepoConfigFromRef(repoPath, ref string) (*RepoConfig, error) {
	data, err := git.ReadFile(repoPath, ref, ".roborev.toml")
	if err != nil {
		errMsg := err.Error()
		// git show emits these specific patterns when the path is missing:
		//   "path '...' does not exist in '...'"
		//   "path '...' exists on disk, but not in '...'"
		if strings.Contains(errMsg, "does not exist in") ||
			strings.Contains(errMsg, "exists on disk, but not in") {
			return nil, nil
		}
		return nil, fmt.Errorf("read .roborev.toml at %s: %w", ref, err)
	}

	var cfg RepoConfig
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, &ConfigParseError{Ref: ref, Err: err}
	}
	return &cfg, nil
}

// resolve returns the first non-zero value from the candidates, or defaultVal
// if all candidates are zero. This encapsulates the standard precedence logic
// (explicit > repo > global > default) used throughout config resolution.
func resolve[T comparable](defaultVal T, candidates ...T) T {
	var zero T
	for _, v := range candidates {
		if v != zero {
			return v
		}
	}
	return defaultVal
}

// ResolveAgent determines which agent to use based on config priority:
// 1. Explicit agent parameter (if non-empty)
// 2. Per-repo config
// 3. Global config
// 4. Default ("codex")
func ResolveAgent(explicit string, repoPath string, globalCfg *Config) string {
	var repoVal string
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		repoVal = repoCfg.Agent
	}
	var globalVal string
	if globalCfg != nil {
		globalVal = globalCfg.DefaultAgent
	}
	return resolve("codex", explicit, repoVal, globalVal)
}

// clampPositive returns v if v > 0, otherwise 0.
func clampPositive(v int) int {
	if v > 0 {
		return v
	}
	return 0
}

// ResolveJobTimeout determines job timeout based on config priority:
// 1. Per-repo config (if set and > 0)
// 2. Global config (if set and > 0)
// 3. Default (30 minutes)
func ResolveJobTimeout(repoPath string, globalCfg *Config) int {
	var repoVal int
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		repoVal = clampPositive(repoCfg.JobTimeoutMinutes)
	}
	var globalVal int
	if globalCfg != nil {
		globalVal = clampPositive(globalCfg.JobTimeoutMinutes)
	}
	return resolve(30, repoVal, globalVal)
}

// IsBranchExcluded checks if a branch should be excluded from reviews
func IsBranchExcluded(repoPath, branch string) bool {
	repoCfg, err := LoadRepoConfig(repoPath)
	if err != nil || repoCfg == nil {
		return false
	}

	return slices.Contains(repoCfg.ExcludedBranches, branch)
}

// GetDisplayName returns the display name for a repo, or empty if not set
func GetDisplayName(repoPath string) string {
	repoCfg, err := LoadRepoConfig(repoPath)
	if err != nil || repoCfg == nil {
		return ""
	}
	return repoCfg.DisplayName
}

// Canonical review type names.
const (
	ReviewTypeDefault  = "default"
	ReviewTypeSecurity = "security"
	ReviewTypeDesign   = "design"
)

// IsDefaultReviewType returns true if the review type represents the standard
// (non-specialized) code review. The canonical name is "default"; "general"
// and "review" are accepted as backward-compatible aliases.
func IsDefaultReviewType(rt string) bool {
	return rt == "" || rt == ReviewTypeDefault ||
		rt == "general" || rt == "review"
}

// ValidateReviewTypes canonicalizes, validates, and deduplicates
// a list of review type strings. Aliases ("general", "review")
// are normalized to "default". Returns an error if any type is
// empty or unrecognized.
func ValidateReviewTypes(types []string) ([]string, error) {
	validSpecial := map[string]bool{
		ReviewTypeSecurity: true,
		ReviewTypeDesign:   true,
	}
	seen := make(map[string]bool, len(types))
	canonical := make([]string, 0, len(types))
	for _, rt := range types {
		if rt == "" {
			return nil, fmt.Errorf(
				"invalid review_type %q "+
					"(valid: default, security, design)", rt)
		}
		if IsDefaultReviewType(rt) {
			rt = ReviewTypeDefault
		} else if !validSpecial[rt] {
			return nil, fmt.Errorf(
				"invalid review_type %q "+
					"(valid: default, security, design)", rt)
		}
		if !seen[rt] {
			seen[rt] = true
			canonical = append(canonical, rt)
		}
	}
	return canonical, nil
}

// NormalizeReasoning validates and normalizes a reasoning level string.
// Returns the canonical form (thorough, standard, fast) or an error if invalid.
// Returns empty string (no error) for empty input.
func NormalizeReasoning(value string) (string, error) {
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

// NormalizeMinSeverity validates and normalizes a minimum severity level string.
// Returns the canonical form (critical, high, medium, low) or an error if invalid.
// Returns empty string (no error) for empty input.
func NormalizeMinSeverity(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "", nil
	}

	switch normalized {
	case "critical", "high", "medium", "low":
		return normalized, nil
	default:
		return "", fmt.Errorf("invalid min_severity level: %q (valid: critical, high, medium, low)", value)
	}
}

// ResolveReviewReasoning determines reasoning level for reviews.
// Priority: explicit > per-repo config > default (thorough)
func ResolveReviewReasoning(explicit string, repoPath string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return NormalizeReasoning(explicit)
	}

	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && strings.TrimSpace(repoCfg.ReviewReasoning) != "" {
		return NormalizeReasoning(repoCfg.ReviewReasoning)
	}

	return "thorough", nil // Default for reviews: deep analysis
}

// ResolveRefineReasoning determines reasoning level for refine.
// Priority: explicit > per-repo config > default (standard)
func ResolveRefineReasoning(explicit string, repoPath string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return NormalizeReasoning(explicit)
	}

	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && strings.TrimSpace(repoCfg.RefineReasoning) != "" {
		return NormalizeReasoning(repoCfg.RefineReasoning)
	}

	return "standard", nil // Default for refine: balanced analysis
}

// ResolveFixReasoning determines reasoning level for fix.
// Priority: explicit > per-repo config > default (standard)
func ResolveFixReasoning(explicit string, repoPath string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return NormalizeReasoning(explicit)
	}

	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && strings.TrimSpace(repoCfg.FixReasoning) != "" {
		return NormalizeReasoning(repoCfg.FixReasoning)
	}

	return "standard", nil // Default for fix: balanced analysis
}

// ResolveModel determines which model to use based on config priority:
// 1. Explicit model parameter (if non-empty)
// 2. Per-repo config (model in .roborev.toml)
// 3. Global config (default_model in config.toml)
// 4. Default (empty string, agent uses its default)
func ResolveModel(explicit string, repoPath string, globalCfg *Config) string {
	var repoVal string
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		repoVal = strings.TrimSpace(repoCfg.Model)
	}
	var globalVal string
	if globalCfg != nil {
		globalVal = strings.TrimSpace(globalCfg.DefaultModel)
	}
	return resolve("", strings.TrimSpace(explicit), repoVal, globalVal)
}

// DefaultMaxPromptSize is the default maximum prompt size in bytes (200KB)
const DefaultMaxPromptSize = 200 * 1024

// ResolveMaxPromptSize determines the maximum prompt size based on config priority:
// 1. Per-repo config (max_prompt_size in .roborev.toml)
// 2. Global config (default_max_prompt_size in config.toml)
// 3. Default (200KB)
func ResolveMaxPromptSize(repoPath string, globalCfg *Config) int {
	var repoVal int
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
		repoVal = clampPositive(repoCfg.MaxPromptSize)
	}
	var globalVal int
	if globalCfg != nil {
		globalVal = clampPositive(globalCfg.DefaultMaxPromptSize)
	}
	return resolve(DefaultMaxPromptSize, repoVal, globalVal)
}

// ResolveAgentForWorkflow determines which agent to use based on workflow and level.
// Priority (Option A - layer wins first, then specificity):
// 1. CLI explicit
// 2. Repo {workflow}_agent_{level}
// 3. Repo {workflow}_agent
// 4. Repo agent
// 5. Global {workflow}_agent_{level}
// 6. Global {workflow}_agent
// 7. Global default_agent
// 8. "codex"
func ResolveAgentForWorkflow(cli, repoPath string, globalCfg *Config, workflow, level string) string {
	if s := strings.TrimSpace(cli); s != "" {
		return s
	}
	repoCfg, _ := LoadRepoConfig(repoPath)
	if s := getWorkflowValue(repoCfg, globalCfg, workflow, level, true); s != "" {
		return s
	}
	return "codex"
}

// ResolveModelForWorkflow determines which model to use based on workflow and level.
// Same priority as ResolveAgentForWorkflow, but returns empty string as default.
func ResolveModelForWorkflow(cli, repoPath string, globalCfg *Config, workflow, level string) string {
	if s := strings.TrimSpace(cli); s != "" {
		return s
	}
	repoCfg, _ := LoadRepoConfig(repoPath)
	return getWorkflowValue(repoCfg, globalCfg, workflow, level, false)
}

// ResolveBackupAgentForWorkflow returns the backup agent for a workflow,
// or empty string if none is configured.
// Priority:
//  1. Repo {workflow}_backup_agent
//  2. Repo backup_agent (generic)
//  3. Global {workflow}_backup_agent
//  4. Global default_backup_agent
//  5. "" (no backup)
func ResolveBackupAgentForWorkflow(repoPath string, globalCfg *Config, workflow string) string {
	repoCfg, _ := LoadRepoConfig(repoPath)

	// Repo layer: workflow-specific > generic
	if repoCfg != nil {
		if s := lookupFieldByTag(reflect.ValueOf(*repoCfg), workflow+"_backup_agent"); s != "" {
			return s
		}
		if s := strings.TrimSpace(repoCfg.BackupAgent); s != "" {
			return s
		}
	}

	// Global layer: workflow-specific > default
	if globalCfg != nil {
		if s := lookupFieldByTag(reflect.ValueOf(*globalCfg), workflow+"_backup_agent"); s != "" {
			return s
		}
		if s := strings.TrimSpace(globalCfg.DefaultBackupAgent); s != "" {
			return s
		}
	}

	return ""
}

// lookupFieldByTag finds a struct field by its TOML tag and returns its trimmed value.
func lookupFieldByTag(v reflect.Value, key string) string {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("toml")
		if tag == "" {
			continue
		}
		if strings.Split(tag, ",")[0] == key {
			return strings.TrimSpace(v.Field(i).String())
		}
	}
	return ""
}

// getWorkflowValue looks up agent or model config following Option A priority.
func getWorkflowValue(repo *RepoConfig, global *Config, workflow, level string, isAgent bool) string {
	// Repo layer: level-specific > workflow-specific > generic
	if repo != nil {
		if s := repoWorkflowField(repo, workflow, level, isAgent); s != "" {
			return s
		}
		if s := repoWorkflowField(repo, workflow, "", isAgent); s != "" {
			return s
		}
		if isAgent && strings.TrimSpace(repo.Agent) != "" {
			return strings.TrimSpace(repo.Agent)
		}
		if !isAgent && strings.TrimSpace(repo.Model) != "" {
			return strings.TrimSpace(repo.Model)
		}
	}
	// Global layer: level-specific > workflow-specific > generic
	if global != nil {
		if s := globalWorkflowField(global, workflow, level, isAgent); s != "" {
			return s
		}
		if s := globalWorkflowField(global, workflow, "", isAgent); s != "" {
			return s
		}
		if isAgent && strings.TrimSpace(global.DefaultAgent) != "" {
			return strings.TrimSpace(global.DefaultAgent)
		}
		if !isAgent && strings.TrimSpace(global.DefaultModel) != "" {
			return strings.TrimSpace(global.DefaultModel)
		}
	}
	return ""
}

// workflowFieldKey builds the TOML key for a workflow field lookup.
// Examples: workflowFieldKey("review", "fast", true) => "review_agent_fast"
//
//	workflowFieldKey("review", "", true) => "review_agent"
func workflowFieldKey(workflow, level string, isAgent bool) string {
	kind := "model"
	if isAgent {
		kind = "agent"
	}
	if level == "" {
		return workflow + "_" + kind
	}
	return workflow + "_" + kind + "_" + level
}

// lookupWorkflowField retrieves a workflow field value from any struct using
// reflection and TOML tags. This replaces the former repoWorkflowField and
// globalWorkflowField switch statements with a single, tag-driven lookup that
// automatically supports new workflows/levels when fields are added.
func lookupWorkflowField(v reflect.Value, workflow, level string, isAgent bool) string {
	key := workflowFieldKey(workflow, level, isAgent)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("toml")
		if tag == "" {
			continue
		}
		if strings.Split(tag, ",")[0] == key {
			return strings.TrimSpace(v.Field(i).String())
		}
	}
	return ""
}

func repoWorkflowField(r *RepoConfig, workflow, level string, isAgent bool) string {
	if r == nil {
		return ""
	}
	return lookupWorkflowField(reflect.ValueOf(*r), workflow, level, isAgent)
}

func globalWorkflowField(g *Config, workflow, level string, isAgent bool) string {
	if g == nil {
		return ""
	}
	return lookupWorkflowField(reflect.ValueOf(*g), workflow, level, isAgent)
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

// roborevIDPattern validates .roborev-id content.
// Must start with alphanumeric, then allows alphanumeric, dots, underscores, hyphens, colons, slashes, at-signs.
var roborevIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:@/-]*$`)

const roborevIDMaxLength = 256

// ValidateReporevID validates the content of a .roborev-id file.
// Returns empty string if valid, or an error message if invalid.
func ValidateRoborevID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "empty after trimming whitespace"
	}
	if len(id) > roborevIDMaxLength {
		return fmt.Sprintf("exceeds max length of %d characters", roborevIDMaxLength)
	}
	if !roborevIDPattern.MatchString(id) {
		return "invalid characters (must start with alphanumeric, then alphanumeric/._:@/-)"
	}
	return ""
}

// ReadRoborevID reads and validates the .roborev-id file from a repo.
// Returns the ID if valid, empty string if file doesn't exist or is invalid.
// If invalid, the error describes why.
func ReadRoborevID(repoPath string) (string, error) {
	path := filepath.Join(repoPath, ".roborev-id")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read .roborev-id: %w", err)
	}

	id := strings.TrimSpace(string(content))
	if validationErr := ValidateRoborevID(id); validationErr != "" {
		return "", fmt.Errorf("invalid .roborev-id: %s", validationErr)
	}
	return id, nil
}

// ResolveRepoIdentity determines the unique identity for a repository.
// Resolution order:
// 1. .roborev-id file in repo root (if exists and valid)
// 2. Git remote "origin" URL
// 3. Any git remote URL
// 4. Fallback: local://{absolute_path}
//
// Note: Credentials are stripped from git remote URLs to prevent secrets from
// being persisted in the database or synced to PostgreSQL.
//
// The getRemoteURL parameter allows injection of git remote lookup for testing.
// Pass nil to use the default git.GetRemoteURL function.
func ResolveRepoIdentity(repoPath string, getRemoteURL func(repoPath, remoteName string) string) string {
	// 1. Try .roborev-id file
	id, err := ReadRoborevID(repoPath)
	if err == nil && id != "" {
		return id
	}
	// If .roborev-id exists but is invalid, fall through (logged at call site if needed)

	// 2 & 3. Try git remote URL (origin first, then any)
	if getRemoteURL == nil {
		getRemoteURL = git.GetRemoteURL
	}
	remoteURL := getRemoteURL(repoPath, "")
	if remoteURL != "" {
		// Strip credentials from URL to avoid persisting secrets
		return stripURLCredentials(remoteURL)
	}

	// 4. Fallback to local path
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		absPath = repoPath
	}
	return "local://" + absPath
}

// stripURLCredentials removes userinfo (username:password) from a URL.
// For non-URL strings (e.g., SSH URLs like git@github.com:user/repo.git),
// returns the original string unchanged.
func stripURLCredentials(rawURL string) string {
	// Try to parse as a standard URL
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// Not a valid URL, return as-is
		return rawURL
	}

	// If there's no scheme, it's likely an SCP-style URL (git@host:repo.git).
	// Strip any credentials (user:pass@host:repo → host:repo).
	if parsed.Scheme == "" {
		if _, after, ok := strings.Cut(rawURL, "@"); ok {
			return after
		}
		return rawURL
	}

	// If there's no userinfo, return as-is
	if parsed.User == nil {
		return rawURL
	}

	// Clear the userinfo
	parsed.User = nil
	return parsed.String()
}
