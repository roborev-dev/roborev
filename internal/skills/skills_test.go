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
func createMockSkill(t *testing.T, homeDir string, agent Agent, skill string) {
	t.Helper()
	dir := filepath.Join(homeDir, "."+string(agent), "skills", skill)
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
			setup:       func(t *testing.T, h string) { createMockSkill(t, h, AgentClaude, s) },
			shouldExist: true,
		})
		tests = append(tests, testCase{
			name:        "Codex with skill " + s,
			agent:       AgentCodex,
			setup:       func(t *testing.T, h string) { createMockSkill(t, h, AgentCodex, s) },
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
			createMockSkill(t, h, AgentClaude, "roborev-address")
			createMockSkill(t, h, AgentCodex, "roborev-address")
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
	tests := []struct {
		name          string
		setup         func(t *testing.T, homeDir string)
		wantResults   int
		wantAgents    []Agent // Used when expecting multiple results
		wantUpdated   int
		wantInstalled int
	}{
		{
			name: "updates Claude with address skill only",
			setup: func(t *testing.T, homeDir string) {
				createMockSkill(t, homeDir, AgentClaude, "roborev-address")
				// Create .codex but NO skills installed
				if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0755); err != nil {
					t.Fatal(err)
				}
			},
			wantResults:   1,
			wantAgents:    []Agent{AgentClaude},
			wantUpdated:   1,
			wantInstalled: len(expectedSkills) - 1,
		},
		{
			name: "updates Claude with respond skill only",
			setup: func(t *testing.T, homeDir string) {
				createMockSkill(t, homeDir, AgentClaude, "roborev-respond")
			},
			wantResults:   1,
			wantAgents:    []Agent{AgentClaude},
			wantUpdated:   1,
			wantInstalled: len(expectedSkills) - 1,
		},
		{
			name: "updates Codex with skills installed",
			setup: func(t *testing.T, homeDir string) {
				createMockSkill(t, homeDir, AgentCodex, "roborev-address")
			},
			wantResults:   1,
			wantAgents:    []Agent{AgentCodex},
			wantUpdated:   1,
			wantInstalled: len(expectedSkills) - 1,
		},
		{
			name: "updates Codex with respond skill only",
			setup: func(t *testing.T, homeDir string) {
				createMockSkill(t, homeDir, AgentCodex, "roborev-respond")
			},
			wantResults:   1,
			wantAgents:    []Agent{AgentCodex},
			wantUpdated:   1,
			wantInstalled: len(expectedSkills) - 1,
		},
		{
			name: "updates both agents when both have skills",
			setup: func(t *testing.T, homeDir string) {
				createMockSkill(t, homeDir, AgentClaude, "roborev-address")
				createMockSkill(t, homeDir, AgentCodex, "roborev-respond")
			},
			wantResults:   2,
			wantAgents:    []Agent{AgentClaude, AgentCodex},
			wantUpdated:   1,
			wantInstalled: len(expectedSkills) - 1,
		},
		{
			name: "skips both when neither has skills",
			setup: func(t *testing.T, homeDir string) {
				if err := os.MkdirAll(filepath.Join(homeDir, ".claude"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0755); err != nil {
					t.Fatal(err)
				}
			},
			wantResults:   0,
			wantAgents:    []Agent{},
			wantUpdated:   0,
			wantInstalled: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpHome := setupTestEnv(t)
			tt.setup(t, tmpHome)

			results, err := Update()
			if err != nil {
				t.Fatalf("Update failed: %v", err)
			}

			if len(results) != tt.wantResults {
				t.Fatalf("expected %d results, got %d", tt.wantResults, len(results))
			}

			if tt.wantResults > 0 {
				// Verify all expected agents are present
				agentFound := make(map[Agent]bool)
				for _, want := range tt.wantAgents {
					agentFound[want] = false
				}
				for _, r := range results {
					agentFound[r.Agent] = true
					if len(r.Updated) != tt.wantUpdated {
						t.Errorf("expected %d updated for %s, got %d", tt.wantUpdated, r.Agent, len(r.Updated))
					}
					if len(r.Installed) != tt.wantInstalled {
						t.Errorf("expected %d installed for %s, got %d", tt.wantInstalled, r.Agent, len(r.Installed))
					}
				}
				for want, found := range agentFound {
					if !found {
						t.Errorf("expected %s in results", want)
					}
				}
			}
		})
	}
}
