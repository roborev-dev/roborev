package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/ghaction"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/spf13/cobra"
)

func ghActionCmd() *cobra.Command {
	var (
		agentFlag      string
		modelFlag      string
		reviewTypes    string
		reasoning      string
		secretName     string
		outputPath     string
		force          bool
		roborevVersion string
	)

	cmd := &cobra.Command{
		Use:   "gh-action",
		Short: "Generate a GitHub Actions workflow for roborev CI reviews",
		Long: `Generate a GitHub Actions workflow file that runs roborev reviews on pull requests.

The workflow installs roborev and the configured agent, then reviews each commit
in the PR. Results can be posted as PR comments.

Configuration is inferred from existing roborev config files (.roborev.toml and
~/.roborev/config.toml) but can be overridden with flags.

After generating the workflow, add a repository secret with your agent API key.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := git.GetRepoRoot(".")
			if err != nil {
				return fmt.Errorf("not a git repository - run this from inside a git repo")
			}

			cfg := resolveWorkflowConfig(root, agentFlag, modelFlag, reviewTypes, reasoning, secretName, roborevVersion)

			if outputPath == "" {
				outputPath = filepath.Join(root, ".github", "workflows", "roborev.yml")
			}

			if err := ghaction.WriteWorkflow(cfg, outputPath, force); err != nil {
				return err
			}

			fmt.Printf("Created workflow at %s\n", outputPath)
			fmt.Println()
			fmt.Printf("Next steps:\n")
			fmt.Printf("  1. Add a repository secret named %q with your %s API key\n", cfg.SecretName, cfg.Agent)
			fmt.Printf("     gh secret set %s\n", cfg.SecretName)
			fmt.Printf("  2. Commit and push the workflow file\n")
			fmt.Printf("  3. Open a pull request to trigger the first review\n")

			return nil
		},
	}

	cmd.Flags().StringVar(&agentFlag, "agent", "", "agent to use (codex, claude-code, gemini, copilot, opencode, cursor, droid)")
	cmd.Flags().StringVar(&modelFlag, "model", "", "model override for the agent")
	cmd.Flags().StringVar(&reviewTypes, "review-types", "", "review types to run (comma-separated: security,default,design)")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level (thorough, standard, fast)")
	cmd.Flags().StringVar(&secretName, "secret-name", "", "GitHub Actions secret name for the agent API key")
	cmd.Flags().StringVar(&outputPath, "output", "", "output path for workflow file (default: .github/workflows/roborev.yml)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing workflow file")
	cmd.Flags().StringVar(&roborevVersion, "roborev-version", "", "roborev version to install (default: latest)")

	return cmd
}

// resolveWorkflowConfig builds a WorkflowConfig by merging CLI flags with
// existing roborev configuration (repo config and global config).
func resolveWorkflowConfig(repoRoot, agentFlag, modelFlag, reviewTypes, reasoning, secretName, roborevVersion string) ghaction.WorkflowConfig {
	cfg := ghaction.DefaultConfig()

	// Load existing configs for inference
	globalCfg, _ := config.LoadGlobal()
	repoCfg, _ := config.LoadRepoConfig(repoRoot)

	// Resolve agent: flag > repo config > global config > default
	if agentFlag != "" {
		cfg.Agent = agentFlag
	} else if repoCfg != nil && repoCfg.Agent != "" {
		cfg.Agent = repoCfg.Agent
	} else if globalCfg != nil && globalCfg.DefaultAgent != "" {
		cfg.Agent = globalCfg.DefaultAgent
	}

	// Resolve model: flag > repo config > global config
	if modelFlag != "" {
		cfg.Model = modelFlag
	} else if repoCfg != nil && repoCfg.Model != "" {
		cfg.Model = repoCfg.Model
	} else if globalCfg != nil && globalCfg.DefaultModel != "" {
		cfg.Model = globalCfg.DefaultModel
	}

	// Resolve review types: flag > repo CI config > global CI config > default
	if reviewTypes != "" {
		cfg.ReviewTypes = strings.Split(reviewTypes, ",")
		for i := range cfg.ReviewTypes {
			cfg.ReviewTypes[i] = strings.TrimSpace(cfg.ReviewTypes[i])
		}
	} else if repoCfg != nil && len(repoCfg.CI.ReviewTypes) > 0 {
		cfg.ReviewTypes = repoCfg.CI.ReviewTypes
	} else if globalCfg != nil && len(globalCfg.CI.ReviewTypes) > 0 {
		cfg.ReviewTypes = globalCfg.CI.ReviewTypes
	}

	// Resolve reasoning: flag > repo CI config > default
	if reasoning != "" {
		cfg.Reasoning = reasoning
	} else if repoCfg != nil && repoCfg.CI.Reasoning != "" {
		cfg.Reasoning = repoCfg.CI.Reasoning
	}

	// Secret name: flag > default
	if secretName != "" {
		cfg.SecretName = secretName
	}

	if roborevVersion != "" {
		cfg.RoborevVersion = roborevVersion
	}

	return cfg
}
