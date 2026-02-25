import re
import sys

with open("internal/testutil/testutil.go", "r") as f:
    content = f.read()

# Add constants before the TestRepo struct comment
const_block = '''const (
	GitUserName  = "Test"
	GitUserEmail = "test@test.com"
)

// TestRepo encapsulates a temporary git repository for tests.'''

pattern = re.compile(
    r'//\s*TestRepo encapsulates a temporary git repository for tests\.'
)
if not pattern.search(content):
    print("ERROR: could not find TestRepo comment", file=sys.stderr)
    sys.exit(1)

content = pattern.sub(const_block, content)

# Replace "Test" with GitUserName and "test@test.com" with GitUserEmail
content = content.replace('"Test"', 'GitUserName')
content = content.replace('"test@test.com"', 'GitUserEmail')

# Env-var patterns: "KEY=value" -> "KEY=" + Const
# Handles optional whitespace around commas and varying trailing punctuation
ENV_REPLACEMENTS = [
    ("GIT_AUTHOR_NAME", "GitUserName"),
    ("GIT_AUTHOR_EMAIL", "GitUserEmail"),
    ("GIT_COMMITTER_NAME", "GitUserName"),
    ("GIT_COMMITTER_EMAIL", "GitUserEmail"),
]

for env_key, const_name in ENV_REPLACEMENTS:
    # Match "KEY=<old-value>" with optional trailing comma/whitespace
    pat = re.compile(
        r'"' + re.escape(env_key) + r'='
        + r'[^"]*"'    # value portion inside the quotes
        + r'(\s*,?)'   # optional trailing comma
    )
    replacement = '"' + env_key + '="+' + const_name + r'\1'

    new_content = pat.sub(replacement, content)
    if new_content == content:
        print(
            f"WARNING: no match for {env_key} replacement",
            file=sys.stderr,
        )
    content = new_content

# Config commands: these should keep their current form (already using
# constants after the broad replace above). Verify they look correct.
for fn_call in [
    'runGit("config", "user.email", GitUserEmail)',
    'runGit("config", "user.name", GitUserName)',
]:
    if fn_call not in content:
        print(
            f"WARNING: expected call not found: {fn_call}",
            file=sys.stderr,
        )

with open("internal/testutil/testutil.go", "w") as f:
    f.write(content)
