package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// setupTestEnv sets all home directory environment variables for cross-platform
// compatibility and returns the temp home directory path. Cleanup is automatic.
func setupTestEnv(t *testing.T) string {
	t.Helper()
	tmpHome := t.TempDir()

	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	return tmpHome
}

// createMockSkill creates an installed skill file at ~/.<agent>/skills/<skill>/SKILL.md.
func createMockSkill(t *testing.T, homeDir, agent, skill string) {
	t.Helper()
	dir := filepath.Join(homeDir, "."+agent, "skills", skill)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
}

// getResultForAgent finds the InstallResult for the given agent, or fails the test.
func getResultForAgent(t *testing.T, results []InstallResult, agent Agent) *InstallResult {
	t.Helper()
	for i := range results {
		if results[i].Agent == agent {
			return &results[i]
		}
	}
	t.Fatalf("no result found for agent %s", agent)
	return nil
}

func TestInstallClaudeSkipsWhenDirMissing(t *testing.T) {
	setupTestEnv(t)

	results, err := Install()
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	claudeResult := getResultForAgent(t, results, AgentClaude)
	if !claudeResult.Skipped {
		t.Error("expected Claude to be skipped when ~/.claude doesn't exist")
	}
	if len(claudeResult.Installed) > 0 {
		t.Errorf("expected no installed skills, got %v", claudeResult.Installed)
	}
}

func TestInstallClaudeWhenDirExists(t *testing.T) {
	tmpHome := setupTestEnv(t)

	// Create .claude directory
	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}

	results, err := Install()
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	claudeResult := getResultForAgent(t, results, AgentClaude)
	if claudeResult.Skipped {
		t.Error("expected Claude NOT to be skipped when ~/.claude exists")
	}
	if len(claudeResult.Installed) != 4 {
		t.Errorf("expected 4 installed skills, got %v", claudeResult.Installed)
	}

	// Verify Claude skill structure (flat directories with SKILL.md)
	skillsDir := filepath.Join(claudeDir, "skills")
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-address", "SKILL.md")); err != nil {
		t.Error("expected roborev-address/SKILL.md to exist")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-design-review", "SKILL.md")); err != nil {
		t.Error("expected roborev-design-review/SKILL.md to exist")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-fix", "SKILL.md")); err != nil {
		t.Error("expected roborev-fix/SKILL.md to exist")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-respond", "SKILL.md")); err != nil {
		t.Error("expected roborev-respond/SKILL.md to exist")
	}
}

func TestInstallCodexWhenDirExists(t *testing.T) {
	tmpHome := setupTestEnv(t)

	// Create .codex directory
	codexDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}

	results, err := Install()
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	codexResult := getResultForAgent(t, results, AgentCodex)
	if codexResult.Skipped {
		t.Error("expected Codex NOT to be skipped when ~/.codex exists")
	}
	if len(codexResult.Installed) != 4 {
		t.Errorf("expected 4 installed skills, got %v", codexResult.Installed)
	}

	// Verify Codex skill structure (flat directories with SKILL.md)
	skillsDir := filepath.Join(codexDir, "skills")
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-address", "SKILL.md")); err != nil {
		t.Error("expected roborev-address/SKILL.md to exist")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-design-review", "SKILL.md")); err != nil {
		t.Error("expected roborev-design-review/SKILL.md to exist")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-fix", "SKILL.md")); err != nil {
		t.Error("expected roborev-fix/SKILL.md to exist")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-respond", "SKILL.md")); err != nil {
		t.Error("expected roborev-respond/SKILL.md to exist")
	}
}

