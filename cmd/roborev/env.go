package main

import (
	"os"
	"path/filepath"
	"strings"
)

// gitRepoEnvKeys lists git environment variables that bind commands to a
// specific repository or worktree. These must be stripped when spawning the
// daemon so it resolves refs from the repo_path in each request, not from
// whichever hook context started it.
//
// Auth/transport vars (GIT_SSH_COMMAND, GIT_ASKPASS, GIT_TERMINAL_PROMPT, etc.)
// are intentionally preserved since the daemon may need them for CI poller
// fetches or other git transport operations.
var gitRepoEnvKeys = map[string]struct{}{
	"GIT_DIR":                          {},
	"GIT_WORK_TREE":                    {},
	"GIT_INDEX_FILE":                   {},
	"GIT_OBJECT_DIRECTORY":             {},
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
	"GIT_COMMON_DIR":                   {},
	"GIT_CEILING_DIRECTORIES":          {},
	"GIT_NAMESPACE":                    {},
	"GIT_PREFIX":                       {},
	"GIT_QUARANTINE_PATH":              {},
	"GIT_DISCOVERY_ACROSS_FILESYSTEM":  {},
	"GIT_CONFIG_PARAMETERS":            {}, // carries git -c options from parent
	"GIT_CONFIG_COUNT":                 {}, // git 2.31+ config propagation
	"GIT_CONFIG_GLOBAL":                {}, // redirects to alternate global config
	"GIT_CONFIG_SYSTEM":                {}, // redirects to alternate system config
	"GIT_EXTERNAL_DIFF":                {}, // would replace diff output with external tool
	"GIT_DIFF_OPTS":                    {}, // alters diff output format
}

// gitRepoEnvPrefixes lists key prefixes for numbered git config propagation
// variables (GIT_CONFIG_KEY_0, GIT_CONFIG_VALUE_0, etc.) that should also
// be stripped.
var gitRepoEnvPrefixes = []string{
	"GIT_CONFIG_KEY_",
	"GIT_CONFIG_VALUE_",
}

// isGitRepoEnvKey reports whether a KEY=value entry is a git repo-context
// variable that should be stripped from daemon environments.
// Uses case-insensitive comparison because Windows env vars are case-insensitive.
func isGitRepoEnvKey(entry string) bool {
	key, _, _ := strings.Cut(entry, "=")
	upper := strings.ToUpper(key)
	if _, ok := gitRepoEnvKeys[upper]; ok {
		return true
	}
	for _, prefix := range gitRepoEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// filterGitEnv returns a copy of env with git repo-context variables removed.
// Git sets variables like GIT_DIR in hook contexts; if the daemon inherits them,
// git commands resolve HEAD from the wrong worktree/repo.
func filterGitEnv(env []string) []string {
	result := make([]string, 0, len(env))
	for _, e := range env {
		if isGitRepoEnvKey(e) {
			continue
		}
		result = append(result, e)
	}
	return result
}

func isGoTestBinaryPath(exePath string) bool {
	base := strings.ToLower(filepath.Base(exePath))
	return strings.HasSuffix(base, ".test") ||
		strings.HasSuffix(base, ".test.exe")
}

// isGoBuildCacheBinary returns true if the binary lives in a Go
// build cache directory (produced by "go run"). These ephemeral
// binaries should not auto-start daemons because they have version
// "dev" and would kill the production daemon via version-mismatch
// restart. Uses path-segment matching to avoid false positives on
// paths like /home/go-builder/bin/roborev.
func isGoBuildCacheBinary(exePath string) bool {
	for seg := range strings.SplitSeq(exePath, string(filepath.Separator)) {
		if seg == "go-build" {
			return true
		}
		// go run temp dirs: go-build<digits>
		if after, ok := strings.CutPrefix(seg, "go-build"); ok &&
			after != "" && isAllDigits(after) {
			return true
		}
	}
	return false
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func shouldRefuseAutoStartDaemon(exePath string) bool {
	if isGoBuildCacheBinary(exePath) {
		return true
	}
	if !isGoTestBinaryPath(exePath) {
		return false
	}
	// Allow explicit opt-in for tests that intentionally want to auto-start.
	return os.Getenv("ROBOREV_TEST_ALLOW_AUTOSTART") != "1"
}
