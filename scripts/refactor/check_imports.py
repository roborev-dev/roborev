import os

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

for file_path in files_to_update:
    if not os.path.exists(file_path):
        continue

    with open(file_path, "r") as f:
        content = f.read()

    has_testutil_import = "testutil" in content
    has_email = '"test@test.com"' in content
    has_name = '"Test"' in content

    if has_email or has_name:
        print(
            f"{file_path}: "
            f"email={has_email}, name={has_name}, "
            f"testutil_imported={has_testutil_import}"
        )
