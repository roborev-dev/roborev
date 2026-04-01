package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/tokens"
	"github.com/spf13/cobra"
)

func showCmd() *cobra.Command {
	var forceJobID bool
	var showPrompt bool
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "show [job_id|sha]",
		Short: "Show review for a commit or job",
		Long: `Show review output for a commit or job.

The argument can be either a job ID (numeric) or a commit SHA.
Job IDs are displayed in review notifications and the TUI.

In a git repo, the argument is first tried as a git ref. If that fails
and it's numeric, it's treated as a job ID. Use --job to force job ID.

Examples:
  roborev show              # Show review for HEAD
  roborev show abc123       # Show review for commit
  roborev show 42           # Job ID (if "42" is not a valid git ref)
  roborev show --job 42     # Force as job ID even if "42" is a valid ref
  roborev show --prompt 42  # Show the prompt sent to the agent`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ensure daemon is running (and restart if version mismatch)
			if err := ensureDaemon(); err != nil {
				return fmt.Errorf("daemon not running: %w", err)
			}

			ep := getDaemonEndpoint()
			addr := ep.BaseURL()
			client := ep.HTTPClient(5 * time.Second)

			var queryURL string
			var displayRef string

			if len(args) == 0 {
				if forceJobID {
					return fmt.Errorf("--job requires a job ID argument")
				}
				// Default to HEAD
				sha := "HEAD"
				root, rootErr := git.GetRepoRoot(".")
				if rootErr != nil {
					return fmt.Errorf("not in a git repository; use a job ID instead (e.g., roborev show 42)")
				}
				if resolved, err := git.ResolveSHA(root, sha); err == nil {
					sha = resolved
				}
				queryURL = addr + "/api/review?sha=" + sha
				displayRef = git.ShortSHA(sha)
			} else {
				arg := args[0]
				var isJobID bool
				var resolvedSHA string

				if forceJobID {
					isJobID = true
				} else {
					// Try to resolve as SHA first (handles numeric SHAs like "123456")
					if root, err := git.GetRepoRoot("."); err == nil {
						if resolved, err := git.ResolveSHA(root, arg); err == nil {
							resolvedSHA = resolved
						}
					}
					// If not resolvable as SHA and is numeric, treat as job ID
					if resolvedSHA == "" {
						if _, err := strconv.ParseInt(arg, 10, 64); err == nil {
							isJobID = true
						}
					}
				}

				if isJobID {
					queryURL = addr + "/api/review?job_id=" + arg
					displayRef = "job " + arg
				} else {
					sha := arg
					if resolvedSHA != "" {
						sha = resolvedSHA
					}
					queryURL = addr + "/api/review?sha=" + sha
					displayRef = git.ShortSHA(sha)
				}
			}

			resp, err := client.Get(queryURL)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon (is it running?)")
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("no review found for %s", displayRef)
			}

			var review storage.Review
			if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			if jsonOutput {
				// Include comments in JSON output so tools/skills can see them.
				// Merge job-based and legacy SHA-based comments (mirroring TUI).
				type reviewWithComments struct {
					storage.Review
					Comments []storage.Response `json:"comments,omitempty"`
				}
				out := reviewWithComments{Review: review}
				out.Comments = fetchShowComments(client, addr, review)
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(&out)
			}

			// Avoid redundant "job X (job X, ...)" output
			if strings.HasPrefix(displayRef, "job ") {
				fmt.Printf("Review for %s (by %s)\n", displayRef, review.Agent)
			} else {
				fmt.Printf("Review for %s (job %d, by %s)\n", displayRef, review.JobID, review.Agent)
			}
			if review.Job != nil {
				if tu := tokens.ParseJSON(review.Job.TokenUsage); tu != nil {
					fmt.Printf("Tokens: %s\n", tu.FormatSummary())
				}
			}
			fmt.Println(strings.Repeat("-", 60))
			if showPrompt {
				fmt.Println(review.Prompt)
			} else {
				fmt.Println(review.Output)
			}

			// Fetch and display comments
			commentsURL := addr + fmt.Sprintf(
				"/api/comments?job_id=%d", review.JobID,
			)
			commentsResp, cErr := client.Get(commentsURL)
			if cErr == nil {
				defer commentsResp.Body.Close()
				if commentsResp.StatusCode == http.StatusOK {
					var result struct {
						Responses []storage.Response `json:"responses"`
					}
					if json.NewDecoder(commentsResp.Body).Decode(&result) == nil &&
						len(result.Responses) > 0 {
						fmt.Println()
						fmt.Println("--- Comments ---")
						for _, r := range result.Responses {
							ts := r.CreatedAt.Format("Jan 02 15:04")
							fmt.Printf("\n[%s] %s:\n", ts, r.Responder)
							fmt.Println(r.Response)
						}
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&forceJobID, "job", false, "force argument to be treated as job ID")
	cmd.Flags().BoolVar(&showPrompt, "prompt", false, "show the prompt sent to the agent instead of the review output")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

// fetchShowComments retrieves comments for a review, merging legacy
// SHA-based comments for single-commit reviews (mirroring TUI/CLI fix).
//
// NOTE: The merge/dedup-by-ID/sort pattern is duplicated in:
//   - internal/storage/reviews.go  GetAllCommentsForJob() (DB path)
//   - cmd/roborev/fix.go           fetchComments()
//   - cmd/roborev/tui/fetch.go     loadResponses()
// Keep all four in sync when changing the merge logic.
func fetchShowComments(client *http.Client, addr string, review storage.Review) []storage.Response {
	var responses []storage.Response

	// Fetch by job ID
	commentsURL := addr + fmt.Sprintf("/api/comments?job_id=%d", review.JobID)
	if resp, err := client.Get(commentsURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not fetch comments for job %d: %v\n", review.JobID, err)
	} else if resp != nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var result struct {
				Responses []storage.Response `json:"responses"`
			}
			if json.NewDecoder(resp.Body).Decode(&result) == nil {
				responses = result.Responses
			}
		}
	}

	// Also fetch legacy SHA-based comments for single commits
	if review.Job != nil && !strings.Contains(review.Job.GitRef, "..") && review.Job.GitRef != "dirty" {
		shaURL := addr + fmt.Sprintf("/api/comments?sha=%s", review.Job.GitRef)
		if resp, err := client.Get(shaURL); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var result struct {
					Responses []storage.Response `json:"responses"`
				}
				if json.NewDecoder(resp.Body).Decode(&result) == nil {
					seen := make(map[int64]bool, len(responses))
					for _, r := range responses {
						seen[r.ID] = true
					}
					for _, r := range result.Responses {
						if !seen[r.ID] {
							seen[r.ID] = true
							responses = append(responses, r)
						}
					}
					sort.Slice(responses, func(i, j int) bool {
						return responses[i].CreatedAt.Before(responses[j].CreatedAt)
					})
				}
			}
		}
	}

	return responses
}
