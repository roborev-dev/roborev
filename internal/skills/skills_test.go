package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var expectedSkills = []string{
	"roborev-design-review",
	"roborev-design-review-branch",
	"roborev-fix",
	"roborev-respond",
	"roborev-review",
	"roborev-review-branch",
}

func setupTestEnv(t *testing.T) string {
	t.Helper()
	tmpHome := t.TempDir()

	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	return tmpHome
}

func createMockSkill(t *testing.T, homeDir string, agent Agent, skill string) {
	t.Helper()
	dir := filepath.Join(homeDir, "."+string(agent), "skills", skill)
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("old"), 0644))
}

func getResultForAgent(t *testing.T, results []InstallResult, agent Agent) *InstallResult {
	t.Helper()
	for i := range results {
		if results[i].Agent == agent {
			return &results[i]
		}
	}
	require.Condition(t, func() bool { return false }, "missing install result: no result found for agent %s", agent)
	return nil
}

func assertSkillsInstalled(t *testing.T, agentDir string) {
	t.Helper()
	skillsDir := filepath.Join(agentDir, "skills")
	for _, skill := range expectedSkills {
		path := filepath.Join(skillsDir, skill, "SKILL.md")
		_, err := os.Stat(path)
		require.NoError(t, err, "expected %s to exist", path)
	}
}

func TestInstallClaudeSkipsWhenDirMissing(t *testing.T) {
	setupTestEnv(t)

	results, err := Install()
	require.NoError(t, err, "Install failed")

	claudeResult := getResultForAgent(t, results, AgentClaude)
	assert.True(t, claudeResult.Skipped, "expected Claude to be skipped when ~/.claude doesn't exist")
	assert.Empty(t, claudeResult.Installed, "expected no installed skills")
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
			require.NoError(t, os.MkdirAll(agentDir, 0755))

			results, err := Install()
			require.NoError(t, err, "Install failed")

			res := getResultForAgent(t, results, tt.agent)
			assert.False(t, res.Skipped, "expected not to be skipped")
			assert.Len(t, res.Installed, len(expectedSkills))
			assertSkillsInstalled(t, agentDir)
		})
	}
}

func TestInstallIdempotent(t *testing.T) {
	tmpHome := setupTestEnv(t)

	err := os.MkdirAll(filepath.Join(tmpHome, ".claude"), 0o755)
	require.NoError(t, err)

	results1, err := Install()
	require.NoError(t, err, "First install failed: %v", err)

	claude1 := getResultForAgent(t, results1, AgentClaude)
	require.Len(t, claude1.Installed, len(expectedSkills), "first install: expected %d installed, got %d", len(expectedSkills), len(claude1.Installed))
	require.Empty(t, claude1.Updated, "first install: expected 0 updated, got %d", len(claude1.Updated))

	results2, err := Install()
	require.NoError(t, err, "Second install failed: %v", err)

	claude2 := getResultForAgent(t, results2, AgentClaude)
	require.Empty(t, claude2.Installed, "second install: expected 0 installed, got %d", len(claude2.Installed))
	require.Len(t, claude2.Updated, len(expectedSkills), "second install: expected %d updated, got %d", len(expectedSkills), len(claude2.Updated))
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
				err := os.MkdirAll(filepath.Join(h, ".claude"), 0o755)
				require.NoError(t, err)
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
				err := os.MkdirAll(filepath.Join(h, ".codex"), 0o755)
				require.NoError(t, err)
			},
			shouldExist: false,
		},
	}

	for _, skill := range expectedSkills {

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

	tests = append(tests, testCase{
		name:  "unsupported agent",
		agent: Agent("unknown"),
		setup: func(t *testing.T, h string) {
			createMockSkill(t, h, AgentClaude, "roborev-fix")
			createMockSkill(t, h, AgentCodex, "roborev-fix")
		},
		shouldExist: false,
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpHome := setupTestEnv(t)
			if tt.setup != nil {
				tt.setup(t, tmpHome)
			}
			require.Equal(t, tt.shouldExist, IsInstalled(tt.agent), "IsInstalled(%s) = %v, want %v", tt.agent, IsInstalled(tt.agent), tt.shouldExist)
		})
	}
}

