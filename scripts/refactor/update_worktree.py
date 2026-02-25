import os

file_path = "internal/worktree/worktree_integration_test.go"
if os.path.exists(file_path):
    with open(file_path, "r") as f:
        content = f.read()

    # Replace the exact block 1
    old_block1 = """	repo := testutil.NewTestRepo(t)
	repo.RunGit("init")
	repo.Config("user.email", "test@test.com")
	repo.Config("user.name", "Test")
	repo.CommitFile("base.txt", "base", "base commit")"""
    new_block1 = """	repo := testutil.InitTestRepo(t)"""
    content = content.replace(old_block1, new_block1)

    # For subRepo and mainRepo
    content = content.replace('"test@test.com"', 'testutil.GitUserEmail')
    content = content.replace('"Test"', 'testutil.GitUserName')

    with open(file_path, "w") as f:
        f.write(content)

# Fix git_test.go if needed
file_path = "internal/git/git_test.go"
if os.path.exists(file_path):
    with open(file_path, "r") as f:
        content = f.read()
    content = content.replace('"test@test.com"', 'testutil.GitUserEmail')
    content = content.replace('"Test"', 'testutil.GitUserName')
    with open(file_path, "w") as f:
        f.write(content)
