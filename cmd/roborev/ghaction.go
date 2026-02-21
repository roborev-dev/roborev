package main

import (
	"fmt"
	"path/filepath"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/ghaction"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/spf13/cobra"
)

func ghActionCmd() *cobra.Command {
	var (
		agentFlag      string
		outputPath     string
		force          bool
		roborevVersion string
	)

	cmd := &cobra.Command{
		Use:   "gh-action",
		Short: "Generate a GitHub Actions workflow for roborev CI reviews",
		Long: `Generate a GitHub Actions workflow file that runs ` +
			`roborev reviews on pull requests.

The workflow installs roborev and the configured agents, ` +
			`then runs 'roborev ci review' which executes the ` +
			`full review_type x agent matrix, synthesizes ` +
			`results, and posts a PR comment.

Review types, reasoning level, severity filter, and other ` +
			`review parameters are configured in .roborev.toml ` +
			`under [ci] and resolved at runtime.

After generating the workflow, add repository secrets ` +
			`for your agent API keys.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := git.GetRepoRoot(".")
			if err != nil {
				return fmt.Errorf(
					"not a git repository - " +
						"run this from inside a git repo")
			}

			cfg := resolveWorkflowConfig(
				root, agentFlag, roborevVersion)

			if outputPath == "" {
				outputPath = filepath.Join(
					root, ".github", "workflows",
					"roborev.yml")
			}

			if err := ghaction.WriteWorkflow(
				cfg, outputPath, force); err != nil {
				return err
			}

			fmt.Printf(
				"Created workflow at %s\n", outputPath)
			fmt.Println()
			fmt.Printf("Next steps:\n")

			// List required secrets per agent
			infos := ghaction.AgentSecrets(cfg.Agents)
			for i, info := range infos {
				fmt.Printf(
					"  %d. Add a repository secret "+
						"named %q (%s API key)\n",
					i+1, info.SecretName, info.Name)
				fmt.Printf(
					"     gh secret set %s\n",
					info.SecretName)
			}
			fmt.Printf(
				"  %d. Commit and push the workflow file\n",
				len(infos)+1)
			fmt.Printf(
				"  %d. Open a pull request to trigger "+
					"the first review\n",
				len(infos)+2)

			return nil
		},
	}

	cmd.Flags().StringVar(&agentFlag, "agent", "",
		"agents to use, comma-separated "+
			"(codex, claude-code, gemini, copilot, "+
			"opencode, cursor, droid)")
	cmd.Flags().StringVar(&outputPath, "output", "",
		"output path for workflow file "+
			"(default: .github/workflows/roborev.yml)")
	cmd.Flags().BoolVar(&force, "force", false,
		"overwrite existing workflow file")
	cmd.Flags().StringVar(&roborevVersion, "roborev-version", "",
		"roborev version to install (default: latest)")

	return cmd
}

// resolveWorkflowConfig builds a WorkflowConfig by merging
// CLI flags with existing roborev configuration.
func resolveWorkflowConfig(
	repoRoot, agentFlag, roborevVersion string,
) ghaction.WorkflowConfig {
	cfg := ghaction.DefaultConfig()

	globalCfg, _ := config.LoadGlobal()
	repoCfg, _ := config.LoadRepoConfig(repoRoot)

	// Resolve agents: flag > repo [ci] agents > repo agent
	// > global [ci] agents > global default_agent > default
	if agentFlag != "" {
		cfg.Agents = splitTrimmed(agentFlag)
	} else if repoCfg != nil &&
		len(repoCfg.CI.Agents) > 0 {
		cfg.Agents = repoCfg.CI.Agents
	} else if repoCfg != nil && repoCfg.Agent != "" {
		cfg.Agents = []string{repoCfg.Agent}
	} else if globalCfg != nil &&
		len(globalCfg.CI.Agents) > 0 {
		cfg.Agents = globalCfg.CI.Agents
	} else if globalCfg != nil &&
		globalCfg.DefaultAgent != "" {
		cfg.Agents = []string{globalCfg.DefaultAgent}
	}

	if roborevVersion != "" {
		cfg.RoborevVersion = roborevVersion
	}

	return cfg
}
