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
		checkFiles = append(currentSkillChecks(skillsDir), legacySkillChecks(skillsDir)...)
	case AgentCodex:
		skillsDir := filepath.Join(home, ".codex", "skills")
		checkFiles = append(currentSkillChecks(skillsDir), legacySkillChecks(skillsDir)...)
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

// legacySkills lists skill directories that have been removed and
// should be deleted from user machines during Update.
var legacySkills = []string{
	"roborev-address",
}

func currentSkillChecks(skillsDir string) []string {
	names := []string{
		"roborev-respond",
		"roborev-fix",
		"roborev-design-review",
		"roborev-design-review-branch",
		"roborev-review",
		"roborev-review-branch",
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(skillsDir, n, "SKILL.md")
	}
	return out
}

func legacySkillChecks(skillsDir string) []string {
	out := make([]string, len(legacySkills))
	for i, n := range legacySkills {
		out[i] = filepath.Join(skillsDir, n, "SKILL.md")
	}
	return out
}

// Update updates skills for agents that already have them installed
// and removes legacy skills that are no longer shipped.
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

// removeLegacySkills deletes skill directories that are no longer
// embedded in the binary.
func removeLegacySkills(agent Agent) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	var skillsDir string
	switch agent {
	case AgentClaude:
		skillsDir = filepath.Join(home, ".claude", "skills")
	case AgentCodex:
		skillsDir = filepath.Join(home, ".codex", "skills")
	default:
		return nil
	}

	for _, name := range legacySkills {
		dir := filepath.Join(skillsDir, name)
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove legacy skill %s: %w", name, err)
		}
	}
	return nil
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

	if err := removeLegacySkills(AgentClaude); err != nil {
		return result, err
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

	if err := removeLegacySkills(AgentCodex); err != nil {
		return result, err
	}
	return result, nil
}

// SkillInfo describes an available skill.
type SkillInfo struct {
	DirName     string // e.g. "roborev-fix"
	Name        string // e.g. "roborev-fix"
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
	Skills    map[string]SkillState // keyed by dir name (e.g. "roborev-fix")
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
			name = entry.Name()
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
		if after, ok := strings.CutPrefix(line, "name:"); ok {
			name = strings.TrimSpace(after)
		} else if after, ok := strings.CutPrefix(line, "description:"); ok {
			description = strings.TrimSpace(after)
		}
	}
	return name, description
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
