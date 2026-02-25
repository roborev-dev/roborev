import re

with open("internal/config/keyval_test.go", "r") as f:
    content = f.read()

# 1. Extract newComplexTestConfig and update TestGetConfigValue / TestListConfigKeys
complex_cfg = """
func newComplexTestConfig() *Config {
	return &Config{
		DefaultAgent:       "codex",
		MaxWorkers:         4,
		ReviewContextCount: 3,
		Sync: SyncConfig{
			Enabled:     true,
			PostgresURL: "postgres://localhost/test",
		},
		CI: CIConfig{
			PollInterval: "10m",
			GitHubAppConfig: GitHubAppConfig{
				GitHubAppID:         12345,
				GitHubAppPrivateKey: "test-private-key",
			},
		},
	}
}
"""

content = content.replace("func TestGetConfigValue(t *testing.T) {\n\tcfg := &Config{\n\t\tDefaultAgent:       \"codex\",\n\t\tMaxWorkers:         4,\n\t\tReviewContextCount: 3,\n\t\tSync: SyncConfig{\n\t\t\tEnabled:     true,\n\t\t\tPostgresURL: \"postgres://localhost/test\",\n\t\t},\n\t\tCI: CIConfig{\n\t\t\tPollInterval: \"10m\",\n\t\t\tGitHubAppConfig: GitHubAppConfig{\n\t\t\t\tGitHubAppID:         12345,\n\t\t\t\tGitHubAppPrivateKey: \"test-private-key\",\n\t\t\t},\n\t\t},\n\t}", complex_cfg + "\nfunc TestGetConfigValue(t *testing.T) {\n\tcfg := newComplexTestConfig()")

content = content.replace("func TestListConfigKeys(t *testing.T) {\n\tcfg := &Config{\n\t\tDefaultAgent: \"codex\",\n\t\tMaxWorkers:   4,\n\t\tSync: SyncConfig{\n\t\t\tEnabled: true,\n\t\t},\n\t\tCI: CIConfig{\n\t\t\tGitHubAppConfig: GitHubAppConfig{\n\t\t\t\tGitHubAppID:         12345,\n\t\t\t\tGitHubAppPrivateKey: \"private-key-data\",\n\t\t\t},\n\t\t},\n\t}", "func TestListConfigKeys(t *testing.T) {\n\tcfg := newComplexTestConfig()")

content = content.replace("assertConfigValues(t, ListConfigKeys(cfg), map[string]string{\n\t\t\"default_agent\":             \"codex\",\n\t\t\"max_workers\":               \"4\",\n\t\t\"sync.enabled\":              \"true\",\n\t\t\"ci.github_app_id\":          \"12345\",\n\t\t\"ci.github_app_private_key\": \"private-key-data\",\n\t})", "assertConfigValues(t, ListConfigKeys(cfg), map[string]string{\n\t\t\"default_agent\":             \"codex\",\n\t\t\"max_workers\":               \"4\",\n\t\t\"review_context_count\":      \"3\",\n\t\t\"sync.enabled\":              \"true\",\n\t\t\"sync.postgres_url\":         \"postgres://localhost/test\",\n\t\t\"ci.poll_interval\":          \"10m\",\n\t\t\"ci.github_app_id\":          \"12345\",\n\t\t\"ci.github_app_private_key\": \"test-private-key\",\n\t})")


# 2. Update TestSetConfigValue Error Reporting
verify_old = "verify func(*Config) bool"
verify_new = "verify func(*testing.T, *Config)"
content = content.replace(verify_old, verify_new)

content = re.sub(r'verify:\s*func\(c \*Config\) bool\s*{\s*return\s+(.+?)\s*},', lambda m: f'verify: func(t *testing.T, c *Config) {{ if !({m.group(1)}) {{ t.Errorf("verification failed") }} }},', content)

# I will write python script out to perform more complex modifications, but using python re.sub is too error prone. I'll just write the full file instead.
