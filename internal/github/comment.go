package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/roborev-dev/roborev/internal/review"
)

// CommentMarker is an invisible HTML marker embedded in every roborev PR
// comment so subsequent runs can find and update the existing comment
// instead of creating duplicates.
const CommentMarker = "<!-- roborev-pr-comment -->"

// Test seam for subprocess creation.
var execCommand = exec.CommandContext

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

	// With --paginate, --jq runs per page so stdout may contain
	// multiple lines when several pages match. Use the first non-empty
	// line (the oldest matching comment).
	firstLine := ""
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			firstLine = s
			break
		}
	}
	if firstLine == "" {
		return 0, nil
	}

	id, err := strconv.ParseInt(firstLine, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse comment ID %q: %w", firstLine, err)
	}
	return id, nil
}

// UpsertPRComment creates or updates a roborev PR comment. It prepends
// the CommentMarker, truncates to review.MaxCommentLen, and either
// patches an existing comment or creates a new one.
func UpsertPRComment(ctx context.Context, ghRepo string, prNumber int, body string, env []string) error {
	// Prepend marker before truncation so it always survives.
	body = CommentMarker + "\n" + body

	const truncSuffix = "\n\n...(truncated — comment exceeded size limit)"
	maxBody := review.MaxCommentLen - len(truncSuffix)
	if len(body) > review.MaxCommentLen {
		// Slice to maxBody, then back up to a valid UTF-8 boundary.
		cut := body[:maxBody]
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
		body = cut + truncSuffix
	}

	existingID, err := FindExistingComment(ctx, ghRepo, prNumber, env)
	if err != nil {
		return fmt.Errorf("find existing comment: %w", err)
	}

	if existingID > 0 {
		if err := patchComment(ctx, ghRepo, existingID, body, env); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "HTTP 403") ||
				strings.Contains(msg, "HTTP 404") {
				// Comment belongs to a different actor/token.
				// Fall back to creating a new one.
				log.Printf(
					"warning: patch comment %d: %v "+
						"(falling back to new comment)",
					existingID, err)
			} else {
				return fmt.Errorf("patch comment %d: %w",
					existingID, err)
			}
		} else {
			return nil
		}
	}
	return createComment(ctx, ghRepo, prNumber, body, env)
}

func patchComment(ctx context.Context, ghRepo string, commentID int64, body string, env []string) error {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("marshal PATCH payload: %w", err)
	}
	cmd := execCommand(ctx, "gh", "api",
		"-X", "PATCH",
		fmt.Sprintf("repos/%s/issues/comments/%d", ghRepo, commentID),
		"--input", "-",
	)
	cmd.Stdin = bytes.NewReader(payload)
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
