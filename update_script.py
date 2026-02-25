import sys

def modify_test_helpers():
    with open('internal/storage/test_helpers_test.go', 'r') as f:
        content = f.read()

    old_repo = """func createTestRepo(t *testing.T, pool *PgPool, identity string) int64 {
	t.Helper()
	id, err := pool.GetOrCreateRepo(t.Context(), identity)
	if err != nil {
		t.Fatalf("Failed to create repo %s: %v", identity, err)
	}
	return id
}"""

    new_repo = """type TestRepoOpts struct {
	Identity string
}

func (opts *TestRepoOpts) applyDefaults() {
	if opts.Identity == "" {
		opts.Identity = "https://github.com/test/repo-" + uuid.NewString() + ".git"
	}
}

func createTestRepo(t *testing.T, pool *pgxpool.Pool, opts TestRepoOpts) int64 {
	t.Helper()
	opts.applyDefaults()

	var id int64
	err := pool.QueryRow(t.Context(), `
		INSERT INTO repos (identity)
		VALUES ($1)
		ON CONFLICT (identity) DO UPDATE SET identity = EXCLUDED.identity
		RETURNING id
	`, opts.Identity).Scan(&id)
	if err != nil {
		t.Fatalf("Failed to create repo %s: %v", opts.Identity, err)
	}
	return id
}"""
    content = content.replace(old_repo, new_repo)

    old_commit = """func createTestCommit(t *testing.T, pool *PgPool, repoID int64, sha string) int64 {
	t.Helper()
	id, err := pool.GetOrCreateCommit(t.Context(), repoID, sha, "Test Author", "Test Subject", time.Now())
	if err != nil {
		t.Fatalf("Failed to create commit %s: %v", sha, err)
	}
	return id
}"""

    new_commit = """type TestCommitOpts struct {
	RepoID     int64
	SHA        string
	Author     string
	Subject    string
	AuthorDate time.Time
}

func (opts *TestCommitOpts) applyDefaults() {
	if opts.SHA == "" {
		opts.SHA = uuid.NewString()
	}
	if opts.Author == "" {
		opts.Author = "Test Author"
	}
	if opts.Subject == "" {
		opts.Subject = "Test Subject"
	}
	if opts.AuthorDate.IsZero() {
		opts.AuthorDate = time.Now()
	}
}

func createTestCommit(t *testing.T, pool *pgxpool.Pool, opts TestCommitOpts) int64 {
	t.Helper()
	opts.applyDefaults()

	var id int64
	err := pool.QueryRow(t.Context(), `
		INSERT INTO commits (repo_id, sha, author, subject, author_date)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (repo_id, sha) DO UPDATE SET author = EXCLUDED.author
		RETURNING id
	`, opts.RepoID, opts.SHA, opts.Author, opts.Subject, opts.AuthorDate).Scan(&id)
	if err != nil {
		t.Fatalf("Failed to create commit %s: %v", opts.SHA, err)
	}
	return id
}"""
    content = content.replace(old_commit, new_commit)

    old_parse = """func hasExecutableCode(stmt string) bool {
	for line := range strings.SplitSeq(stmt, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return true
		}
	}
	return false
}

func parseSQLStatements(sql string) []string {
	var stmts []string
	for stmt := range strings.SplitSeq(sql, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt != "" && hasExecutableCode(stmt) {
			stmts = append(stmts, stmt)
		}
	}
	return stmts
}"""

    new_parse = """func parseSQLStatements(sql string) []string {
	var stmts []string
	for _, stmt := range strings.Split(sql, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt != "" {
			stmts = append(stmts, stmt)
		}
	}
	return stmts
}"""
    content = content.replace(old_parse, new_parse)

    with open('internal/storage/test_helpers_test.go', 'w') as f:
        f.write(content)

def modify_postgres_test():
    with open('internal/storage/postgres_test.go', 'r') as f:
        content = f.read()

    content = content.replace('repoID := createTestRepo(t, pool, repoIdentity)', 'repoID := createTestRepo(t, pool.Pool(), TestRepoOpts{Identity: repoIdentity})')
    content = content.replace('repoID := createTestRepo(t, pool, "https://github.com/test/batch-jobs-test.git")', 'repoID := createTestRepo(t, pool.Pool(), TestRepoOpts{Identity: "https://github.com/test/batch-jobs-test.git"})')
    content = content.replace('repoID := createTestRepo(t, pool, "https://github.com/test/batch-reviews-test.git")', 'repoID := createTestRepo(t, pool.Pool(), TestRepoOpts{Identity: "https://github.com/test/batch-reviews-test.git"})')
    content = content.replace('repoID := createTestRepo(t, pool, "https://github.com/test/batch-responses-test.git")', 'repoID := createTestRepo(t, pool.Pool(), TestRepoOpts{Identity: "https://github.com/test/batch-responses-test.git"})')

    content = content.replace('commitID := createTestCommit(t, pool, repoID, "abc123456789")', 'commitID := createTestCommit(t, pool.Pool(), TestCommitOpts{RepoID: repoID, SHA: "abc123456789"})')
    content = content.replace('commitID := createTestCommit(t, pool, repoID, fmt.Sprintf("batch-jobs-sha-%d", i))', 'commitID := createTestCommit(t, pool.Pool(), TestCommitOpts{RepoID: repoID, SHA: fmt.Sprintf("batch-jobs-sha-%d", i)})')
    content = content.replace('commitID := createTestCommit(t, pool, repoID, "batch-reviews-sha")', 'commitID := createTestCommit(t, pool.Pool(), TestCommitOpts{RepoID: repoID, SHA: "batch-reviews-sha"})')
    content = content.replace('commitID := createTestCommit(t, pool, repoID, "batch-responses-sha")', 'commitID := createTestCommit(t, pool.Pool(), TestCommitOpts{RepoID: repoID, SHA: "batch-responses-sha"})')

    with open('internal/storage/postgres_test.go', 'w') as f:
        f.write(content)

modify_test_helpers()
modify_postgres_test()
