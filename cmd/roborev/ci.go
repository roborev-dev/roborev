package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/review"
	"github.com/spf13/cobra"
)

func ciCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "CI-specific commands for running roborev in CI pipelines",
	}

	cmd.AddCommand(ciReviewCmd())
	return cmd
}

func ciReviewCmd() *cobra.Command {
	var (
		refFlag        string
		commentFlag    bool
		ghRepoFlag     string
		prFlag         int
		agentFlag      string
		reviewTypes    string
		reasoning      string
		minSeverity    string
		synthesisAgent string
	)

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run a full review matrix and optionally post a PR comment",
		Long: `Run the full review_type x agent matrix in parallel, ` +
			`synthesize results, and output or post a PR comment.

This command is designed for CI pipelines. It reads ` +
			`configuration from .roborev.toml and runs without ` +
			`a daemon or database.

Flags override config values. When run inside GitHub ` +
			`Actions, --ref, --gh-repo, and --pr are ` +
			`auto-detected from the environment.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCIReview(
				cmd.Context(), ciReviewOpts{
					ref:            refFlag,
					comment:        commentFlag,
					ghRepo:         ghRepoFlag,
					pr:             prFlag,
					agents:         agentFlag,
					reviewTypes:    reviewTypes,
					reasoning:      reasoning,
					minSeverity:    minSeverity,
					synthesisAgent: synthesisAgent,
				})
		},
	}

	cmd.Flags().StringVar(&refFlag, "ref", "",
		"git ref range (e.g., BASE..HEAD); "+
			"auto-detected in GitHub Actions")
	cmd.Flags().BoolVar(&commentFlag, "comment", false,
		"post result as PR comment via gh pr comment")
	cmd.Flags().StringVar(&ghRepoFlag, "gh-repo", "",
		"GitHub repo owner/name (auto from GITHUB_REPOSITORY)")
	cmd.Flags().IntVar(&prFlag, "pr", 0,
		"PR number (auto from event JSON / GITHUB_REF)")
	cmd.Flags().StringVar(&agentFlag, "agent", "",
		"comma-separated agent list (overrides config)")
	cmd.Flags().StringVar(&reviewTypes, "review-types", "",
		"comma-separated review types (overrides config)")
	cmd.Flags().StringVar(&reasoning, "reasoning", "",
		"reasoning level: thorough, standard, fast")
	cmd.Flags().StringVar(&minSeverity, "min-severity", "",
		"minimum severity filter: critical, high, medium, low")
	cmd.Flags().StringVar(&synthesisAgent, "synthesis-agent", "",
		"agent for synthesis (overrides config)")

	return cmd
}

type ciReviewOpts struct {
	ref            string
	comment        bool
	ghRepo         string
	pr             int
	agents         string
	reviewTypes    string
	reasoning      string
	minSeverity    string
	synthesisAgent string
}

func runCIReview(ctx context.Context, opts ciReviewOpts) error {
	// Determine repo root
	root, err := git.GetRepoRoot(".")
	if err != nil {
		return fmt.Errorf(
			"not a git repository — " +
				"run this from inside a git repo")
	}

	// Load configs
	globalCfg, _ := config.LoadGlobal()
	repoCfg, _ := config.LoadRepoConfig(root)

	// Resolve git ref
	gitRef := opts.ref
	if gitRef == "" {
		detected, err := detectGitRef()
		if err != nil {
			return fmt.Errorf(
				"--ref not provided and auto-detection "+
					"failed: %w", err)
		}
		gitRef = detected
	}

	// Resolve agents
	agents := resolveAgentList(
		opts.agents, repoCfg, globalCfg)

	// Resolve review types
	reviewTypes := resolveReviewTypes(
		opts.reviewTypes, repoCfg, globalCfg)

	// Resolve reasoning
	reasoningLevel := resolveCIReasoning(
		opts.reasoning, repoCfg, globalCfg)

	// Resolve min severity
	minSev := resolveCIMinSeverity(
		opts.minSeverity, repoCfg, globalCfg)

	// Resolve synthesis agent
	synthAgent := resolveCISynthesisAgent(
		opts.synthesisAgent, repoCfg, globalCfg)

	log.Printf(
		"ci review: ref=%s agents=%v types=%v "+
			"reasoning=%s min_severity=%s",
		gitRef, agents, reviewTypes,
		reasoningLevel, minSev)

	// Run batch
	batchCfg := review.BatchConfig{
		RepoPath:    root,
		GitRef:      gitRef,
		Agents:      agents,
		ReviewTypes: reviewTypes,
		Reasoning:   reasoningLevel,
	}

	results := review.RunBatch(ctx, batchCfg)

	// Determine HEAD SHA for comment formatting
	headSHA := extractHeadSHA(gitRef)

	// Synthesize
	comment, err := review.Synthesize(
		ctx, results, review.SynthesizeOpts{
			Agent:       synthAgent,
			MinSeverity: minSev,
			RepoPath:    root,
			HeadSHA:     headSHA,
		})
	if err != nil {
		return fmt.Errorf("synthesize: %w", err)
	}

	// Output to stdout
	fmt.Println(comment)

	// Post as PR comment if requested
	if opts.comment {
		ghRepo := opts.ghRepo
		if ghRepo == "" {
			ghRepo = os.Getenv("GITHUB_REPOSITORY")
		}
		if ghRepo == "" {
			return fmt.Errorf(
				"--comment requires --gh-repo or " +
					"GITHUB_REPOSITORY env var")
		}

		prNumber := opts.pr
		if prNumber == 0 {
			detected, err := detectPRNumber()
			if err != nil {
				return fmt.Errorf(
					"--comment requires --pr or "+
						"auto-detection from "+
						"GITHUB_EVENT_PATH/GITHUB_REF: %w",
					err)
			}
			prNumber = detected
		}

		if err := postCIComment(
			ghRepo, prNumber, comment,
		); err != nil {
			return fmt.Errorf(
				"post PR comment: %w", err)
		}
		log.Printf(
			"ci review: posted comment on %s#%d",
			ghRepo, prNumber)
	}

	return nil
}

func resolveAgentList(
	flag string,
	repoCfg *config.RepoConfig,
	globalCfg *config.Config,
) []string {
	if flag != "" {
		return splitTrimmed(flag)
	}
	if repoCfg != nil && len(repoCfg.CI.Agents) > 0 {
		return repoCfg.CI.Agents
	}
	if globalCfg != nil && len(globalCfg.CI.Agents) > 0 {
		return globalCfg.CI.Agents
	}
	// Default: empty string = auto-detect
	return []string{""}
}

func resolveReviewTypes(
	flag string,
	repoCfg *config.RepoConfig,
	globalCfg *config.Config,
) []string {
	if flag != "" {
		return splitTrimmed(flag)
	}
	if repoCfg != nil && len(repoCfg.CI.ReviewTypes) > 0 {
		return repoCfg.CI.ReviewTypes
	}
	if globalCfg != nil && len(globalCfg.CI.ReviewTypes) > 0 {
		return globalCfg.CI.ReviewTypes
	}
	return []string{"security"}
}

func resolveCIReasoning(
	flag string,
	repoCfg *config.RepoConfig,
	globalCfg *config.Config,
) string {
	if flag != "" {
		if n, err := config.NormalizeReasoning(flag); err == nil {
			return n
		}
	}
	if repoCfg != nil && repoCfg.CI.Reasoning != "" {
		if n, err := config.NormalizeReasoning(
			repoCfg.CI.Reasoning); err == nil {
			return n
		}
	}
	// Global CI config doesn't have a Reasoning field,
	// but we check for it in the CI config section.
	return "thorough"
}

func resolveCIMinSeverity(
	flag string,
	repoCfg *config.RepoConfig,
	globalCfg *config.Config,
) string {
	if flag != "" {
		if n, err := config.NormalizeMinSeverity(flag); err == nil {
			return n
		}
	}
	if repoCfg != nil && repoCfg.CI.MinSeverity != "" {
		if n, err := config.NormalizeMinSeverity(
			repoCfg.CI.MinSeverity); err == nil {
			return n
		}
	}
	if globalCfg != nil && globalCfg.CI.MinSeverity != "" {
		if n, err := config.NormalizeMinSeverity(
			globalCfg.CI.MinSeverity); err == nil {
			return n
		}
	}
	return ""
}

func resolveCISynthesisAgent(
	flag string,
	repoCfg *config.RepoConfig,
	globalCfg *config.Config,
) string {
	if flag != "" {
		return flag
	}
	if globalCfg != nil && globalCfg.CI.SynthesisAgent != "" {
		return globalCfg.CI.SynthesisAgent
	}
	return ""
}

func splitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// detectGitRef attempts to auto-detect the git ref range from
// the GitHub Actions environment.
func detectGitRef() (string, error) {
	eventPath := os.Getenv("GITHUB_EVENT_PATH")
	if eventPath == "" {
		return "", fmt.Errorf("GITHUB_EVENT_PATH not set")
	}

	data, err := os.ReadFile(eventPath)
	if err != nil {
		return "", fmt.Errorf("read event file: %w", err)
	}

	var event struct {
		PullRequest struct {
			Base struct {
				SHA string `json:"sha"`
			} `json:"base"`
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return "", fmt.Errorf("parse event JSON: %w", err)
	}

	base := event.PullRequest.Base.SHA
	head := event.PullRequest.Head.SHA
	if base == "" || head == "" {
		return "", fmt.Errorf(
			"event JSON missing " +
				"pull_request.base.sha or " +
				"pull_request.head.sha")
	}

	return base + ".." + head, nil
}

// detectPRNumber attempts to auto-detect the PR number from
// the GitHub Actions environment.
func detectPRNumber() (int, error) {
	// Try event JSON first
	eventPath := os.Getenv("GITHUB_EVENT_PATH")
	if eventPath != "" {
		data, err := os.ReadFile(eventPath)
		if err == nil {
			var event struct {
				PullRequest struct {
					Number int `json:"number"`
				} `json:"pull_request"`
			}
			if err := json.Unmarshal(
				data, &event); err == nil {
				if event.PullRequest.Number > 0 {
					return event.PullRequest.Number, nil
				}
			}
		}
	}

	// Try GITHUB_REF (refs/pull/N/merge)
	ghRef := os.Getenv("GITHUB_REF")
	if strings.HasPrefix(ghRef, "refs/pull/") {
		parts := strings.Split(ghRef, "/")
		if len(parts) >= 3 {
			n, err := strconv.Atoi(parts[2])
			if err == nil && n > 0 {
				return n, nil
			}
		}
	}

	return 0, fmt.Errorf(
		"could not detect PR number from environment")
}

// extractHeadSHA extracts the HEAD SHA from a git ref range.
// For "BASE..HEAD" returns HEAD; for a single ref returns it.
func extractHeadSHA(gitRef string) string {
	if _, after, ok := strings.Cut(gitRef, ".."); ok {
		return after
	}
	return gitRef
}

// postCIComment posts a comment on a GitHub PR using gh CLI.
// Truncates the body to stay within GitHub's comment limit.
func postCIComment(
	ghRepo string,
	prNumber int,
	body string,
) error {
	const maxCommentLen = 60000
	if len(body) > maxCommentLen {
		body = body[:maxCommentLen] +
			"\n\n...(truncated — comment exceeded size limit)"
	}

	ghCmd := exec.Command("gh", "pr", "comment",
		"--repo", ghRepo,
		strconv.Itoa(prNumber),
		"--body-file", "-")
	ghCmd.Stdin = strings.NewReader(body)
	ghCmd.Stderr = os.Stderr

	if out, err := ghCmd.Output(); err != nil {
		return fmt.Errorf(
			"gh pr comment: %v (output: %s)",
			err, string(out))
	}
	return nil
}
