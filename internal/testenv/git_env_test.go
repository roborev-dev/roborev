package testenv

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIsolatedGitEnv(t *testing.T) {
	dir := "/tmp/mockdir"
	baseEnv := []string{
		"PATH=/usr/bin",
		"HOME=/user/home",
		"home=/user/home2",
		"GIT_CONFIG_NOSYSTEM=0",
		"git_config_nosystem=0",
		"XDG_CONFIG_HOME=/user/home/.config",
		"GIT_AUTHOR_NAME=Old Name",
		"GIT_CONFIG_GLOBAL=/inherited/global/config",
		"git_config_global=/inherited/global/config2",
		"UNRELATED=value",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.pager",
		"GIT_CONFIG_VALUE_0=cat",
	}

	got := BuildIsolatedGitEnv(baseEnv, dir)

	// We expect UNRELATED and PATH to be kept, and the overrides appended.
	// We expect HOME, GIT_CONFIG_NOSYSTEM, XDG_CONFIG_HOME to be overridden.
	// We expect GIT_CONFIG_COUNT, GIT_CONFIG_KEY_0, and GIT_AUTHOR_NAME to be dropped.

	expectedVars := map[string]string{
		"PATH":                "/usr/bin",
		"UNRELATED":           "value",
		"HOME":                dir,
		"XDG_CONFIG_HOME":     filepath.Join(dir, ".config"),
		"GIT_CONFIG_GLOBAL":   filepath.Join(dir, ".gitconfig"),
		"GIT_CONFIG_SYSTEM":   "/dev/null",
		"GIT_CONFIG_NOSYSTEM": "1",
	}

	gotMap := make(map[string]string)
	counts := make(map[string]int)
	for _, env := range got {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			key := strings.ToUpper(parts[0])
			gotMap[key] = parts[1]
			counts[key]++
		}
	}

	for k, v := range expectedVars {
		if gotV, ok := gotMap[k]; !ok || gotV != v {
			t.Errorf("Expected %s=%s, got %s", k, v, gotV)
		}
		if counts[k] != 1 {
			t.Errorf("Expected exactly 1 instance of %s, got %d", k, counts[k])
		}
	}

	// Check that we didn't get any extra variables (e.g. GIT_AUTHOR_NAME)
	for k := range gotMap {
		if _, ok := expectedVars[k]; !ok {
			t.Errorf("Unexpected variable in isolated env: %s=%s", k, gotMap[k])
		}
	}
}
