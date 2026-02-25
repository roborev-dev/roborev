# 1. Update cmd/roborev/refine_test.go
file_path = "cmd/roborev/refine_test.go"
with open(file_path, "r") as f:
    content = f.read()

old_func = '''func setupHookTestRepo(t *testing.T) *testutil.TestRepo {
	t.Helper()
	repo := testutil.NewTestRepo(t)
	repo.RunGit("init")
	repo.SymbolicRef("HEAD", "refs/heads/main")
	repo.Config("user.email", "test@test.com")
	repo.Config("user.name", "Test")
	repo.CommitFile("base.txt", "base", "base commit")
	return repo
}'''

content = content.replace(old_func, '')
content = content.replace('setupHookTestRepo(t)', 'testutil.InitTestRepo(t)')
with open(file_path, "w") as f:
    f.write(content)


# 2. Update cmd/roborev/refine_integration_test.go
file_path = "cmd/roborev/refine_integration_test.go"
with open(file_path, "r") as f:
    content = f.read()

old_func2 = '''	// Helper to create a standard test repo
	// Returns repo, baseSHA
	createStandardRepo := func(t *testing.T) (*testutil.TestRepo, string) {
		repo := testutil.NewTestRepo(t)
		repo.RunGit("init")
		repo.SymbolicRef("HEAD", "refs/heads/main")
		repo.Config("user.email", "test@test.com")
		repo.Config("user.name", "Test")
		baseSHA := repo.CommitFile("base.txt", "base", "base commit")
		return repo, baseSHA
	}'''

new_func2 = '''	// Helper to create a standard test repo
	// Returns repo, baseSHA
	createStandardRepo := func(t *testing.T) (*testutil.TestRepo, string) {
		repo := testutil.InitTestRepo(t)
		baseSHA := repo.RevParse("HEAD")
		return repo, baseSHA
	}'''

content = content.replace(old_func2, new_func2)
with open(file_path, "w") as f:
    f.write(content)


print("Done")
