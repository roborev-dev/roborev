package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/roborev-dev/roborev/internal/git"
)

// Config holds the daemon configuration
type Config struct {
	ServerAddr         string `toml:"server_addr"`
	MaxWorkers         int    `toml:"max_workers"`
	ReviewContextCount int    `toml:"review_context_count"`
	DefaultAgent       string `toml:"default_agent"`
	DefaultModel       string `toml:"default_model"` // Default model for agents (format varies by agent)
	JobTimeoutMinutes  int    `toml:"job_timeout_minutes"`

	// Workflow-specific agent/model configuration
	ReviewAgent         string `toml:"review_agent"`
	ReviewAgentFast     string `toml:"review_agent_fast"`
	ReviewAgentStandard string `toml:"review_agent_standard"`
	ReviewAgentThorough string `toml:"review_agent_thorough"`
	RefineAgent         string `toml:"refine_agent"`
	RefineAgentFast     string `toml:"refine_agent_fast"`
	RefineAgentStandard string `toml:"refine_agent_standard"`
	RefineAgentThorough string `toml:"refine_agent_thorough"`
	ReviewModel         string `toml:"review_model"`
	ReviewModelFast     string `toml:"review_model_fast"`
	ReviewModelStandard string `toml:"review_model_standard"`
	ReviewModelThorough string `toml:"review_model_thorough"`
	RefineModel         string `toml:"refine_model"`
	RefineModelFast     string `toml:"refine_model_fast"`
	RefineModelStandard string `toml:"refine_model_standard"`
	RefineModelThorough string `toml:"refine_model_thorough"`
	AllowUnsafeAgents  *bool  `toml:"allow_unsafe_agents"` // nil = not set, allows commands to choose their own default

	// Agent commands
	CodexCmd      string `toml:"codex_cmd"`
	ClaudeCodeCmd string `toml:"claude_code_cmd"`

	// API keys (optional - agents use subscription auth by default)
	AnthropicAPIKey string `toml:"anthropic_api_key"`

	// Sync configuration for PostgreSQL
	Sync SyncConfig `toml:"sync"`

	// Analysis settings
	DefaultMaxPromptSize int `toml:"default_max_prompt_size"` // Max prompt size in bytes before falling back to paths (default: 200KB)
}

// SyncConfig holds configuration for PostgreSQL sync
type SyncConfig struct {
	// Enabled enables sync to PostgreSQL
	Enabled bool `toml:"enabled"`

	// PostgresURL is the connection string for PostgreSQL.
	// Supports environment variable expansion via ${VAR} syntax.
	PostgresURL string `toml:"postgres_url"`

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

// RepoConfig holds per-repo overrides
type RepoConfig struct {
	Agent              string   `toml:"agent"`
	Model              string   `toml:"model"` // Model for agents (format varies by agent)
	ReviewContextCount int      `toml:"review_context_count"`
	ReviewGuidelines   string   `toml:"review_guidelines"`
	JobTimeoutMinutes  int      `toml:"job_timeout_minutes"`
	ExcludedBranches   []string `toml:"excluded_branches"`
	DisplayName        string   `toml:"display_name"`
	ReviewReasoning    string   `toml:"review_reasoning"` // Reasoning level for reviews: thorough, standard, fast
	RefineReasoning    string   `toml:"refine_reasoning"` // Reasoning level for refine: thorough, standard, fast

	// Workflow-specific agent/model configuration
	ReviewAgent         string `toml:"review_agent"`
	ReviewAgentFast     string `toml:"review_agent_fast"`
	ReviewAgentStandard string `toml:"review_agent_standard"`
	ReviewAgentThorough string `toml:"review_agent_thorough"`
	RefineAgent         string `toml:"refine_agent"`
	RefineAgentFast     string `toml:"refine_agent_fast"`
	RefineAgentStandard string `toml:"refine_agent_standard"`
	RefineAgentThorough string `toml:"refine_agent_thorough"`
	ReviewModel         string `toml:"review_model"`
	ReviewModelFast     string `toml:"review_model_fast"`
	ReviewModelStandard string `toml:"review_model_standard"`
	ReviewModelThorough string `toml:"review_model_thorough"`
	RefineModel         string `toml:"refine_model"`
	RefineModelFast     string `toml:"refine_model_fast"`
	RefineModelStandard string `toml:"refine_model_standard"`
	RefineModelThorough string `toml:"refine_model_thorough"`

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

// ResolveModel determines which model to use based on config priority:
// 1. Explicit model parameter (if non-empty)
// 2. Per-repo config (model in .roborev.toml)
// 3. Global config (default_model in config.toml)
// 4. Default (empty string, agent uses its default)
func ResolveModel(explicit string, repoPath string, globalCfg *Config) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}

	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && strings.TrimSpace(repoCfg.Model) != "" {
		return strings.TrimSpace(repoCfg.Model)
	}

	if globalCfg != nil && strings.TrimSpace(globalCfg.DefaultModel) != "" {
		return strings.TrimSpace(globalCfg.DefaultModel)
	}

	return ""
}

