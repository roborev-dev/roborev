// Package ghaction generates GitHub Actions workflow files for roborev CI reviews.
package ghaction

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// Allowed values for validation (prevent injection).
var (
	allowedAgents     = []string{"codex", "claude-code", "gemini", "copilot", "opencode", "cursor", "droid"}
	allowedReasoning  = []string{"thorough", "standard", "fast"}
	allowedTypes      = []string{"security", "design", "default", "review", "general"}
	safeIdentifierRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	safeVersionRE     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$`)
	safeModelStringRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]*$`)
)

// WorkflowConfig holds the parameters for generating a GitHub Actions workflow.
type WorkflowConfig struct {
	// Agent is the AI agent to use (e.g., "codex", "claude-code", "gemini").
	Agent string

	// Model overrides the default model for the agent.
	Model string

	// ReviewTypes is the list of review types to run (e.g., ["security"]).
	// Each type produces a separate review invocation.
	ReviewTypes []string

	// Reasoning is the reasoning level (thorough, standard, fast).
	Reasoning string

	// SecretName is the GitHub Actions secret name that holds the agent API key.
	SecretName string

	// RoborevVersion is the roborev release version to install. Empty means "latest".
	RoborevVersion string
}

// DefaultConfig returns a WorkflowConfig with sensible defaults.
func DefaultConfig() WorkflowConfig {
	return WorkflowConfig{
		Agent:       "codex",
		ReviewTypes: []string{"security"},
		Reasoning:   "thorough",
		SecretName:  "ROBOREV_API_KEY",
	}
}

// Validate checks all config fields against allowlists and safe patterns.
// Returns an error describing the first invalid field found.
func (c *WorkflowConfig) Validate() error {
	if !contains(allowedAgents, c.Agent) {
		return fmt.Errorf("invalid agent %q (valid: %s)", c.Agent, strings.Join(allowedAgents, ", "))
	}
	if !contains(allowedReasoning, c.Reasoning) {
		return fmt.Errorf("invalid reasoning %q (valid: %s)", c.Reasoning, strings.Join(allowedReasoning, ", "))
	}
	for _, rt := range c.ReviewTypes {
		if !contains(allowedTypes, rt) {
			return fmt.Errorf("invalid review type %q (valid: %s)", rt, strings.Join(allowedTypes, ", "))
		}
	}
	if !safeIdentifierRE.MatchString(c.SecretName) {
		return fmt.Errorf("invalid secret name %q (must match %s)", c.SecretName, safeIdentifierRE.String())
	}
	if c.RoborevVersion != "" && !safeVersionRE.MatchString(c.RoborevVersion) {
		return fmt.Errorf("invalid roborev version %q (expected semver like 0.33.1)", c.RoborevVersion)
	}
	if c.Model != "" && !safeModelStringRE.MatchString(c.Model) {
		return fmt.Errorf("invalid model %q (must match %s)", c.Model, safeModelStringRE.String())
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// agentEnvVar returns the environment variable name that the agent expects
// for API authentication.
func agentEnvVar(agent string) string {
	switch agent {
	case "claude-code":
		return "ANTHROPIC_API_KEY"
	case "gemini":
		return "GOOGLE_API_KEY"
	case "copilot":
		return "GITHUB_TOKEN"
	default:
		return "OPENAI_API_KEY"
	}
}

// agentInstallCmd returns the shell command(s) to install the given agent CLI.
func agentInstallCmd(agent string) string {
	switch agent {
	case "claude-code":
		return "npm install -g @anthropic-ai/claude-code"
	case "gemini":
		return "npm install -g @anthropic-ai/gemini-cli || echo 'Note: gemini agent requires the Gemini CLI; see https://github.com/google-gemini/gemini-cli'"
	case "copilot":
		return "gh extension install github/gh-copilot || true"
	case "codex":
		return "npm install -g @openai/codex"
	case "opencode":
		return "go install github.com/opencode-ai/opencode@latest"
	case "cursor":
		return "echo 'Cursor agent is not available in CI environments; choose a different agent'"
	case "droid":
		return "pip install droid-cli || echo 'Note: droid agent may require additional setup; see Factory documentation'"
	default:
		return "echo 'Install your agent CLI manually'"
	}
}

// Generate produces a GitHub Actions workflow YAML string from the given config.
func Generate(cfg WorkflowConfig) (string, error) {
	// Apply defaults for empty fields
	if cfg.Agent == "" {
		cfg.Agent = "codex"
	}
	if len(cfg.ReviewTypes) == 0 {
		cfg.ReviewTypes = []string{"security"}
	}
	if cfg.Reasoning == "" {
		cfg.Reasoning = "thorough"
	}
	if cfg.SecretName == "" {
		cfg.SecretName = "ROBOREV_API_KEY"
	}

	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("invalid config: %w", err)
	}

	// Build review commands: one per review type (--type accepts a single value).
	// Types "default", "review", and "general" are standard reviews (no --type flag).
	var reviewCmds []string
	for _, rt := range cfg.ReviewTypes {
		cmd := "roborev review"
		cmd += " --local"
		cmd += " --agent " + cfg.Agent
		cmd += " --reasoning " + cfg.Reasoning
		if cfg.Model != "" {
			cmd += " --model " + cfg.Model
		}
		if rt != "default" && rt != "review" && rt != "general" {
			cmd += " --type " + rt
		}
		cmd += ` "${COMMIT}"`
		reviewCmds = append(reviewCmds, cmd)
	}

	data := templateData{
		Agent:           cfg.Agent,
		Model:           cfg.Model,
		Reasoning:       cfg.Reasoning,
		SecretName:      cfg.SecretName,
		AgentEnvVar:     agentEnvVar(cfg.Agent),
		AgentInstallCmd: agentInstallCmd(cfg.Agent),
		RoborevVersion:  cfg.RoborevVersion,
		ReviewCommands:  reviewCmds,
	}

	tmpl, err := template.New("workflow").Parse(workflowTemplate)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return buf.String(), nil
}

// WriteWorkflow generates the workflow and writes it to the given path.
// Creates parent directories as needed. Returns an error if the file
// already exists and force is false.
func WriteWorkflow(cfg WorkflowConfig, outputPath string, force bool) error {
	if !force {
		if _, err := os.Stat(outputPath); err == nil {
			return fmt.Errorf("workflow file already exists: %s (use --force to overwrite)", outputPath)
		}
	}

	content, err := Generate(cfg)
	if err != nil {
		return err
	}

	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write workflow: %w", err)
	}

	return nil
}

