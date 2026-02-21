package main

import (
	"context"
	"encoding/json"
	"errors"
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
	// Validate flag-only inputs early (before git/config
	// checks) so users get clear errors even outside a repo.
	if opts.reviewTypes != "" {
		if _, err := config.ValidateReviewTypes(
			splitTrimmed(opts.reviewTypes)); err != nil {
			return err
		}
	}
	if opts.reasoning != "" {
		if _, err := config.NormalizeReasoning(
			opts.reasoning); err != nil {
			return err
		}
	}
	if opts.minSeverity != "" {
		if _, err := config.NormalizeMinSeverity(
			opts.minSeverity); err != nil {
			return err
		}
	}

	// Resolve git ref (from flag or environment — no repo
	// needed, so validate before the git repo check).
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

	// Determine repo root
	root, err := git.GetRepoRoot(".")
	if err != nil {
		return fmt.Errorf(
			"not a git repository — " +
				"run this from inside a git repo")
	}

	// Load configs (warn on error, don't fail)
	globalCfg, err := config.LoadGlobal()
	if err != nil {
		log.Printf(
			"ci review: load global config: %v "+
				"(using defaults)", err)
	}
	repoCfg, err := config.LoadRepoConfig(root)
	if err != nil {
		log.Printf(
			"ci review: load repo config: %v "+
				"(using defaults)", err)
	}

	// Resolve agents
	agents := resolveAgentList(
		opts.agents, repoCfg, globalCfg)
	if len(agents) == 0 {
		return fmt.Errorf("no agents configured " +
			"(check --agent flag or config)")
	}

	// Resolve review types
	reviewTypes := resolveReviewTypes(
		opts.reviewTypes, repoCfg, globalCfg)
	if len(reviewTypes) == 0 {
		return fmt.Errorf("no review types configured " +
			"(check --review-types flag or config)")
	}
	reviewTypes, err = config.ValidateReviewTypes(
		reviewTypes)
	if err != nil {
		return err
	}

	// Resolve reasoning
	reasoningLevel, err := resolveCIReasoning(
		opts.reasoning, repoCfg, globalCfg)
	if err != nil {
		return err
	}

	// Resolve min severity
	minSev, err := resolveCIMinSeverity(
		opts.minSeverity, repoCfg, globalCfg)
	if err != nil {
		return err
	}

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
		RepoPath:     root,
		GitRef:       gitRef,
		Agents:       agents,
		ReviewTypes:  reviewTypes,
		Reasoning:    reasoningLevel,
		GlobalConfig: globalCfg,
	}

	results := review.RunBatch(ctx, batchCfg)

	// Determine HEAD SHA for comment formatting
	headSHA := extractHeadSHA(gitRef)

	// Synthesize
	comment, synthErr := review.Synthesize(
		ctx, results, review.SynthesizeOpts{
			Agent:       synthAgent,
			MinSeverity: minSev,
			RepoPath:    root,
			GitRef:      gitRef,
			HeadSHA:     headSHA,
		})
	if synthErr != nil &&
		!errors.Is(synthErr, review.ErrAllFailed) {
		return fmt.Errorf("synthesize: %w", synthErr)
	}

	// Output to stdout (even on all-failed, for CI logs)
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

	// Exit non-zero when all review jobs failed
	if synthErr != nil {
		return synthErr
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
	return []string{config.ReviewTypeSecurity}
}

func resolveCIReasoning(
	flag string,
	repoCfg *config.RepoConfig,
	globalCfg *config.Config,
) (string, error) {
	if flag != "" {
		n, err := config.NormalizeReasoning(flag)
		if err != nil {
			return "", err
		}
		return n, nil
	}
	if repoCfg != nil && repoCfg.CI.Reasoning != "" {
		if n, err := config.NormalizeReasoning(
			repoCfg.CI.Reasoning); err == nil {
			return n, nil
		}
	}
	return "thorough", nil
}

func resolveCIMinSeverity(
	flag string,
	repoCfg *config.RepoConfig,
	globalCfg *config.Config,
) (string, error) {
	if flag != "" {
		n, err := config.NormalizeMinSeverity(flag)
		if err != nil {
			return "", err
		}
		return n, nil
	}
	if repoCfg != nil && repoCfg.CI.MinSeverity != "" {
		if n, err := config.NormalizeMinSeverity(
			repoCfg.CI.MinSeverity); err == nil {
			return n, nil
		}
	}
	if globalCfg != nil && globalCfg.CI.MinSeverity != "" {
		if n, err := config.NormalizeMinSeverity(
			globalCfg.CI.MinSeverity); err == nil {
			return n, nil
		}
	}
	return "", nil
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

// ghPREvent represents the pull_request fields from a
// GitHub Actions event payload.
type ghPREvent struct {
	PullRequest struct {
		Number int `json:"number"`
		Base   struct {
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}

// readPREvent reads and unmarshals the GitHub Actions event
// file pointed to by GITHUB_EVENT_PATH.
func readPREvent() (*ghPREvent, error) {
	eventPath := os.Getenv("GITHUB_EVENT_PATH")
	if eventPath == "" {
		return nil, fmt.Errorf("GITHUB_EVENT_PATH not set")
	}
	data, err := os.ReadFile(eventPath)
	if err != nil {
		return nil, fmt.Errorf("read event file: %w", err)
	}
	var event ghPREvent
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("parse event JSON: %w", err)
	}
	return &event, nil
}

// detectGitRef attempts to auto-detect the git ref range from
// the GitHub Actions environment.
func detectGitRef() (string, error) {
	event, err := readPREvent()
	if err != nil {
		return "", err
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
	event, err := readPREvent()
	if err == nil && event.PullRequest.Number > 0 {
		return event.PullRequest.Number, nil
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
	if len(body) > review.MaxCommentLen {
		body = body[:review.MaxCommentLen] +
			"\n\n...(truncated — comment exceeded " +
			"size limit)"
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
