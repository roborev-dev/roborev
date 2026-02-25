import os
import re

files_to_update = [
    "internal/worktree/worktree_integration_test.go",
    "internal/worktree/worktree_test.go",
    "internal/git/git_test.go",
    "internal/config/config_test.go",
    "internal/daemon/client_test.go",
    "internal/daemon/server_test.go",
    "internal/daemon/ci_poller_test.go",
    "cmd/roborev/helpers_test.go",
    "cmd/roborev/main_test_helpers_test.go",
    "cmd/roborev/fix_test.go"
]

# Match Go import blocks: import "pkg" or import (\n...\n)
_SINGLE_IMPORT_RE = re.compile(
    r'^import\s+"[^"]*testutil[^"]*"', re.MULTILINE
)
_BLOCK_IMPORT_RE = re.compile(
    r'^import\s*\(.*?\)', re.MULTILINE | re.DOTALL
)


def has_testutil_in_imports(content: str) -> bool:
    """Return True only if 'testutil' appears inside a Go import."""
    if _SINGLE_IMPORT_RE.search(content):
        return True
    for block in _BLOCK_IMPORT_RE.finditer(content):
        if "testutil" in block.group(0):
            return True
    return False


for file_path in files_to_update:
    if not os.path.exists(file_path):
        continue

    with open(file_path, "r") as f:
        content = f.read()

    has_testutil_import = has_testutil_in_imports(content)
    has_email = '"test@test.com"' in content
    has_name = '"Test"' in content

    if has_email or has_name:
        print(
            f"{file_path}: "
            f"email={has_email}, name={has_name}, "
            f"testutil_imported={has_testutil_import}"
        )