type templateData struct {
	Agent           string
	Model           string
	Reasoning       string
	SecretName      string
	AgentEnvVar     string
	AgentInstallCmd string
	RoborevVersion  string
	ReviewCommands  []string
}

// Pinned SHA for actions/checkout v4.2.2 — matches the pattern used in the
// project's own .github/workflows/ci.yml for supply-chain hardening.
var workflowTemplate = `# roborev CI Review
# Generated by: roborev init gh-action
# Runs AI-powered code reviews on pull requests.
#
# Required setup:
#   1. Add a repository secret named "{{ .SecretName }}" with your agent API key
#   2. The secret is passed as {{ .AgentEnvVar }} to the review agent
#
# Customize review behavior by editing the flags below or adding a
# .roborev.toml file to your repository root.

name: roborev

on:
  pull_request:
    types: [opened, synchronize, reopened]

permissions:
  contents: read
  pull-requests: write

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd  # v6.0.2
        with:
          fetch-depth: 0

      - name: Install roborev
        run: |
          set -euo pipefail
          {{- if .RoborevVersion }}
          ROBOREV_VERSION="{{ .RoborevVersion }}"
          {{- else }}
          ROBOREV_VERSION=$(curl -sfL https://api.github.com/repos/roborev-dev/roborev/releases/latest | grep '"tag_name"' | sed -E 's/.*"v?([^"]+)".*/\1/')
          {{- end }}
          ARCHIVE="roborev_${ROBOREV_VERSION}_linux_amd64.tar.gz"
          curl -sfLO "https://github.com/roborev-dev/roborev/releases/download/v${ROBOREV_VERSION}/${ARCHIVE}"
          curl -sfLO "https://github.com/roborev-dev/roborev/releases/download/v${ROBOREV_VERSION}/checksums.txt"
          sha256sum --check --ignore-missing checksums.txt
          tar xzf "${ARCHIVE}" -C /usr/local/bin roborev
          rm -f "${ARCHIVE}" checksums.txt
          roborev version

      - name: Install agent
        run: {{ .AgentInstallCmd }}

      - name: Run review
        env:
          {{ .AgentEnvVar }}: ${{"{{"}} secrets.{{ .SecretName }} {{"}}"}}
        run: |
          set -euo pipefail
          BASE_SHA=${{"{{"}} github.event.pull_request.base.sha {{"}}"}}
          HEAD_SHA=${{"{{"}} github.event.pull_request.head.sha {{"}}"}}

          for COMMIT in $(git rev-list --reverse "${BASE_SHA}..${HEAD_SHA}"); do
            echo "::group::Reviewing ${COMMIT}"
            {{- range .ReviewCommands }}
            {{ . }} || true
            {{- end }}
            echo "::endgroup::"
          done

      - name: Post results
        if: always()
        env:
          GH_TOKEN: ${{"{{"}} secrets.GITHUB_TOKEN {{"}}"}}
        run: |
          # Show review results in the CI log
          roborev list --json 2>/dev/null || echo "No review results found"
`
