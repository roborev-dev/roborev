package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var diffSnapshotRE = regexp.MustCompile("`([^`]*roborev-snapshot-[^`]*\\.diff)`")

// diffSnapshotDirs scans a review prompt for backtick-quoted paths to
// roborev diff snapshots and returns the unique parent directories.
// Only paths that point to an existing file inside a "roborev-snapshot-"
// directory are returned, so sandboxed agents can expose just those
// private directories instead of the whole system temp dir.
func diffSnapshotDirs(reviewPrompt string) []string {
	matches := diffSnapshotRE.FindAllStringSubmatch(reviewPrompt, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var dirs []string
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		path := filepath.Clean(match[1])
		if !filepath.IsAbs(path) {
			continue
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "roborev-snapshot-") || filepath.Ext(base) != ".diff" {
			continue
		}
		dir := filepath.Dir(path)
		if !strings.HasPrefix(filepath.Base(dir), "roborev-snapshot-") {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return dirs
}
