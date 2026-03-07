//go:build integration

package git

import (
	"testing"
)

// AddSubmodule adds a submodule to the test repository.
func (r *TestRepo) AddSubmodule(sourceDir, path string) {
	r.T.Helper()
	r.Run("-c", "protocol.file.allow=always", "submodule", "add", sourceDir, path)
}

// CloneTestRepo clones an existing repository into a new TestRepo.
func CloneTestRepo(t *testing.T, sourceDir string) *TestRepo {
	t.Helper()
	cloneDir := t.TempDir()
	runGit(t, "", "clone", sourceDir, cloneDir)
	return &TestRepo{T: t, Dir: cloneDir}
}
