package github

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/roborev-dev/roborev/internal/review"
)

// CommentMarker is an invisible HTML marker embedded in every roborev PR
// comment so subsequent runs can find and update the existing comment
// instead of creating duplicates.
const CommentMarker = "<!-- roborev-pr-comment -->"

// Test seams for subprocess creation.
var (
	execCommand  = exec.CommandContext
	execLookPath = exec.LookPath
)

// FindExistingComment searches for an existing roborev comment on the
// given PR. It returns the comment ID if found, or 0 if no match exists.
// env, when non-nil, is set on the subprocess (e.g. for GitHub App tokens).
func FindExistingComment(ctx context.Context, ghRepo string, prNumber int, env []string) (int64, error) {
	jqFilter := fmt.Sprintf(
		`[.[] | select(.body | contains(%q)) | .id] | first // empty`,
		CommentMarker,
	)

	cmd := execCommand(ctx, "gh", "api",
		fmt.Sprintf("repos/%s/issues/%d/comments", ghRepo, prNumber),
		"--paginate",
		"--jq", jqFilter,
	)
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("gh api list comments: %w: %s", err, stderr.String())
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return 0, nil
	}

	id, err := strconv.ParseInt(output, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse comment ID %q: %w", output, err)
	}
	return id, nil
}

// UpsertPRComment creates or updates a roborev PR comment. It prepends
// the CommentMarker, truncates to review.MaxCommentLen, and either
// patches an existing comment or creates a new one.
func UpsertPRComment(ctx context.Context, ghRepo string, prNumber int, body string, env []string) error {
	// Prepend marker before truncation so it always survives.
	body = CommentMarker + "\n" + body

	if len(body) > review.MaxCommentLen {
		body = body[:review.MaxCommentLen] +
			"\n\n...(truncated — comment exceeded size limit)"
	}

	existingID, err := FindExistingComment(ctx, ghRepo, prNumber, env)
	if err != nil {
		return fmt.Errorf("find existing comment: %w", err)
	}

	if existingID > 0 {
		return patchComment(ctx, ghRepo, existingID, body, env)
	}
	return createComment(ctx, ghRepo, prNumber, body, env)
}

func patchComment(ctx context.Context, ghRepo string, commentID int64, body string, env []string) error {
	cmd := execCommand(ctx, "gh", "api",
		"-X", "PATCH",
		fmt.Sprintf("repos/%s/issues/comments/%d", ghRepo, commentID),
		"--input", "-",
	)
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"body":%s}`, jsonString(body)))
	if env != nil {
		cmd.Env = env
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh api PATCH comment: %w: %s", err, string(out))
	}
	return nil
}

func createComment(ctx context.Context, ghRepo string, prNumber int, body string, env []string) error {
	cmd := execCommand(ctx, "gh", "pr", "comment",
		"--repo", ghRepo,
		strconv.Itoa(prNumber),
		"--body-file", "-",
	)
	cmd.Stdin = strings.NewReader(body)
	if env != nil {
		cmd.Env = env
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh pr comment: %w: %s", err, string(out))
	}
	return nil
}

// jsonString returns a JSON-encoded string value (with surrounding quotes).
func jsonString(s string) string {
	// Use strconv.Quote which produces a valid JSON string literal
	// for ASCII and escapes everything else correctly.
	return strconv.Quote(s)
}