func TestInstallRemovesLegacySkills(t *testing.T) {
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

			require.NoError(t, os.MkdirAll(filepath.Join(tmpHome, tt.dirName), 0755))
			createMockSkill(t, tmpHome, tt.agent, "roborev-address")

			_, err := Install()
			require.NoError(t, err)

			legacyDir := filepath.Join(tmpHome, tt.dirName, "skills", "roborev-address")
			_, err = os.Stat(legacyDir)
			assert.True(t, os.IsNotExist(err), "expected legacy dir to be removed after install")

			assertSkillsInstalled(t, filepath.Join(tmpHome, tt.dirName))
		})
	}
}

func TestUpdateRemovesLegacySkills(t *testing.T) {
	tmpHome := setupTestEnv(t)

	// Install a current skill so IsInstalled returns true
	createMockSkill(t, tmpHome, AgentClaude, "roborev-fix")

	// Plant the legacy skill
	legacyDir := filepath.Join(tmpHome, ".claude", "skills", "roborev-address")
	require.NoError(t, os.MkdirAll(legacyDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "SKILL.md"), []byte("old"), 0644))

	_, err := Update()
	require.NoError(t, err)

	// Legacy skill should be removed
	_, err = os.Stat(legacyDir)
	assert.True(t, os.IsNotExist(err), "expected legacy roborev-address dir to be removed")
}

func TestUpdateLegacyOnlyInstall(t *testing.T) {
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

			// User only has the deprecated skill — no current skills
			createMockSkill(t, tmpHome, tt.agent, "roborev-address")

			results, err := Update()
			require.NoError(t, err)

			require.Len(t, results, 1)
			res := getResultForAgent(t, results, tt.agent)
			assert.Len(t, res.Installed, len(expectedSkills))

			// Legacy dir should be removed
			legacyDir := filepath.Join(tmpHome, tt.dirName, "skills", "roborev-address")
			_, err = os.Stat(legacyDir)
			assert.True(t, os.IsNotExist(err), "expected legacy dir to be removed")
		})
	}
}

func TestUpdateOnlyUpdatesInstalled(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(t *testing.T, homeDir string)
		wantResults   int
		wantAgents    []Agent
		wantUpdated   int
		wantInstalled int
	}{
		{
			name: "updates Claude with fix skill only",
			setup: func(t *testing.T, homeDir string) {
				createMockSkill(t, homeDir, AgentClaude, "roborev-fix")

				err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0o755)
				require.NoError(t, err)
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
			name: "updates Codex with fix skill only",
			setup: func(t *testing.T, homeDir string) {
				createMockSkill(t, homeDir, AgentCodex, "roborev-fix")
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
				createMockSkill(t, homeDir, AgentClaude, "roborev-fix")
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
				err := os.MkdirAll(filepath.Join(homeDir, ".claude"), 0o755)
				require.NoError(t, err)
				err = os.MkdirAll(filepath.Join(homeDir, ".codex"), 0o755)
				require.NoError(t, err)
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
			require.NoError(t, err, "Update failed: %v", err)
			require.NoError(t, err, "expected %d results, got %d", tt.wantResults, len(results))

			if tt.wantResults > 0 {

				agentFound := make(map[Agent]bool)
				for _, want := range tt.wantAgents {
					agentFound[want] = false
				}
				for _, r := range results {
					agentFound[r.Agent] = true
					require.Len(t, r.Updated, tt.wantUpdated, "expected %d updated for %s, got %d", tt.wantUpdated, r.Agent, len(r.Updated))
					require.Len(t, r.Installed, tt.wantInstalled, "expected %d installed for %s, got %d", tt.wantInstalled, r.Agent, len(r.Installed))
				}
				for want, found := range agentFound {
					require.True(t, found, "expected %s in results", want)
				}
			}
		})
	}
}