func TestInstallIdempotent(t *testing.T) {
	tmpHome := setupTestEnv(t)

	// Create .claude directory
	if err := os.MkdirAll(filepath.Join(tmpHome, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}

	// First install
	results1, err := Install()
	if err != nil {
		t.Fatalf("First install failed: %v", err)
	}

	claude1 := getResultForAgent(t, results1, AgentClaude)
	if len(claude1.Installed) != 4 {
		t.Errorf("first install: expected 4 installed, got %d", len(claude1.Installed))
	}
	if len(claude1.Updated) != 0 {
		t.Errorf("first install: expected 0 updated, got %d", len(claude1.Updated))
	}

	// Second install should show "updated" not "installed"
	results2, err := Install()
	if err != nil {
		t.Fatalf("Second install failed: %v", err)
	}

	claude2 := getResultForAgent(t, results2, AgentClaude)
	if len(claude2.Installed) != 0 {
		t.Errorf("second install: expected 0 installed, got %d", len(claude2.Installed))
	}
	if len(claude2.Updated) != 4 {
		t.Errorf("second install: expected 4 updated, got %d", len(claude2.Updated))
	}
}

func TestIsInstalledClaude(t *testing.T) {
	tmpHome := setupTestEnv(t)

	// No .claude dir - not installed
	if IsInstalled(AgentClaude) {
		t.Error("expected IsInstalled=false when ~/.claude doesn't exist")
	}

	// Create .claude but no skills
	if err := os.MkdirAll(filepath.Join(tmpHome, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if IsInstalled(AgentClaude) {
		t.Error("expected IsInstalled=false when no skills installed")
	}

	// Create only respond skill (not address)
	createMockSkill(t, tmpHome, "claude", "roborev-respond")
	if !IsInstalled(AgentClaude) {
		t.Error("expected IsInstalled=true when roborev-respond/SKILL.md exists")
	}

	// Remove respond, add address
	os.RemoveAll(filepath.Join(tmpHome, ".claude", "skills", "roborev-respond"))
	createMockSkill(t, tmpHome, "claude", "roborev-address")
	if !IsInstalled(AgentClaude) {
		t.Error("expected IsInstalled=true when roborev-address/SKILL.md exists")
	}
}

func TestIsInstalledCodex(t *testing.T) {
	tmpHome := setupTestEnv(t)

	// No .codex dir - not installed
	if IsInstalled(AgentCodex) {
		t.Error("expected IsInstalled=false when ~/.codex doesn't exist")
	}

	// Create .codex but no skills
	if err := os.MkdirAll(filepath.Join(tmpHome, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}
	if IsInstalled(AgentCodex) {
		t.Error("expected IsInstalled=false when no skills installed")
	}

	// Create only respond skill
	createMockSkill(t, tmpHome, "codex", "roborev-respond")
	if !IsInstalled(AgentCodex) {
		t.Error("expected IsInstalled=true when roborev-respond/SKILL.md exists")
	}
}

func TestUpdateOnlyUpdatesInstalled(t *testing.T) {
	t.Run("updates Claude with address skill only", func(t *testing.T) {
		tmpHome := setupTestEnv(t)

		createMockSkill(t, tmpHome, "claude", "roborev-address")

		// Create .codex but NO skills installed
		if err := os.MkdirAll(filepath.Join(tmpHome, ".codex"), 0755); err != nil {
			t.Fatal(err)
		}

		results, err := Update()
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		// Should only have Claude result (Codex not installed)
		if len(results) != 1 {
			t.Errorf("expected 1 result (Claude only), got %d", len(results))
		}
		if len(results) > 0 && results[0].Agent != AgentClaude {
			t.Errorf("expected Claude result, got %s", results[0].Agent)
		}
	})

	t.Run("updates Claude with respond skill only", func(t *testing.T) {
		tmpHome := setupTestEnv(t)

		createMockSkill(t, tmpHome, "claude", "roborev-respond")

		results, err := Update()
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("expected 1 result, got %d", len(results))
		}
		if len(results) > 0 && results[0].Agent != AgentClaude {
			t.Errorf("expected Claude result, got %s", results[0].Agent)
		}
		// Should update respond (existed) and install address+design-review+fix (didn't exist)
		if len(results) > 0 {
			if len(results[0].Updated) != 1 {
				t.Errorf("expected 1 updated (respond), got %d", len(results[0].Updated))
			}
			if len(results[0].Installed) != 3 {
				t.Errorf("expected 3 installed (address, design-review, fix), got %d", len(results[0].Installed))
			}
		}
	})

	t.Run("updates Codex with skills installed", func(t *testing.T) {
		tmpHome := setupTestEnv(t)

		createMockSkill(t, tmpHome, "codex", "roborev-address")

		results, err := Update()
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("expected 1 result (Codex only), got %d", len(results))
		}
		if len(results) > 0 && results[0].Agent != AgentCodex {
			t.Errorf("expected Codex result, got %s", results[0].Agent)
		}
	})

	t.Run("updates Codex with respond skill only", func(t *testing.T) {
		tmpHome := setupTestEnv(t)

		createMockSkill(t, tmpHome, "codex", "roborev-respond")

		results, err := Update()
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("expected 1 result, got %d", len(results))
		}
		if len(results) > 0 && results[0].Agent != AgentCodex {
			t.Errorf("expected Codex result, got %s", results[0].Agent)
		}
	})

	t.Run("updates both agents when both have skills", func(t *testing.T) {
		tmpHome := setupTestEnv(t)

		createMockSkill(t, tmpHome, "claude", "roborev-address")
		createMockSkill(t, tmpHome, "codex", "roborev-respond")

		results, err := Update()
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		if len(results) != 2 {
			t.Errorf("expected 2 results (both agents), got %d", len(results))
		}

		// Verify both agents are present (not duplicates)
		var hasClaude, hasCodex bool
		for _, r := range results {
			if r.Agent == AgentClaude {
				hasClaude = true
			}
			if r.Agent == AgentCodex {
				hasCodex = true
			}
		}
		if !hasClaude {
			t.Error("expected Claude in results")
		}
		if !hasCodex {
			t.Error("expected Codex in results")
		}
	})

	t.Run("skips both when neither has skills", func(t *testing.T) {
		tmpHome := setupTestEnv(t)

		// Create .claude and .codex dirs but no skills
		if err := os.MkdirAll(filepath.Join(tmpHome, ".claude"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(tmpHome, ".codex"), 0755); err != nil {
			t.Fatal(err)
		}

		results, err := Update()
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		if len(results) != 0 {
			t.Errorf("expected 0 results (no skills installed), got %d", len(results))
		}
	})
}
