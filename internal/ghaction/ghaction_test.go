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

func TestGenerate_DefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Verify key elements are present
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
	}
	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("output missing %q", check)
		}
	}
}

func TestGenerate_ClaudeAgent(t *testing.T) {
	cfg := WorkflowConfig{
		Agent:       "claude-code",
		ReviewTypes: []string{"security", "default"},
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
	if !strings.Contains(out, "npm install -g @anthropic-ai/claude-code") {
		t.Error("expected claude-code install command")
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
	// Generate with zero-value config should fill defaults
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

func TestGenerate_MultipleReviewTypes(t *testing.T) {
	cfg := WorkflowConfig{
		Agent:       "codex",
		ReviewTypes: []string{"security", "default", "design"},
		Reasoning:   "thorough",
		SecretName:  "ROBOREV_API_KEY",
	}
	out, err := Generate(cfg)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(out, "--type security,default,design") {
		t.Error("expected comma-joined review types")
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

	// Verify original content preserved
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
		{"claude", "ANTHROPIC_API_KEY"},
		{"gemini", "GOOGLE_API_KEY"},
		{"copilot", "GITHUB_TOKEN"},
		{"opencode", "OPENAI_API_KEY"},
		{"droid", "OPENAI_API_KEY"},
		{"unknown", "OPENAI_API_KEY"},
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
		{"codex", "npm install -g @openai/codex"},
		{"claude-code", "npm install -g @anthropic-ai/claude-code"},
		{"copilot", "gh extension install"},
		{"cursor", "not available in CI"},
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
