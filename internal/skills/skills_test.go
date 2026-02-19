package skills

import (
	"os"
	"path/filepath"
	"testing"
)

var expectedSkills = []string{
	"roborev-address",
	"roborev-design-review",
	"roborev-design-review-branch",
	"roborev-fix",
	"roborev-respond",
	"roborev-review",
	"roborev-review-branch",
}

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

func assertSkillsInstalled(t *testing.T, agentDir string) {
	t.Helper()
	skillsDir := filepath.Join(agentDir, "skills")
	for _, skill := range expectedSkills {
		path := filepath.Join(skillsDir, skill, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist", path)
		}
	}
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

func TestInstallWhenDirExists(t *testing.T) {
	tests := []struct {
		agent   Agent
		dirName string
	}{
		{AgentClaude, ".claude"},
		{AgentCodex, ".codex"},
	}

	for _, tt := range tests {
		t.Run(string(tt.agent), func(t *testing.T) {
			tmpHome := setupTestEnv(t)
			agentDir := filepath.Join(tmpHome, tt.dirName)
			if err := os.MkdirAll(agentDir, 0755); err != nil {
				t.Fatal(err)
			}

			results, err := Install()
			if err != nil {
				t.Fatalf("Install failed: %v", err)
			}

			res := getResultForAgent(t, results, tt.agent)
			if res.Skipped {
				t.Error("expected not to be skipped")
			}
			if len(res.Installed) != len(expectedSkills) {
				t.Errorf("expected %d installed skills, got %d", len(expectedSkills), len(res.Installed))
			}
			assertSkillsInstalled(t, agentDir)
		})
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
	if len(claude1.Installed) != len(expectedSkills) {
		t.Errorf("first install: expected %d installed, got %d", len(expectedSkills), len(claude1.Installed))
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
	if len(claude2.Updated) != len(expectedSkills) {
		t.Errorf("second install: expected %d updated, got %d", len(expectedSkills), len(claude2.Updated))
	}
}

func TestIsInstalled(t *testing.T) {
	type testCase struct {
		name        string
		agent       Agent
		setup       func(t *testing.T, home string)
		shouldExist bool
	}

	tests := []testCase{
		{
			name:        "Claude missing dir",
			agent:       AgentClaude,
			setup:       func(t *testing.T, h string) {},
			shouldExist: false,
		},
		{
			name:  "Claude dir exists no skills",
			agent: AgentClaude,
			setup: func(t *testing.T, h string) {
				if err := os.MkdirAll(filepath.Join(h, ".claude"), 0755); err != nil {
					t.Fatal(err)
				}
			},
			shouldExist: false,
		},
		{
			name:        "Codex missing dir",
			agent:       AgentCodex,
			setup:       func(t *testing.T, h string) {},
			shouldExist: false,
		},
		{
			name:  "Codex dir exists no skills",
			agent: AgentCodex,
			setup: func(t *testing.T, h string) {
				if err := os.MkdirAll(filepath.Join(h, ".codex"), 0755); err != nil {
					t.Fatal(err)
				}
			},
			shouldExist: false,
		},
	}

	for _, skill := range expectedSkills {
		// Capture variable for closure
		s := skill
		tests = append(tests, testCase{
			name:        "Claude with skill " + s,
			agent:       AgentClaude,
			setup:       func(t *testing.T, h string) { createMockSkill(t, h, "claude", s) },
			shouldExist: true,
		})
		tests = append(tests, testCase{
			name:        "Codex with skill " + s,
			agent:       AgentCodex,
			setup:       func(t *testing.T, h string) { createMockSkill(t, h, "codex", s) },
			shouldExist: true,
		})
	}

	// Unsupported agent should always return false.
	tests = append(tests, testCase{
		name:  "unsupported agent",
		agent: Agent("unknown"),
		setup: func(t *testing.T, h string) {
			// Install skills for both known agents to ensure
			// the unknown agent still returns false.
			createMockSkill(t, h, "claude", "roborev-address")
			createMockSkill(t, h, "codex", "roborev-address")
		},
		shouldExist: false,
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpHome := setupTestEnv(t)
			if tt.setup != nil {
				tt.setup(t, tmpHome)
			}
			if got := IsInstalled(tt.agent); got != tt.shouldExist {
				t.Errorf("IsInstalled(%s) = %v, want %v", tt.agent, got, tt.shouldExist)
			}
		})
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
			if len(results[0].Installed) != len(expectedSkills)-1 {
				t.Errorf("expected %d installed, got %d", len(expectedSkills)-1, len(results[0].Installed))
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
		// Should update respond (existed) and install the rest (didn't exist)
		if len(results) > 0 {
			if len(results[0].Updated) != 1 {
				t.Errorf("expected 1 updated (respond), got %d", len(results[0].Updated))
			}
			if len(results[0].Installed) != len(expectedSkills)-1 {
				t.Errorf("expected %d installed, got %d", len(expectedSkills)-1, len(results[0].Installed))
			}
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
