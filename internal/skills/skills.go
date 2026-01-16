// Package skills provides embedded skill files for AI agents (Claude Code, Codex)
// and installation utilities.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

//go:embed claude/*/SKILL.md
var claudeSkills embed.FS

//go:embed codex/*/SKILL.md
var codexSkills embed.FS

// Agent represents a supported AI agent
type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
)

// InstallResult contains the result of a skill installation
type InstallResult struct {
	Agent     Agent
	Installed []string
	Updated   []string
	Skipped   bool // True if agent config dir doesn't exist
}

// Install installs skills for all supported agents whose config directories exist.
// It is idempotent - running multiple times will update existing skills.
func Install() ([]InstallResult, error) {
	var results []InstallResult

	// Install Claude skills
	result, err := installClaude()
	if err != nil {
		return nil, fmt.Errorf("claude skills: %w", err)
	}
	results = append(results, result)

	// Install Codex skills
	result, err = installCodex()
	if err != nil {
		return nil, fmt.Errorf("codex skills: %w", err)
	}
	results = append(results, result)

	return results, nil
}

// IsInstalled checks if any roborev skills are installed for the given agent
func IsInstalled(agent Agent) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	var checkFiles []string

	switch agent {
	case AgentClaude:
		skillsDir := filepath.Join(home, ".claude", "skills")
		checkFiles = []string{
			filepath.Join(skillsDir, "roborev-address", "SKILL.md"),
			filepath.Join(skillsDir, "roborev-respond", "SKILL.md"),
		}
	case AgentCodex:
		skillsDir := filepath.Join(home, ".codex", "skills")
		checkFiles = []string{
			filepath.Join(skillsDir, "roborev-address", "SKILL.md"),
			filepath.Join(skillsDir, "roborev-respond", "SKILL.md"),
		}
	default:
		return false
	}

	// Return true if any skill file exists
	for _, f := range checkFiles {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// Update updates skills for agents that already have them installed
func Update() ([]InstallResult, error) {
	var results []InstallResult

	if IsInstalled(AgentClaude) {
		result, err := installClaude()
		if err != nil {
			return nil, fmt.Errorf("update claude skills: %w", err)
		}
		results = append(results, result)
	}

	if IsInstalled(AgentCodex) {
		result, err := installCodex()
		if err != nil {
			return nil, fmt.Errorf("update codex skills: %w", err)
		}
		results = append(results, result)
	}

	return results, nil
}

func installClaude() (InstallResult, error) {
	result := InstallResult{Agent: AgentClaude}

	home, err := os.UserHomeDir()
	if err != nil {
		return result, fmt.Errorf("get home dir: %w", err)
	}

	// Check if ~/.claude exists
	claudeDir := filepath.Join(home, ".claude")
	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		result.Skipped = true
		return result, nil
	}

	// Create skills directory
	skillsDir := filepath.Join(claudeDir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return result, fmt.Errorf("create skills dir: %w", err)
	}

	// Install each skill directory
	entries, err := fs.ReadDir(claudeSkills, "claude")
	if err != nil {
		return result, fmt.Errorf("read embedded skills: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillName := entry.Name()
		skillDir := filepath.Join(skillsDir, skillName)

		// Create skill directory
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			return result, fmt.Errorf("create %s dir: %w", skillName, err)
		}

		// Read SKILL.md (use path.Join for embed FS - requires forward slashes)
		content, err := claudeSkills.ReadFile(path.Join("claude", skillName, "SKILL.md"))
		if err != nil {
			return result, fmt.Errorf("read %s/SKILL.md: %w", skillName, err)
		}

		destPath := filepath.Join(skillDir, "SKILL.md")
		existed := fileExists(destPath)

		if err := os.WriteFile(destPath, content, 0644); err != nil {
			return result, fmt.Errorf("write %s/SKILL.md: %w", skillName, err)
		}

		if existed {
			result.Updated = append(result.Updated, skillName)
		} else {
			result.Installed = append(result.Installed, skillName)
		}
	}

	return result, nil
}

func installCodex() (InstallResult, error) {
	result := InstallResult{Agent: AgentCodex}

	home, err := os.UserHomeDir()
	if err != nil {
		return result, fmt.Errorf("get home dir: %w", err)
	}

	// Check if ~/.codex exists
	codexDir := filepath.Join(home, ".codex")
	if _, err := os.Stat(codexDir); os.IsNotExist(err) {
		result.Skipped = true
		return result, nil
	}

	// Create skills directory
	skillsDir := filepath.Join(codexDir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return result, fmt.Errorf("create skills dir: %w", err)
	}

	// Install each skill directory
	entries, err := fs.ReadDir(codexSkills, "codex")
	if err != nil {
		return result, fmt.Errorf("read embedded skills: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillName := entry.Name()
		skillDir := filepath.Join(skillsDir, skillName)

		// Create skill directory
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			return result, fmt.Errorf("create %s dir: %w", skillName, err)
		}

		// Read SKILL.md (use path.Join for embed FS - requires forward slashes)
		content, err := codexSkills.ReadFile(path.Join("codex", skillName, "SKILL.md"))
		if err != nil {
			return result, fmt.Errorf("read %s/SKILL.md: %w", skillName, err)
		}

		destPath := filepath.Join(skillDir, "SKILL.md")
		existed := fileExists(destPath)

		if err := os.WriteFile(destPath, content, 0644); err != nil {
			return result, fmt.Errorf("write %s/SKILL.md: %w", skillName, err)
		}

		if existed {
			result.Updated = append(result.Updated, skillName)
		} else {
			result.Installed = append(result.Installed, skillName)
		}
	}

	return result, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
