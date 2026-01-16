package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallClaudeSkipsWhenDirMissing(t *testing.T) {
	// Use temp HOME without .claude directory
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	results, err := Install()
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// Find Claude result
	var claudeResult *InstallResult
	for i := range results {
		if results[i].Agent == AgentClaude {
			claudeResult = &results[i]
			break
		}
	}

	if claudeResult == nil {
		t.Fatal("expected Claude result")
	}
	if !claudeResult.Skipped {
		t.Error("expected Claude to be skipped when ~/.claude doesn't exist")
	}
	if len(claudeResult.Installed) > 0 {
		t.Errorf("expected no installed skills, got %v", claudeResult.Installed)
	}
}

func TestInstallClaudeWhenDirExists(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create .claude directory
	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}

	results, err := Install()
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	var claudeResult *InstallResult
	for i := range results {
		if results[i].Agent == AgentClaude {
			claudeResult = &results[i]
			break
		}
	}

	if claudeResult == nil {
		t.Fatal("expected Claude result")
	}
	if claudeResult.Skipped {
		t.Error("expected Claude NOT to be skipped when ~/.claude exists")
	}
	if len(claudeResult.Installed) != 2 {
		t.Errorf("expected 2 installed skills, got %v", claudeResult.Installed)
	}

	// Verify files exist
	skillsDir := filepath.Join(claudeDir, "skills")
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-address.md")); err != nil {
		t.Error("expected roborev-address.md to exist")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-respond.md")); err != nil {
		t.Error("expected roborev-respond.md to exist")
	}
}

func TestInstallCodexWhenDirExists(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create .codex directory
	codexDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}

	results, err := Install()
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	var codexResult *InstallResult
	for i := range results {
		if results[i].Agent == AgentCodex {
			codexResult = &results[i]
			break
		}
	}

	if codexResult == nil {
		t.Fatal("expected Codex result")
	}
	if codexResult.Skipped {
		t.Error("expected Codex NOT to be skipped when ~/.codex exists")
	}
	if len(codexResult.Installed) != 2 {
		t.Errorf("expected 2 installed skills, got %v", codexResult.Installed)
	}

	// Verify Codex skill structure (directories with SKILL.md)
	skillsDir := filepath.Join(codexDir, "skills")
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-address", "SKILL.md")); err != nil {
		t.Error("expected roborev-address/SKILL.md to exist")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "roborev-respond", "SKILL.md")); err != nil {
		t.Error("expected roborev-respond/SKILL.md to exist")
	}
}

func TestInstallIdempotent(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create .claude directory
	if err := os.MkdirAll(filepath.Join(tmpHome, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}

	// First install
	results1, err := Install()
	if err != nil {
		t.Fatalf("First install failed: %v", err)
	}

	var claude1 *InstallResult
	for i := range results1 {
		if results1[i].Agent == AgentClaude {
			claude1 = &results1[i]
			break
		}
	}
	if len(claude1.Installed) != 2 {
		t.Errorf("first install: expected 2 installed, got %d", len(claude1.Installed))
	}
	if len(claude1.Updated) != 0 {
		t.Errorf("first install: expected 0 updated, got %d", len(claude1.Updated))
	}

	// Second install should show "updated" not "installed"
	results2, err := Install()
	if err != nil {
		t.Fatalf("Second install failed: %v", err)
	}

	var claude2 *InstallResult
	for i := range results2 {
		if results2[i].Agent == AgentClaude {
			claude2 = &results2[i]
			break
		}
	}
	if len(claude2.Installed) != 0 {
		t.Errorf("second install: expected 0 installed, got %d", len(claude2.Installed))
	}
	if len(claude2.Updated) != 2 {
		t.Errorf("second install: expected 2 updated, got %d", len(claude2.Updated))
	}
}

func TestIsInstalledClaude(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// No .claude dir - not installed
	if IsInstalled(AgentClaude) {
		t.Error("expected IsInstalled=false when ~/.claude doesn't exist")
	}

	// Create .claude but no skills
	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if IsInstalled(AgentClaude) {
		t.Error("expected IsInstalled=false when no skills installed")
	}

	// Create only respond skill (not address)
	skillsDir := filepath.Join(claudeDir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "roborev-respond.md"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsInstalled(AgentClaude) {
		t.Error("expected IsInstalled=true when roborev-respond.md exists")
	}

	// Remove respond, add address
	os.Remove(filepath.Join(skillsDir, "roborev-respond.md"))
	if err := os.WriteFile(filepath.Join(skillsDir, "roborev-address.md"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsInstalled(AgentClaude) {
		t.Error("expected IsInstalled=true when roborev-address.md exists")
	}
}

func TestIsInstalledCodex(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// No .codex dir - not installed
	if IsInstalled(AgentCodex) {
		t.Error("expected IsInstalled=false when ~/.codex doesn't exist")
	}

	// Create .codex but no skills
	codexDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}
	if IsInstalled(AgentCodex) {
		t.Error("expected IsInstalled=false when no skills installed")
	}

	// Create only respond skill
	skillsDir := filepath.Join(codexDir, "skills", "roborev-respond")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsInstalled(AgentCodex) {
		t.Error("expected IsInstalled=true when roborev-respond/SKILL.md exists")
	}
}

func TestUpdateOnlyUpdatesInstalled(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create .claude with skills installed
	claudeSkillsDir := filepath.Join(tmpHome, ".claude", "skills")
	if err := os.MkdirAll(claudeSkillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeSkillsDir, "roborev-address.md"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .codex but NO skills installed
	codexDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Update should only update Claude (which has skills), not Codex
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
}
