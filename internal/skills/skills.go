// Package skills provides embedded skill files for AI agents (Claude Code, Codex)
// and installation utilities.
package skills

import (
	"bufio"
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
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
			filepath.Join(skillsDir, "roborev-fix", "SKILL.md"),
			filepath.Join(skillsDir, "roborev-design-review", "SKILL.md"),
		}
	case AgentCodex:
		skillsDir := filepath.Join(home, ".codex", "skills")
		checkFiles = []string{
			filepath.Join(skillsDir, "roborev-address", "SKILL.md"),
			filepath.Join(skillsDir, "roborev-respond", "SKILL.md"),
			filepath.Join(skillsDir, "roborev-fix", "SKILL.md"),
			filepath.Join(skillsDir, "roborev-design-review", "SKILL.md"),
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

// SkillInfo describes an available skill.
type SkillInfo struct {
	DirName     string // e.g. "roborev-address"
	Name        string // e.g. "roborev:address"
	Description string
}

// SkillState describes whether a skill is installed and up to date for an agent.
type SkillState int

const (
	SkillMissing  SkillState = iota // Not installed
	SkillCurrent                    // Installed and matches embedded version
	SkillOutdated                   // Installed but content differs from embedded
)

// AgentStatus describes the installation state for a single agent.
type AgentStatus struct {
	Agent     Agent
	Available bool                  // Whether the agent config dir exists
	Skills    map[string]SkillState // keyed by dir name (e.g. "roborev-address")
}

// ListSkills returns metadata for all embedded skills (from the Claude skill set).
func ListSkills() ([]SkillInfo, error) {
	entries, err := fs.ReadDir(claudeSkills, "claude")
	if err != nil {
		return nil, fmt.Errorf("read embedded skills: %w", err)
	}

	var out []SkillInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		content, err := claudeSkills.ReadFile(path.Join("claude", entry.Name(), "SKILL.md"))
		if err != nil {
			return nil, fmt.Errorf("read %s/SKILL.md: %w", entry.Name(), err)
		}
		name, desc := parseFrontmatter(content)
		if name == "" {
			name = strings.Replace(entry.Name(), "roborev-", "roborev:", 1)
		}
		out = append(out, SkillInfo{DirName: entry.Name(), Name: name, Description: desc})
	}
	return out, nil
}

// Status returns per-agent, per-skill installation state.
func Status() []AgentStatus {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	type agentDef struct {
		agent     Agent
		configDir string
		embedFS   embed.FS
		embedDir  string
	}

	agents := []agentDef{
		{AgentClaude, filepath.Join(home, ".claude"), claudeSkills, "claude"},
		{AgentCodex, filepath.Join(home, ".codex"), codexSkills, "codex"},
	}

	var out []AgentStatus
	for _, a := range agents {
		status := AgentStatus{
			Agent:  a.agent,
			Skills: make(map[string]SkillState),
		}

		if _, err := os.Stat(a.configDir); err != nil {
			out = append(out, status)
			continue
		}
		status.Available = true

		entries, err := fs.ReadDir(a.embedFS, a.embedDir)
		if err != nil {
			out = append(out, status)
			continue
		}

		skillsDir := filepath.Join(a.configDir, "skills")
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			installedPath := filepath.Join(skillsDir, name, "SKILL.md")

			installedContent, err := os.ReadFile(installedPath)
			if err != nil {
				status.Skills[name] = SkillMissing
				continue
			}

			embeddedContent, err := a.embedFS.ReadFile(path.Join(a.embedDir, name, "SKILL.md"))
			if err != nil {
				status.Skills[name] = SkillMissing
				continue
			}

			if bytes.Equal(installedContent, embeddedContent) {
				status.Skills[name] = SkillCurrent
			} else {
				status.Skills[name] = SkillOutdated
			}
		}

		out = append(out, status)
	}
	return out
}

// parseFrontmatter extracts name and description from YAML frontmatter.
func parseFrontmatter(data []byte) (name, description string) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return "", ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		if strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return name, description
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
