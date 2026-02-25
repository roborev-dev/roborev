import re

with open("internal/testutil/testutil.go", "r") as f:
    content = f.read()

# Add constants
content = re.sub(r'// TestRepo encapsulates a temporary git repository for tests\.',
'''const (
	GitUserName  = "Test"
	GitUserEmail = "test@test.com"
)

// TestRepo encapsulates a temporary git repository for tests.''', content)

# Replace "Test" with GitUserName and "test@test.com" with GitUserEmail
content = content.replace('"Test"', 'GitUserName').replace('"test@test.com"', 'GitUserEmail')

# But we need to be careful not to break strings like "git config user.name Test"
# Actually, let's use exact replacements for the env vars and config commands
content = content.replace('GIT_AUTHOR_NAME=Test', 'GIT_AUTHOR_NAME="+GitUserName')
content = content.replace('GIT_AUTHOR_EMAIL=test@test.com', 'GIT_AUTHOR_EMAIL="+GitUserEmail')
content = content.replace('GIT_COMMITTER_NAME=Test', 'GIT_COMMITTER_NAME="+GitUserName')
content = content.replace('GIT_COMMITTER_EMAIL=test@test.com', 'GIT_COMMITTER_EMAIL="+GitUserEmail')

content = content.replace('runGit("config", "user.email", GitUserEmail)', 'runGit("config", "user.email", GitUserEmail)')
content = content.replace('runGit("config", "user.name", GitUserName)', 'runGit("config", "user.name", GitUserName)')

content = content.replace('{"git", "-C", dir, "config", "user.email", GitUserEmail}', '{"git", "-C", dir, "config", "user.email", GitUserEmail}')
content = content.replace('{"git", "-C", dir, "config", "user.name", GitUserName}', '{"git", "-C", dir, "config", "user.name", GitUserName}')


with open("internal/testutil/testutil.go", "w") as f:
    f.write(content)
