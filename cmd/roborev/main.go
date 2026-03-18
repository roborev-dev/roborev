package main

import (
	"os"

	"github.com/spf13/cobra"
)

var (
	serverAddr string
	verbose    bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "roborev",
		Short: "Automatic code review for git commits",
		Long:  "roborev automatically reviews git commits using AI agents (Codex, Claude Code, Gemini, Copilot, OpenCode, Cursor, Kiro, Pi)",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return validateServerFlag()
		},
	}

	rootCmd.PersistentFlags().StringVar(&serverAddr, "server", "", "daemon server address (e.g. 127.0.0.1:7373 or unix://)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(reviewCmd())
	rootCmd.AddCommand(postCommitCmd())
	rootCmd.AddCommand(enqueueCmd()) // hidden alias for backward compatibility
	rootCmd.AddCommand(waitCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(listCmd())
	rootCmd.AddCommand(showCmd())
	rootCmd.AddCommand(commentCmd())
	rootCmd.AddCommand(respondCmd()) // hidden alias for backward compatibility
	rootCmd.AddCommand(closeCmd())
	rootCmd.AddCommand(installHookCmd())
	rootCmd.AddCommand(uninstallHookCmd())
	rootCmd.AddCommand(daemonCmd())
	rootCmd.AddCommand(streamCmd())
	rootCmd.AddCommand(tuiCmd())
	rootCmd.AddCommand(refineCmd())
	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(analyzeCmd())
	rootCmd.AddCommand(fixCmd())
	rootCmd.AddCommand(compactCmd())
	rootCmd.AddCommand(promptCmd()) // hidden alias for backward compatibility
	rootCmd.AddCommand(repoCmd())
	rootCmd.AddCommand(skillsCmd())
	rootCmd.AddCommand(syncCmd())
	rootCmd.AddCommand(remapCmd())
	rootCmd.AddCommand(checkAgentsCmd())
	rootCmd.AddCommand(ciCmd())
	rootCmd.AddCommand(logCmd())
	rootCmd.AddCommand(summaryCmd())
	rootCmd.AddCommand(backfillVerdictsCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(backfillTokensCmd())
	rootCmd.AddCommand(updateCmd())
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		// Check for exitError to exit with specific code without extra output
		if exitErr, ok := err.(*exitError); ok {
			os.Exit(exitErr.code)
		}
		os.Exit(1)
	}
}
