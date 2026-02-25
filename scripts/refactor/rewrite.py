import re

with open('internal/storage/postgres_test.go', 'r') as f:
    content = f.read()

helper = """func setupMigrationEnv(t *testing.T) *MigrationTestEnv {
	env := NewMigrationTestEnv(t)
	// Clean up after test
	env.CleanupDropSchema("roborev")
	env.CleanupDropTable("public", "schema_version")
	env.CleanupDropTable("public", "repos")

	env.DropTable("public", "repos")
	env.DropTable("public", "schema_version")
	env.DropSchema("roborev")
	return env
}

"""

# Add helper before first test that uses it
idx = content.find('func TestIntegration_EnsureSchema_MigratesLegacyTables')
if idx != -1:
    content = content[:idx] + helper + content[idx:]

patterns = [
    # TestIntegration_EnsureSchema_MigratesLegacyTables
    r"""\tenv := NewMigrationTestEnv\(t\)\n\tenv\.SkipIfTableInSchema\("roborev", "schema_version"\)\n\tenv\.SkipIfTableInSchema\("public", "schema_version"\)\n\n\t// Clean up after test\n\tenv\.CleanupDropSchema\("roborev"\)\n\tenv\.CleanupDropTable\("public", "schema_version"\)\n\n\t// Create legacy table in public schema\n\tenv\.Exec\(`CREATE TABLE IF NOT EXISTS public\.schema_version \(version INTEGER PRIMARY KEY\)`\)\n\tenv\.Exec\(`INSERT INTO public\.schema_version \(version\) VALUES \(1\) ON CONFLICT DO NOTHING`\)\n\n\tenv\.DropSchema\("roborev"\)""",
    
    # TestIntegration_EnsureSchema_MigratesMultipleTablesAndMixedState
    r"""\tenv := NewMigrationTestEnv\(t\)\n\t// Clean up after test\n\tenv\.CleanupDropSchema\("roborev"\)\n\tenv\.CleanupDropTable\("public", "schema_version"\)\n\tenv\.CleanupDropTable\("public", "repos"\)\n\n\tenv\.DropTable\("public", "schema_version"\)\n\tenv\.DropTable\("public", "repos"\)\n\tenv\.DropSchema\("roborev"\)""",

    # TestIntegration_EnsureSchema_DualSchemaWithDataErrors
    r"""\tenv := NewMigrationTestEnv\(t\)\n\t// Clean up after test\n\tenv\.CleanupDropSchema\("roborev"\)\n\tenv\.CleanupDropTable\("public", "schema_version"\)\n\tenv\.CleanupDropTable\("public", "repos"\)\n\n\tenv\.DropTable\("public", "repos"\)\n\tenv\.DropTable\("public", "schema_version"\)\n\tenv\.DropSchema\("roborev"\)""",

    # TestIntegration_EnsureSchema_EmptyPublicTableDropped
    r"""\tenv := NewMigrationTestEnv\(t\)\n\t// Clean up after test\n\tenv\.CleanupDropSchema\("roborev"\)\n\tenv\.CleanupDropTable\("public", "schema_version"\)\n\tenv\.CleanupDropTable\("public", "repos"\)\n\n\tenv\.DropTable\("public", "repos"\)\n\tenv\.DropTable\("public", "schema_version"\)\n\tenv\.DropSchema\("roborev"\)""",

    # TestIntegration_EnsureSchema_MigratesPublicTableWithData
    r"""\tenv := NewMigrationTestEnv\(t\)\n\t// Clean up after test\n\tenv\.CleanupDropSchema\("roborev"\)\n\tenv\.CleanupDropTable\("public", "schema_version"\)\n\tenv\.CleanupDropTable\("public", "repos"\)\n\n\tenv\.DropTable\("public", "repos"\)\n\tenv\.DropTable\("public", "schema_version"\)\n\tenv\.DropSchema\("roborev"\)"""
]

# We handle the legacy test separately because of the SkipIfTableInSchema
legacy_repl = """\tenv := setupMigrationEnv(t)
	env.SkipIfTableInSchema("roborev", "schema_version")
	env.SkipIfTableInSchema("public", "schema_version")

	// Create legacy table in public schema
	env.Exec(`CREATE TABLE IF NOT EXISTS public.schema_version (version INTEGER PRIMARY KEY)`)
	env.Exec(`INSERT INTO public.schema_version (version) VALUES (1) ON CONFLICT DO NOTHING`)"""

content = re.sub(patterns[0], legacy_repl, content)

for p in patterns[1:]:
    content = re.sub(p, "\tenv := setupMigrationEnv(t)", content)

with open('internal/storage/postgres_test.go', 'w') as f:
    f.write(content)

