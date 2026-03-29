package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	PRDiscussionSourceIssueComment  = "issue_comment"
	PRDiscussionSourceReview        = "review"
	PRDiscussionSourceReviewComment = "review_comment"
)

type PRDiscussionComment struct {
	Author    string
	Body      string
	Source    string
	Path      string
	Line      int
	CreatedAt time.Time
}

type ghCommentUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type ghIssueComment struct {
	Body      string        `json:"body"`
	CreatedAt string        `json:"created_at"`
	User      ghCommentUser `json:"user"`
}

type ghReview struct {
	Body        string        `json:"body"`
	SubmittedAt string        `json:"submitted_at"`
	User        ghCommentUser `json:"user"`
}

type ghReviewComment struct {
	Body         string        `json:"body"`
	CreatedAt    string        `json:"created_at"`
	Path         string        `json:"path"`
	Line         *int          `json:"line"`
	OriginalLine *int          `json:"original_line"`
	User         ghCommentUser `json:"user"`
}

type ghCollaborator struct {
	Login    string `json:"login"`
	RoleName string `json:"role_name"`
}

// ListPRDiscussionComments returns human-authored pull request discussion
// comments across top-level issue comments, review summaries, and inline review
// comments. Results are sorted oldest-first.
func ListPRDiscussionComments(ctx context.Context, ghRepo string, prNumber int, env []string) ([]PRDiscussionComment, error) {
	var comments []PRDiscussionComment

	issueLines, err := ghAPIBase64Lines(ctx, fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", ghRepo, prNumber), env)
	if err != nil {
		return nil, err
	}
	for _, line := range issueLines {
		var item ghIssueComment
		if err := decodeBase64JSON(line, &item); err != nil {
			return nil, fmt.Errorf("parse issue comment: %w", err)
		}
		if !isHumanGitHubUser(item.User) || isRoborevCommentBody(item.Body) || strings.TrimSpace(item.Body) == "" {
			continue
		}
		comments = append(comments, PRDiscussionComment{
			Author:    item.User.Login,
			Body:      item.Body,
			Source:    PRDiscussionSourceIssueComment,
			CreatedAt: parseGitHubTimestamp(item.CreatedAt),
		})
	}

	reviewLines, err := ghAPIBase64Lines(ctx, fmt.Sprintf("repos/%s/pulls/%d/reviews?per_page=100", ghRepo, prNumber), env)
	if err != nil {
		return nil, err
	}
	for _, line := range reviewLines {
		var item ghReview
		if err := decodeBase64JSON(line, &item); err != nil {
			return nil, fmt.Errorf("parse review summary: %w", err)
		}
		if !isHumanGitHubUser(item.User) || isRoborevCommentBody(item.Body) || strings.TrimSpace(item.Body) == "" {
			continue
		}
		comments = append(comments, PRDiscussionComment{
			Author:    item.User.Login,
			Body:      item.Body,
			Source:    PRDiscussionSourceReview,
			CreatedAt: parseGitHubTimestamp(item.SubmittedAt),
		})
	}

	inlineLines, err := ghAPIBase64Lines(ctx, fmt.Sprintf("repos/%s/pulls/%d/comments?per_page=100", ghRepo, prNumber), env)
	if err != nil {
		return nil, err
	}
	for _, line := range inlineLines {
		var item ghReviewComment
		if err := decodeBase64JSON(line, &item); err != nil {
			return nil, fmt.Errorf("parse review comment: %w", err)
		}
		if !isHumanGitHubUser(item.User) || isRoborevCommentBody(item.Body) || strings.TrimSpace(item.Body) == "" {
			continue
		}
		comment := PRDiscussionComment{
			Author:    item.User.Login,
			Body:      item.Body,
			Source:    PRDiscussionSourceReviewComment,
			Path:      item.Path,
			CreatedAt: parseGitHubTimestamp(item.CreatedAt),
		}
		if item.Line != nil && *item.Line > 0 {
			comment.Line = *item.Line
		} else if item.OriginalLine != nil && *item.OriginalLine > 0 {
			comment.Line = *item.OriginalLine
		}
		comments = append(comments, comment)
	}

	sort.SliceStable(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})

	return comments, nil
}

// ListTrustedRepoCollaborators returns collaborator logins that have effective
// maintain or admin access to the repository. Logins are normalized to lower
// case for case-insensitive matching against GitHub comment authors.
func ListTrustedRepoCollaborators(ctx context.Context, ghRepo string, env []string) (map[string]struct{}, error) {
	lines, err := ghAPIBase64Lines(ctx, fmt.Sprintf("repos/%s/collaborators?per_page=100&affiliation=all", ghRepo), env)
	if err != nil {
		return nil, err
	}

	trusted := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		var item ghCollaborator
		if err := decodeBase64JSON(line, &item); err != nil {
			return nil, fmt.Errorf("parse collaborator: %w", err)
		}
		login := strings.ToLower(strings.TrimSpace(item.Login))
		if login == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.RoleName)) {
		case "admin", "maintain":
			trusted[login] = struct{}{}
		}
	}

	return trusted, nil
}

func ghAPIBase64Lines(ctx context.Context, endpoint string, env []string) ([]string, error) {
	cmd := execCommand(ctx, "gh", "api", endpoint, "--paginate", "--jq", ".[] | @base64")
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh api %s: %w: %s", endpoint, err, string(out))
	}

	var lines []string
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func decodeBase64JSON(line string, dst any) error {
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return fmt.Errorf("decode base64: %w", err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}

func isHumanGitHubUser(user ghCommentUser) bool {
	if strings.EqualFold(strings.TrimSpace(user.Type), "bot") {
		return false
	}
	return !strings.HasSuffix(strings.TrimSpace(user.Login), "[bot]")
}

func isRoborevCommentBody(body string) bool {
	return strings.Contains(body, CommentMarker)
}

func parseGitHubTimestamp(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return ts
}
