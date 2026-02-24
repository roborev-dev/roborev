package main

import (
	"fmt"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/spf13/cobra"
)

// registerAgentCompletion registers shell completion for the --agent flag.
// Panics if the flag doesn't exist on the command (programming error).
func registerAgentCompletion(cmd *cobra.Command) {
	if err := cmd.RegisterFlagCompletionFunc("agent", func(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return agent.Available(), cobra.ShellCompDirectiveNoFileComp
	}); err != nil {
		panic(fmt.Sprintf("registering agent completion for %s: %v", cmd.Name(), err))
	}
}

// registerReasoningCompletion registers shell completion for the --reasoning flag.
// Panics if the flag doesn't exist on the command (programming error).
func registerReasoningCompletion(cmd *cobra.Command) {
	if err := cmd.RegisterFlagCompletionFunc("reasoning", func(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return agent.ReasoningLevels(), cobra.ShellCompDirectiveNoFileComp
	}); err != nil {
		panic(fmt.Sprintf("registering reasoning completion for %s: %v", cmd.Name(), err))
	}
}
