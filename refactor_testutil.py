import sys

file_path = "internal/testutil/testutil.go"
with open(file_path, "r") as f:
    content = f.read()

# Add constants
content = content.replace(
    "// TestRepo encapsulates a temporary git repository for tests.",
    "const (\n\tGitUserName  = \"Test\"\n\tGitUserEmail = \"test@test.com\"\n)\n\n// TestRepo encapsulates a temporary git repository for tests."
)

# Replace env vars
content = content.replace('"GIT_AUTHOR_NAME=Test",', '"GIT_AUTHOR_NAME="+GitUserName,')
content = content.replace('"GIT_AUTHOR_EMAIL=test@test.com",', '"GIT_AUTHOR_EMAIL="+GitUserEmail,')
content = content.replace('"GIT_COMMITTER_NAME=Test",', '"GIT_COMMITTER_NAME="+GitUserName,')
content = content.replace('"GIT_COMMITTER_EMAIL=test@test.com",', '"GIT_COMMITTER_EMAIL="+GitUserEmail,')

# Replace git config commands
content = content.replace('runGit("config", "user.email", "test@test.com")', 'runGit("config", "user.email", GitUserEmail)')
content = content.replace('runGit("config", "user.name", "Test")', 'runGit("config", "user.name", GitUserName)')

content = content.replace('{"git", "-C", dir, "config", "user.email", "test@test.com"}', '{"git", "-C", dir, "config", "user.email", GitUserEmail}')
content = content.replace('{"git", "-C", dir, "config", "user.name", "Test"}', '{"git", "-C", dir, "config", "user.name", GitUserName}')

# CreateCompletedReview uses "Test" as author name
content = content.replace('GetOrCreateCommit(repoID, sha, "Test", "test commit", time.Now())', 'GetOrCreateCommit(repoID, sha, GitUserName, "test commit", time.Now())')

# Add InitTestRepo helper function
init_repo_code = """
// InitTestRepo creates a standard test repository with an initial commit on the main branch.
func InitTestRepo(t *testing.T) *TestRepo {
	t.Helper()
	repo := NewTestRepo(t)
	repo.RunGit("init")
	repo.SymbolicRef("HEAD", "refs/heads/main")
	repo.Config("user.email", GitUserEmail)
	repo.Config("user.name", GitUserName)
	repo.CommitFile("base.txt", "base", "base commit")
	return repo
}
"""

content = content.replace('// RunGit runs a git command in the repo directory.', init_repo_code + '\n// RunGit runs a git command in the repo directory.')

with open(file_path, "w") as f:
    f.write(content)

print("Updated testutil.go successfully.")
