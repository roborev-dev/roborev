import re

with open('cmd/roborev/refine_test.go', 'r') as f:
    content = f.read()

# Refactor TestValidateRefineContext_RefusesMainBranchWithoutSince
content = content.replace('''	repoDir, _, _ := setupTestGitRepo(t)

	// Stay on main branch (don't create feature branch)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)''', '''	repo := setupHookTestRepo(t)

	// Stay on main branch (don't create feature branch)
	chdirForTest(t, repo.Root)''')

# Refactor TestValidateRefineContext_AllowsMainBranchWithSince
content = content.replace('''	repoDir, baseSHA, runGit := setupTestGitRepo(t)

	// Add another commit on main
	gitCommitFile(t, repoDir, runGit, "second.txt", "second", "second commit")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)''', '''	repo := setupHookTestRepo(t)
	baseSHA := repo.RevParse("HEAD")

	// Add another commit on main
	repo.CommitFile("second.txt", "second", "second commit")

	chdirForTest(t, repo.Root)''')

# Refactor TestValidateRefineContext_SinceWorksOnFeatureBranch
content = content.replace('''	repoDir, baseSHA, runGit := setupTestGitRepo(t)

	// Create feature branch with commits
	runGit("checkout", "-b", "feature")
	gitCommitFile(t, repoDir, runGit, "feature.txt", "feature", "feature commit")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)''', '''	repo := setupHookTestRepo(t)
	baseSHA := repo.RevParse("HEAD")

	// Create feature branch with commits
	repo.RunGit("checkout", "-b", "feature")
	repo.CommitFile("feature.txt", "feature", "feature commit")

	chdirForTest(t, repo.Root)''')

# Refactor TestValidateRefineContext_InvalidSinceRef
content = content.replace('''	repoDir, _, _ := setupTestGitRepo(t)

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)''', '''	repo := setupHookTestRepo(t)

	chdirForTest(t, repo.Root)''')

# Refactor TestValidateRefineContext_SinceNotAncestorOfHEAD
content = content.replace('''	repoDir, _, runGit := setupTestGitRepo(t)

	// Create a commit on a separate branch that diverges from main
	runGit("checkout", "-b", "other-branch")
	otherBranchSHA := gitCommitFile(t, repoDir, runGit, "other.txt", "other", "commit on other branch")

	// Go back to main and create a different commit
	runGit("checkout", "main")
	gitCommitFile(t, repoDir, runGit, "main2.txt", "main2", "second commit on main")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)''', '''	repo := setupHookTestRepo(t)

	// Create a commit on a separate branch that diverges from main
	repo.RunGit("checkout", "-b", "other-branch")
	repo.CommitFile("other.txt", "other", "commit on other branch")
	otherBranchSHA := repo.RevParse("HEAD")

	// Go back to main and create a different commit
	repo.RunGit("checkout", "main")
	repo.CommitFile("main2.txt", "main2", "second commit on main")

	chdirForTest(t, repo.Root)''')

# Refactor TestValidateRefineContext_FeatureBranchWithoutSinceStillWorks
content = content.replace('''	repoDir, baseSHA, runGit := setupTestGitRepo(t)

	// Create feature branch
	runGit("checkout", "-b", "feature")
	gitCommitFile(t, repoDir, runGit, "feature.txt", "feature", "feature commit")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)''', '''	repo := setupHookTestRepo(t)
	baseSHA := repo.RevParse("HEAD")

	// Create feature branch
	repo.RunGit("checkout", "-b", "feature")
	repo.CommitFile("feature.txt", "feature", "feature commit")

	chdirForTest(t, repo.Root)''')

# Refactor TestCommitWithHookRetry* to use setupHookTestRepo
content = re.sub(r'''	repo := testutil\.NewTestRepo\(t\)\n	repo\.RunGit\("init"\)\n	repo\.SymbolicRef\("HEAD", "refs/heads/main"\)\n	repo\.Config\("user\.email", "test@test\.com"\)\n	repo\.Config\("user\.name", "Test"\)\n	repo\.CommitFile\("base\.txt", "base", "base commit"\)''', '''	repo := setupHookTestRepo(t)''', content)

with open('cmd/roborev/refine_test.go', 'w') as f:
    f.write(content)
