import re
import sys

def main():
    with open("internal/storage/sync_test.go", "r") as f:
        content = f.read()

    # 1. Add methods to syncTestHelper
    helper_methods = """
func (h *syncTestHelper) createPendingJob(sha string) *ReviewJob {
\tcommit, err := h.db.GetOrCreateCommit(h.repo.ID, sha, "Author", "Subject", time.Now())
\tif err != nil {
\t\th.t.Fatalf("Failed to create commit: %v", err)
\t}
\tjob, err := h.db.EnqueueJob(EnqueueOpts{RepoID: h.repo.ID, CommitID: commit.ID, GitRef: sha, Agent: "test", Reasoning: "thorough"})
\tif err != nil {
\t\th.t.Fatalf("Failed to enqueue job: %v", err)
\t}
\treturn job
}

func (h *syncTestHelper) clearSourceMachineID(jobID int64) {
\t_, err := h.db.Exec(`UPDATE review_jobs SET source_machine_id = NULL WHERE id = ?`, jobID)
\tif err != nil {
\t\th.t.Fatalf("Failed to clear source_machine_id: %v", err)
\t}
}

func (h *syncTestHelper) clearRepoIdentity(repoID int64) {
\t_, err := h.db.Exec(`UPDATE repos SET identity = NULL WHERE id = ?`, repoID)
\tif err != nil {
\t\th.t.Fatalf("Failed to clear identity: %v", err)
\t}
}
"""
    # Insert methods after syncTestHelper definition
    match = re.search(r'func newSyncTestHelper\(.*?return &syncTestHelper.*?}', content, re.DOTALL)
    if match:
        content = content[:match.end()] + "\n" + helper_methods + content[match.end():]
    else:
        print("Could not find newSyncTestHelper")
        sys.exit(1)

    # 2. Extract Legacy Schema Fixtures
    legacy_schemas = """
const legacySchemaV1DDL = `
\t\tCREATE TABLE repos (
\t\t\tid INTEGER PRIMARY KEY,
\t\t\troot_path TEXT UNIQUE NOT NULL,
\t\t\tname TEXT NOT NULL,
\t\t\tcreated_at TEXT NOT NULL DEFAULT (datetime('now'))
\t\t);
\t\tCREATE TABLE commits (
\t\t\tid INTEGER PRIMARY KEY,
\t\t\trepo_id INTEGER NOT NULL REFERENCES repos(id),
\t\t\tsha TEXT UNIQUE NOT NULL,
\t\t\tauthor TEXT NOT NULL,
\t\t\tsubject TEXT NOT NULL,
\t\t\ttimestamp TEXT NOT NULL,
\t\t\tcreated_at TEXT NOT NULL DEFAULT (datetime('now'))
\t\t);
\t\tCREATE INDEX idx_commits_sha ON commits(sha);
\t`

const legacySchemaV2DDL = `
\t\tCREATE TABLE repos (
\t\t\tid INTEGER PRIMARY KEY,
\t\t\troot_path TEXT UNIQUE NOT NULL,
\t\t\tname TEXT NOT NULL,
\t\t\tcreated_at TEXT NOT NULL DEFAULT (datetime('now')),
\t\t\tidentity TEXT
\t\t);
\t\tCREATE TABLE commits (
\t\t\tid INTEGER PRIMARY KEY,
\t\t\trepo_id INTEGER NOT NULL REFERENCES repos(id),
\t\t\tsha TEXT NOT NULL,
\t\t\tauthor TEXT NOT NULL,
\t\t\tsubject TEXT NOT NULL,
\t\t\ttimestamp TEXT NOT NULL,
\t\t\tcreated_at TEXT NOT NULL DEFAULT (datetime('now')),
\t\t\tUNIQUE(repo_id, sha)
\t\t);

\t\tCREATE INDEX idx_commits_sha ON commits(sha);
\t`

const legacySchemaV3DDL = `
\t\tCREATE TABLE repos (
\t\t\tid INTEGER PRIMARY KEY,
\t\t\troot_path TEXT UNIQUE NOT NULL,
\t\t\tname TEXT NOT NULL,
\t\t\tcreated_at TEXT NOT NULL DEFAULT (datetime('now')),
\t\t\tidentity TEXT
\t\t);
\t\tCREATE UNIQUE INDEX idx_repos_identity ON repos(identity) WHERE identity IS NOT NULL;
\t\tCREATE TABLE commits (
\t\t\tid INTEGER PRIMARY KEY,
\t\t\trepo_id INTEGER NOT NULL REFERENCES repos(id),
\t\t\tsha TEXT NOT NULL,
\t\t\tauthor TEXT NOT NULL,
\t\t\tsubject TEXT NOT NULL,
\t\t\ttimestamp TEXT NOT NULL,
\t\t\tcreated_at TEXT NOT NULL DEFAULT (datetime('now')),
\t\t\tUNIQUE(repo_id, sha)
\t\t);

\t\tCREATE INDEX idx_commits_sha ON commits(sha);
\t`

func setupLegacySchema(t *testing.T, db *sql.DB, ddl string) {
\t_, err := db.Exec(ddl)
\tif err != nil {
\t\tdb.Close()
\t\tt.Fatalf("Failed to create old schema: %v", err)
\t}
\tcreateLegacyCommonTables(t, db)
}
"""
    # Insert legacy schema stuff before TestCommitsMigration_SameSHADifferentRepos
    content = content.replace('func TestCommitsMigration_SameSHADifferentRepos', legacy_schemas + '\nfunc TestCommitsMigration_SameSHADifferentRepos')

    # Now replace the inline schemas
    def replace_schema(test_name, ddl_var):
        nonlocal content
        # Find the test function block
        start_idx = content.find(f"func {test_name}(")
        if start_idx == -1:
            print(f"Could not find {test_name}")
            return
        
        # Find the rawDB.Exec block for the schema
        # We look for `_, err = rawDB.Exec(` ... `)`
        exec_start = content.find("_, err = rawDB.Exec(`", start_idx)
        if exec_start == -1:
            print(f"Could not find rawDB.Exec in {test_name}")
            return
            
        exec_end = content.find("`)", exec_start)
        if exec_end == -1:
            return
            
        # The block continues to checking err and then createLegacyCommonTables
        block_end = content.find("createLegacyCommonTables(t, rawDB)", exec_end)
        if block_end == -1:
            return
        
        block_end += len("createLegacyCommonTables(t, rawDB)")
        
        old_block = content[exec_start:block_end]
        new_block = f"setupLegacySchema(t, rawDB, {ddl_var})"
        content = content[:exec_start] + new_block + content[block_end:]
        print(f"Replaced schema in {test_name}")

    replace_schema("TestCommitsMigration_SameSHADifferentRepos", "legacySchemaV1DDL")
    replace_schema("TestDuplicateRepoIdentity_MigrationSuccess", "legacySchemaV2DDL")
    replace_schema("TestUniqueIndexMigration", "legacySchemaV3DDL")

    # 3. Refactor TestBackfillSourceMachineID
    test1_start = content.find("func TestBackfillSourceMachineID(t *testing.T) {")
    test1_end = content.find("func TestBackfillRepoIdentities_LocalRepoFallback", test1_start)
    if test1_start != -1 and test1_end != -1:
        t1_body = content[test1_start:test1_end]
        
        # Replace manual setup with helper
        t1_body = re.sub(
            r'db := openTestDB\(t\)\s*defer db\.Close\(\)\s*// Create test data.*?if err != nil \{\s*t\.Fatalf\("EnqueueJob failed: %v", err\)\s*\}',
            'h := newSyncTestHelper(t)\n\n\tjob := h.createPendingJob("abc123")',
            t1_body, flags=re.DOTALL
        )
        
        # Replace variable query calls
        t1_body = t1_body.replace("db.QueryRow(", "h.db.QueryRow(")
        t1_body = t1_body.replace("db.Exec(", "h.db.Exec(")
        t1_body = t1_body.replace("db.BackfillSourceMachineID()", "h.db.BackfillSourceMachineID()")
        t1_body = t1_body.replace("db.GetMachineID()", "h.db.GetMachineID()")
        
        # Replace clear statement
        t1_body = re.sub(
            r'_, err = h\.db\.Exec\(`UPDATE review_jobs SET source_machine_id = NULL WHERE id = \?`, job\.ID\)\s*if err != nil \{\s*t\.Fatalf\("Failed to clear source_machine_id: %v", err\)\s*\}',
            'h.clearSourceMachineID(job.ID)',
            t1_body, flags=re.DOTALL
        )
        
        content = content[:test1_start] + t1_body + content[test1_end:]
        print("Refactored TestBackfillSourceMachineID")

    # 4. Refactor TestGetKnownJobUUIDs
    test2_start = content.find("func TestGetKnownJobUUIDs(t *testing.T) {")
    if test2_start != -1:
        test2_end = content.find("\n}\n", test2_start) + 3
        t2_body = content[test2_start:test2_end]
        
        t2_body = re.sub(
            r'db := openTestDB\(t\)\s*defer db\.Close\(\)\s*repo, err := db\.GetOrCreateRepo.*?if err != nil \{\s*t\.Fatalf\("EnqueueJob failed: %v", err\)\s*\}',
            'h := newSyncTestHelper(t)\n\n\tjob := h.createPendingJob("abc123")',
            t2_body, flags=re.DOTALL
        )
        t2_body = t2_body.replace("db.GetKnownJobUUIDs", "h.db.GetKnownJobUUIDs")
        
        content = content[:test2_start] + t2_body + content[test2_end:]
        print("Refactored TestGetKnownJobUUIDs")

    # 5. Refactor SQL clear statements in other tests using temporary helper
    clear_repo_identity_old = """_, err = db.Exec(`UPDATE repos SET identity = NULL WHERE id = ?`, repo.ID)
\tif err != nil {
\t\tt.Fatalf("Failed to clear identity: %v", err)
\t}"""
    clear_repo_identity_new = """h := &syncTestHelper{t: t, db: db}
\th.clearRepoIdentity(repo.ID)"""
    
    content = content.replace(clear_repo_identity_old, clear_repo_identity_new)
    print("Refactored clear identity calls")

    with open("internal/storage/sync_test.go", "w") as f:
        f.write(content)

if __name__ == "__main__":
    main()