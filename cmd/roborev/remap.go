package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/spf13/cobra"
)

func remapCmd() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:   "remap",
		Short: "Remap review jobs after a rebase",
		Long: `Reads old-sha/new-sha pairs from stdin (one per line,
space-separated) and updates review jobs to point at the
new commits. Intended to be called from a post-rewrite hook.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			gitCwd, err := git.GetRepoRoot(".")
			if err != nil {
				return fmt.Errorf("not a git repository: %w", err)
			}
			repoRoot, err := git.GetMainRepoRoot(".")
			if err != nil {
				repoRoot = gitCwd
			}

			// Parse stdin: "old_sha new_sha" per line
			var mappings []daemon.RemapMapping
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				oldSHA, newSHA := fields[0], fields[1]

				oldPatchID := git.GetPatchID(gitCwd, oldSHA)
				newPatchID := git.GetPatchID(gitCwd, newSHA)

				// Skip if either has no patch-id or they differ
				if oldPatchID == "" || newPatchID == "" {
					continue
				}
				if oldPatchID != newPatchID {
					continue
				}

				info, err := git.GetCommitInfo(gitCwd, newSHA)
				if err != nil {
					continue
				}

				mappings = append(mappings, daemon.RemapMapping{
					OldSHA:    oldSHA,
					NewSHA:    newSHA,
					PatchID:   newPatchID,
					Author:    info.Author,
					Subject:   info.Subject,
					Timestamp: info.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
				})
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}

			if len(mappings) == 0 {
				if !quiet {
					fmt.Println("No mappings to remap")
				}
				return nil
			}

			addr := getDaemonAddr()
			client := daemon.NewHTTPClient(addr)

			result, err := client.Remap(daemon.RemapRequest{
				RepoPath: repoRoot,
				Mappings: mappings,
			})
			if err != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "remap failed: %v\n", err)
				}
				return nil // Don't fail the hook
			}

			if !quiet {
				fmt.Printf("Remapped %d review(s), skipped %d\n",
					result.Remapped, result.Skipped)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress output")

	return cmd
}
