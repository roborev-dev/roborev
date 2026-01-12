package version

import (
	"runtime/debug"
	"strings"
)

// Version is the build version. Set via -ldflags for releases,
// otherwise falls back to git commit hash from VCS info.
// For dev builds with git describe version, use: make install
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

	var revision string
	var modified bool

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if revision == "" {
		return "dev"
	}

	// Use short hash
	if len(revision) > 7 {
		revision = revision[:7]
	}

	// Mark dirty builds
	if modified {
		revision += "-dirty"
	}

	return revision
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
