package ghaction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestGenerate(t *testing.T) {
	tests := []struct {
		name        string
		cfg         WorkflowConfig
		wantStrs    []string
		notWantStrs []string
		envChecks   func(t *testing.T, env map[string]string)
	}{
		{
			name: "default config",
			cfg:  DefaultConfig(),
			wantStrs: []string{
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
				"grep -F \"  ${ARCHIVE}\" checksums.txt > verify.txt",
				"set -euo pipefail",
				"@openai/codex@latest",
				"Pin agent CLI versions",
				`"$HOME/.local/bin"`,
				"$GITHUB_PATH",
				`"$HOME/.local/bin/roborev" version`,
				"api.github.com",
			},
			notWantStrs: []string{
				"--commit",
				"--format json",
				"comment --pr",
				"actions/checkout@v4",
				"--local",
				"--agent codex",
				"Post results",
				"/usr/local/bin",
				"--ignore-missing",
			},
			envChecks: func(t *testing.T, env map[string]string) {
				if _, ok := env["OPENAI_API_KEY"]; !ok {
					t.Error("expected OPENAI_API_KEY env var")
				}
			},
		},
		{
			name: "multi agent",
			cfg: WorkflowConfig{
				Agents: []string{"codex", "claude-code"},
			},
			wantStrs: []string{
				"@openai/codex@latest",
				"@anthropic-ai/claude-code@latest",
				"OPENAI_API_KEY",
				"ANTHROPIC_API_KEY",
			},
		},
		{
			name: "claude agent",
			cfg: WorkflowConfig{
				Agents: []string{"claude-code"},
			},
			wantStrs: []string{
				"ANTHROPIC_API_KEY",
				"@anthropic-ai/claude-code@latest",
			},
		},
		{
			name: "gemini agent",
			cfg: WorkflowConfig{
				Agents: []string{"gemini"},
			},
			wantStrs: []string{
				"GOOGLE_API_KEY",
				"@google/gemini-cli@latest",
			},
		},
		{
			name: "copilot agent",
			cfg: WorkflowConfig{
				Agents: []string{"copilot"},
			},
			wantStrs: []string{
				"@github/copilot@latest",
				"GH_TOKEN:",
			},
			envChecks: func(t *testing.T, env map[string]string) {
				if _, ok := env["GITHUB_TOKEN"]; ok {
					t.Error("env block should not contain bare GITHUB_TOKEN: entry for copilot")
				}
			},
		},
		{
			name: "pinned version",
			cfg: WorkflowConfig{
				Agents:         []string{"codex"},
				RoborevVersion: "0.33.1",
			},
			wantStrs: []string{
				`ROBOREV_VERSION="0.33.1"`,
			},
			notWantStrs: []string{
				"api.github.com",
			},
		},
		{
			name: "empty fields",
			cfg:  WorkflowConfig{},
			wantStrs: []string{
				"OPENAI_API_KEY",
				"@openai/codex@latest",
			},
		},
		{
			name: "dedupes env vars",
			cfg: WorkflowConfig{
				Agents: []string{"codex", "opencode"},
			},
			wantStrs: []string{
				"@openai/codex@latest",
				"opencode-ai/opencode@latest",
			},
			envChecks: func(t *testing.T, env map[string]string) {
				if _, ok := env["OPENAI_API_KEY"]; !ok {
					t.Error("expected OPENAI_API_KEY in env")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Generate(tt.cfg)
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}
			for _, want := range tt.wantStrs {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q", want)
				}
			}
			for _, notWant := range tt.notWantStrs {
				if strings.Contains(out, notWant) {
					t.Errorf("output should not contain %q", notWant)
				}
			}

			if tt.envChecks != nil || tt.name == "dedupes env vars" {
				var wf struct {
					Jobs map[string]struct {
						Steps []struct {
							Name string            `yaml:"name"`
							Env  map[string]string `yaml:"env"`
						} `yaml:"steps"`
					} `yaml:"jobs"`
				}
				if err := yaml.Unmarshal([]byte(out), &wf); err != nil {
					t.Fatalf("failed to parse yaml: %v", err)
				}

				var reviewEnv map[string]string
				for _, job := range wf.Jobs {
					for _, step := range job.Steps {
						if step.Name == "Run review" {
							reviewEnv = step.Env
						}
					}
				}
				if tt.envChecks != nil {
					tt.envChecks(t, reviewEnv)
				}

				if tt.name == "dedupes env vars" {
					lines := strings.Split(out, "\n")
					envDefCount := 0
					for _, line := range lines {
						if strings.HasPrefix(strings.TrimSpace(line), "OPENAI_API_KEY:") {
							envDefCount++
						}
					}
					if envDefCount != 1 {
						t.Errorf("expected 1 OPENAI_API_KEY: definition, got %d", envDefCount)
					}
				}
			}
		})
	}
}

func TestAgentInstallCmd(t *testing.T) {
	tests := []struct {
		agent       string
		wantPkg     string
		notWantPkgs []string
	}{
		{
			agent:   "codex",
			wantPkg: "npm install -g @openai/codex@latest",
		},
		{
			agent:   "claude-code",
			wantPkg: "npm install -g @anthropic-ai/claude-code@latest",
		},
		{
			agent:   "gemini",
			wantPkg: "npm install -g @google/gemini-cli@latest",
			notWantPkgs: []string{
				"@anthropic-ai/gemini",
			},
		},
		{
			agent:   "copilot",
			wantPkg: "npm install -g @github/copilot@latest",
			notWantPkgs: []string{
				"gh extension install",
			},
		},
		{
			agent:   "cursor",
			wantPkg: "not available in CI",
		},
		{
			agent:   "droid",
			wantPkg: "droid-cli",
		},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			cmd := AgentInstallCmd(tt.agent)
			if !strings.Contains(cmd, tt.wantPkg) {
				t.Errorf(
					"AgentInstallCmd(%q) = %q, want package %q",
					tt.agent, cmd, tt.wantPkg)
			}
			for _, bad := range tt.notWantPkgs {
				if strings.Contains(cmd, bad) {
					t.Errorf(
						"AgentInstallCmd(%q) = %q, should NOT contain %q",
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

func TestAgentSecrets(t *testing.T) {
	tests := []struct {
		name     string
		agents   []string
		wantLen  int
		wantVars []string
	}{
		{
			name:     "single agent",
			agents:   []string{"codex"},
			wantLen:  1,
			wantVars: []string{"OPENAI_API_KEY"},
		},
		{
			name:     "dedupes by env var",
			agents:   []string{"codex", "opencode"},
			wantLen:  1,
			wantVars: []string{"OPENAI_API_KEY"},
		},
		{
			name:     "multi env var",
			agents:   []string{"codex", "claude-code"},
			wantLen:  2,
			wantVars: []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"},
		},
		{
			name:     "copilot alone produces empty list",
			agents:   []string{"copilot"},
			wantLen:  0,
			wantVars: nil,
		},
		{
			name:     "copilot plus codex only codex secret",
			agents:   []string{"copilot", "codex"},
			wantLen:  1,
			wantVars: []string{"OPENAI_API_KEY"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secrets := AgentSecrets(tt.agents)
			if len(secrets) != tt.wantLen {
				t.Fatalf("expected %d secrets, got %d", tt.wantLen, len(secrets))
			}
			for i, wantVar := range tt.wantVars {
				if secrets[i].EnvVar != wantVar {
					t.Errorf("expected %q, got %q", wantVar, secrets[i].EnvVar)
				}
			}
		})
	}
}