// DefaultMaxPromptSize is the default maximum prompt size in bytes (200KB)
const DefaultMaxPromptSize = 200 * 1024

// ResolveMaxPromptSize determines the maximum prompt size based on config priority:
// 1. Per-repo config (max_prompt_size in .roborev.toml)
// 2. Global config (default_max_prompt_size in config.toml)
// 3. Default (200KB)
func ResolveMaxPromptSize(repoPath string, globalCfg *Config) int {
	if repoCfg, err := LoadRepoConfig(repoPath); err == nil && repoCfg != nil && repoCfg.MaxPromptSize > 0 {
		return repoCfg.MaxPromptSize
	}

	if globalCfg != nil && globalCfg.DefaultMaxPromptSize > 0 {
		return globalCfg.DefaultMaxPromptSize
	}

	return DefaultMaxPromptSize
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

func repoWorkflowField(r *RepoConfig, workflow, level string, isAgent bool) string {
	if r == nil {
		return ""
	}
	var v string
	if isAgent {
		switch workflow + "_" + level {
		case "review_fast":
			v = r.ReviewAgentFast
		case "review_standard":
			v = r.ReviewAgentStandard
		case "review_thorough":
			v = r.ReviewAgentThorough
		case "review_":
			v = r.ReviewAgent
		case "refine_fast":
			v = r.RefineAgentFast
		case "refine_standard":
			v = r.RefineAgentStandard
		case "refine_thorough":
			v = r.RefineAgentThorough
		case "refine_":
			v = r.RefineAgent
		}
	} else {
		switch workflow + "_" + level {
		case "review_fast":
			v = r.ReviewModelFast
		case "review_standard":
			v = r.ReviewModelStandard
		case "review_thorough":
			v = r.ReviewModelThorough
		case "review_":
			v = r.ReviewModel
		case "refine_fast":
			v = r.RefineModelFast
		case "refine_standard":
			v = r.RefineModelStandard
		case "refine_thorough":
			v = r.RefineModelThorough
		case "refine_":
			v = r.RefineModel
		}
	}
	return strings.TrimSpace(v)
}

func globalWorkflowField(g *Config, workflow, level string, isAgent bool) string {
	if g == nil {
		return ""
	}
	var v string
	if isAgent {
		switch workflow + "_" + level {
		case "review_fast":
			v = g.ReviewAgentFast
		case "review_standard":
			v = g.ReviewAgentStandard
		case "review_thorough":
			v = g.ReviewAgentThorough
		case "review_":
			v = g.ReviewAgent
		case "refine_fast":
			v = g.RefineAgentFast
		case "refine_standard":
			v = g.RefineAgentStandard
		case "refine_thorough":
			v = g.RefineAgentThorough
		case "refine_":
			v = g.RefineAgent
		}
	} else {
		switch workflow + "_" + level {
		case "review_fast":
			v = g.ReviewModelFast
		case "review_standard":
			v = g.ReviewModelStandard
		case "review_thorough":
			v = g.ReviewModelThorough
		case "review_":
			v = g.ReviewModel
		case "refine_fast":
			v = g.RefineModelFast
		case "refine_standard":
			v = g.RefineModelStandard
		case "refine_thorough":
			v = g.RefineModelThorough
		case "refine_":
			v = g.RefineModel
		}
	}
	return strings.TrimSpace(v)
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

	// If there's no scheme, it's likely an SSH URL (git@...) - return as-is
	if parsed.Scheme == "" {
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
