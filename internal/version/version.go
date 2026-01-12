package version

import (
	"os/exec"
	"runtime/debug"
	"strings"
)

// Version is the build version. Set via -ldflags for releases,
// otherwise falls back to git describe or commit hash.
var Version = "dev"

func init() {
	// If Version was set via ldflags, use it
	if Version != "dev" {
		return
	}

	// Fall back to VCS info for development builds
	Version = getVersionFromVCS()
}

func getVersionFromVCS() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}

	var modified bool
	for _, setting := range info.Settings {
		if setting.Key == "vcs.modified" {
			modified = setting.Value == "true"
			break
		}
	}

	// Try git describe first - shows tag + commits ahead + short hash
	// e.g., "v0.7.0-6-g0a5b394"
	if desc := getGitDescribe(); desc != "" {
		if modified {
			desc += "-dirty"
		}
		return desc
	}

	// Fall back to commit hash from build info
	var revision string
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
			break
		}
	}

	if revision == "" {
		return "dev"
	}

	// Use short hash
	if len(revision) > 7 {
		revision = revision[:7]
	}

	if modified {
		revision += "-dirty"
	}

	return revision
}

// getGitDescribe runs git describe to get version relative to tags
func getGitDescribe() string {
	cmd := exec.Command("git", "describe", "--tags", "--always")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Full returns the full version string with additional build info
func Full() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}

	var parts []string
	parts = append(parts, Version)

	for _, setting := range info.Settings {
		if setting.Key == "vcs.time" {
			parts = append(parts, setting.Value)
			break
		}
	}

	return strings.Join(parts, " ")
}
