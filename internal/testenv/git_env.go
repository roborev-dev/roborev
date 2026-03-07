package testenv

import (
	"path/filepath"
	"strings"
)

// BuildIsolatedGitEnv creates an environment slice for git commands
// that isolates them from user global configuration and avoids duplicate keys.
func BuildIsolatedGitEnv(baseEnv []string, dir string) []string {
	overrides := []struct {
		key   string
		value string
	}{
		{"HOME", dir},
		{"XDG_CONFIG_HOME", filepath.Join(dir, ".config")},
		{"GIT_CONFIG_GLOBAL", filepath.Join(dir, ".gitconfig")},
		{"GIT_CONFIG_SYSTEM", "/dev/null"},
		{"GIT_CONFIG_NOSYSTEM", "1"},
	}

	skipKeys := []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	}

	skip := make(map[string]struct{}, len(overrides)+len(skipKeys))
	for _, override := range overrides {
		skip[strings.ToUpper(override.key)] = struct{}{}
	}
	for _, key := range skipKeys {
		skip[strings.ToUpper(key)] = struct{}{}
	}

	filtered := make([]string, 0, len(baseEnv)+len(overrides))
	for _, entry := range baseEnv {
		key, _, _ := strings.Cut(entry, "=")
		upperKey := strings.ToUpper(key)
		if _, ok := skip[upperKey]; ok {
			continue
		}
		// Also skip other git config env vars that might leak
		if upperKey == "GIT_CONFIG_PARAMETERS" || upperKey == "GIT_CONFIG_COUNT" || strings.HasPrefix(upperKey, "GIT_CONFIG_KEY_") || strings.HasPrefix(upperKey, "GIT_CONFIG_VALUE_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	for _, override := range overrides {
		filtered = append(filtered, override.key+"="+override.value)
	}
	return filtered
}
