package main

import (
	"os"

	"github.com/spf13/cobra"
)

var (
	verbose bool
)

type serverAddrKey struct{}
type workDirKey struct{}

func getWorkDir(cmd *cobra.Command) (string, error) {
	if ctx := cmd.Context(); ctx != nil {
		if dir, ok := ctx.Value(workDirKey{}).(string); ok {
			return dir, nil
		}
	}
	return os.Getwd()
}

func getExplicitServerAddr(cmd *cobra.Command) (string, bool) {
	if ctx := cmd.Context(); ctx != nil {
		if addr, ok := ctx.Value(serverAddrKey{}).(string); ok {
			return addr, true
		}
	}
	if f := cmd.Flag("server"); f != nil {
		val := f.Value.String()
		if val != "http://127.0.0.1:7373" {
			return val, true
		}
	}
	if testAddr := os.Getenv("ROBOREV_TEST_SERVER_ADDR"); testAddr != "" {
		return testAddr, true
	}
	return "", false
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "roborev",
		Short: "Automatic code review for git commits",
		Long:  "roborev automatically reviews git commits using AI agents (Codex, Claude Code, Gemini, Copilot, OpenCode, Cursor, Kiro, Pi)",
	}

	rootCmd.PersistentFlags().String("server", "http://127.0.0.1:7373", "daemon server address")
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
	rootCmd.AddCommand(configCmd())
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
