package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/githook"
	"github.com/spf13/cobra"
)

func initCmd() *cobra.Command {
	var agent string
	var noDaemon bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize roborev in current repository",
		Long: `Initialize roborev with a single command:
  - Creates ~/.roborev/ global config directory
  - Creates .roborev.toml in repo (if --agent specified)
  - Installs post-commit hook
  - Starts the daemon (unless --no-daemon)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Initializing roborev...")

			// 1. Ensure we're in a git repo
			root, err := git.GetRepoRoot(".")
			if err != nil {
				return fmt.Errorf("not a git repository - run this from inside a git repo")
			}

			// 2. Create config directory and default config
			configDir := config.DataDir()
			if err := os.MkdirAll(configDir, 0755); err != nil {
				return fmt.Errorf("create config dir: %w", err)
			}

			configPath := config.GlobalConfigPath()
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				cfg := config.DefaultConfig()
				if agent != "" {
					cfg.DefaultAgent = agent
				}
				if err := config.SaveGlobal(cfg); err != nil {
					return fmt.Errorf("save config: %w", err)
				}
				fmt.Printf("  Created config at %s\n", configPath)
			} else {
				fmt.Printf("  Config already exists at %s\n", configPath)
			}

			// 3. Create per-repo config if agent specified
			repoConfigPath := filepath.Join(root, ".roborev.toml")
			if agent != "" {
				if _, err := os.Stat(repoConfigPath); os.IsNotExist(err) {
					repoConfig := fmt.Sprintf("# roborev per-repo configuration\nagent = %q\n", agent)
					if err := os.WriteFile(repoConfigPath, []byte(repoConfig), 0644); err != nil {
						return fmt.Errorf("create repo config: %w", err)
					}
					fmt.Printf("  Created %s\n", repoConfigPath)
				}
			}

			// 4. Install hooks (post-commit + post-rewrite)
			hooksDir, err := git.GetHooksPath(root)
			if err != nil {
				return fmt.Errorf("get hooks path: %w", err)
			}
			if err := os.MkdirAll(hooksDir, 0755); err != nil {
				return fmt.Errorf("create hooks directory: %w", err)
			}
			if err := githook.InstallAll(hooksDir, false); err != nil {
				if githook.HasRealErrors(err) {
					return fmt.Errorf("install hooks: %w", err)
				}
				fmt.Printf("  Warning: %v\n", err)
			}

			// 5. Start daemon (or just register if --no-daemon)
			var initIncomplete bool
			if noDaemon {
				// Try to register with an already-running daemon, but don't start one
				if err := registerRepo(root); err != nil {
					initIncomplete = true
					if isTransportError(err) {
						fmt.Println("  Daemon not running (use 'roborev daemon start' or systemctl)")
					} else {
						fmt.Printf("  Warning: failed to register repo: %v\n", err)
					}
				} else {
					fmt.Println("  Repo registered with running daemon")
				}
			} else if err := ensureDaemon(); err != nil {
				initIncomplete = true
				fmt.Printf("  Warning: %v\n", err)
				fmt.Println("  Run 'roborev daemon start' to start manually")
			} else {
				fmt.Println("  Daemon is running")
				if err := registerRepo(root); err != nil {
					initIncomplete = true
					fmt.Printf("  Warning: failed to register repo: %v\n", err)
				} else {
					fmt.Println("  Repo registered")
				}
			}

			// 5. Success message
			fmt.Println()
			if initIncomplete {
				fmt.Println("Setup incomplete: repo was not registered with the daemon.")
				fmt.Println("Start the daemon and run 'roborev init' again, or register manually.")
			} else {
				fmt.Println("Ready! Every commit will now be automatically reviewed.")
			}
			fmt.Println()
			fmt.Println("Commands:")
			fmt.Println("  roborev status      - view queue and daemon status")
			fmt.Println("  roborev show HEAD   - view review for a commit")
			fmt.Println("  roborev tui         - interactive terminal UI")

			return nil
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "default agent (codex, claude-code, gemini, copilot, opencode, cursor, kilo)")
	cmd.Flags().BoolVar(&noDaemon, "no-daemon", false, "skip auto-starting daemon (useful with systemd/launchd)")
	registerAgentCompletion(cmd)

	cmd.AddCommand(ghActionCmd())

	return cmd
}
