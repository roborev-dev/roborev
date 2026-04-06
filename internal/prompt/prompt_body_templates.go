package prompt

import (
	"bytes"
	"strconv"
	"strings"
	"text/template"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
)

type markdownSectionView struct {
	Heading string
	Body    string
}

type reviewCommentView struct {
	Responder string
	Response  string
}

type previousReviewView struct {
	Commit    string
	Output    string
	Comments  []reviewCommentView
	Available bool
}

type reviewAttemptView struct {
	Label    string
	Agent    string
	When     string
	Output   string
	Comments []reviewCommentView
}

type optionalSectionsView struct {
	ProjectGuidelines *markdownSectionView
	AdditionalContext string
	PreviousReviews   []previousReviewView
	InRangeReviews    []inRangeReviewView
	PreviousAttempts  []reviewAttemptView
}

type currentCommitSectionView struct {
	Commit  string
	Subject string
	Author  string
	Message string
}

type commitRangeEntryView struct {
	Commit  string
	Subject string
}

type commitRangeSectionView struct {
	Count   int
	Entries []commitRangeEntryView
}

type dirtyChangesSectionView struct {
	Description string
}

type diffSectionView struct {
	Heading  string
	Body     string
	Fallback string
}

type singlePromptView struct {
	Optional optionalSectionsView
	Current  currentCommitSectionView
	Diff     diffSectionView
}

type rangePromptView struct {
	Optional optionalSectionsView
	Current  commitRangeSectionView
	Diff     diffSectionView
}

type dirtyPromptView struct {
	Optional optionalSectionsView
	Current  dirtyChangesSectionView
	Diff     diffSectionView
}

type inRangeReviewView struct {
	Commit   string
	Agent    string
	Verdict  string
	Output   string
	Comments []reviewCommentView
}

type addressAttemptView struct {
	Responder string
	Response  string
	When      string
}

type addressPromptView struct {
	ProjectGuidelines *markdownSectionView
	ToolAttempts      []addressAttemptView
	UserComments      []addressAttemptView
	SeverityFilter    string
	ReviewFindings    string
	OriginalDiff      string
	JobID             int64
}

type reviewAttemptContext struct {
	Review    storage.Review
	Responses []storage.Response
}

type systemPromptView struct {
	NoSkillsInstruction string
	CurrentDate         string
}

type inlineDiffView struct {
	Body string
}

type dirtyTruncatedDiffFallbackView struct {
	Body string
}

var promptTemplates = template.Must(template.New("prompt-templates").ParseFS(
	templateFS,
	"templates/*.md.gotmpl",
))

func renderSinglePrompt(view singlePromptView) (string, error) {
	return executePromptTemplate("assembled_single.md.gotmpl", view)
}

func renderRangePrompt(view rangePromptView) (string, error) {
	return executePromptTemplate("assembled_range.md.gotmpl", view)
}

func renderDirtyPrompt(view dirtyPromptView) (string, error) {
	return executePromptTemplate("assembled_dirty.md.gotmpl", view)
}

func renderAddressPrompt(view addressPromptView) (string, error) {
	return executePromptTemplate("assembled_address.md.gotmpl", view)
}

func fitSinglePrompt(limit int, view singlePromptView) (string, error) {
	body, err := renderSinglePrompt(view)
	if err != nil {
		return "", err
	}
	if len(body) <= limit {
		return body, nil
	}

	for trimOptionalSections(&view.Optional) {
		body, err = renderSinglePrompt(view)
		if err != nil {
			return "", err
		}
		if len(body) <= limit {
			return body, nil
		}
	}

	if view.Current.Message != "" {
		view.Current.Message = ""
		body, err = renderSinglePrompt(view)
		if err != nil {
			return "", err
		}
		if len(body) <= limit {
			return body, nil
		}
	}

	if view.Current.Author != "" {
		view.Current.Author = ""
		body, err = renderSinglePrompt(view)
		if err != nil {
			return "", err
		}
		if len(body) <= limit {
			return body, nil
		}
	}

	for len(body) > limit && view.Current.Subject != "" {
		overflow := len(body) - limit
		view.Current.Subject = truncateUTF8(view.Current.Subject, max(0, len(view.Current.Subject)-overflow))
		body, err = renderSinglePrompt(view)
		if err != nil {
			return "", err
		}
	}

	return hardCapPrompt(body, limit), nil
}

func fitRangePrompt(limit int, view rangePromptView) (string, error) {
	_, body, err := trimRangePromptView(limit, view)
	if err != nil {
		return "", err
	}
	return hardCapPrompt(body, limit), nil
}

func cloneCommitRangeSectionView(view commitRangeSectionView) commitRangeSectionView {
	cloned := view
	if len(view.Entries) == 0 {
		return cloned
	}
	cloned.Entries = append([]commitRangeEntryView(nil), view.Entries...)
	return cloned
}

func trimRangePromptView(limit int, view rangePromptView) (rangePromptView, string, error) {
	view.Current = cloneCommitRangeSectionView(view.Current)
	body, err := renderRangePrompt(view)
	if err != nil {
		return rangePromptView{}, "", err
	}
	if len(body) <= limit {
		return view, body, nil
	}

	for trimOptionalSections(&view.Optional) {
		body, err = renderRangePrompt(view)
		if err != nil {
			return rangePromptView{}, "", err
		}
		if len(body) <= limit {
			return view, body, nil
		}
	}

	for i := len(view.Current.Entries) - 1; i >= 0 && len(body) > limit; i-- {
		if view.Current.Entries[i].Subject == "" {
			continue
		}
		view.Current.Entries[i].Subject = ""
		body, err = renderRangePrompt(view)
		if err != nil {
			return rangePromptView{}, "", err
		}
	}

	for len(view.Current.Entries) > 0 && len(body) > limit {
		view.Current.Entries = view.Current.Entries[:len(view.Current.Entries)-1]
		body, err = renderRangePrompt(view)
		if err != nil {
			return rangePromptView{}, "", err
		}
	}

	return view, body, nil
}

func fitDirtyPrompt(limit int, view dirtyPromptView) (string, error) {
	body, err := renderDirtyPrompt(view)
	if err != nil {
		return "", err
	}
	if len(body) <= limit {
		return body, nil
	}

	for trimOptionalSections(&view.Optional) {
		body, err = renderDirtyPrompt(view)
		if err != nil {
			return "", err
		}
		if len(body) <= limit {
			return body, nil
		}
	}

	return hardCapPrompt(body, limit), nil
}

func trimOptionalSections(view *optionalSectionsView) bool {
	switch {
	case len(view.PreviousAttempts) > 0:
		view.PreviousAttempts = nil
	case len(view.PreviousReviews) > 0:
		view.PreviousReviews = nil
	case view.AdditionalContext != "":
		view.AdditionalContext = ""
	case view.ProjectGuidelines != nil:
		view.ProjectGuidelines = nil
	default:
		return false
	}
	return true
}

func renderSystemPrompt(name string, view systemPromptView) (string, error) {
	return executePromptTemplate(name, view)
}

func renderAddressPromptFromSections(view addressPromptView) (string, error) {
	return renderAddressPrompt(view)
}

func renderOptionalSectionsFromView(view optionalSectionsView) (string, error) {
	body, err := executePromptTemplate("optional_sections", view)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(body), nil
}

func renderOptionalSectionsPrefix(view optionalSectionsView) (string, error) {
	body, err := renderOptionalSectionsFromView(view)
	if err != nil || body == "" {
		return body, err
	}
	return body + "\n\n", nil
}

func renderCurrentCommitRequired(view currentCommitSectionView) (string, error) {
	return executePromptTemplate("current_commit_required", view)
}

func renderCurrentCommitOverflow(view currentCommitSectionView) (string, error) {
	return executePromptTemplate("current_commit_overflow", view)
}

func renderCommitRangeRequired(view commitRangeSectionView) (string, error) {
	return executePromptTemplate("commit_range_required", view)
}

func renderCommitRangeOverflow(view commitRangeSectionView) (string, error) {
	return executePromptTemplate("commit_range_overflow", view)
}

func renderDirtyChangesSection(view dirtyChangesSectionView) (string, error) {
	return executePromptTemplate("dirty_changes", view)
}

func renderDiffBlock(view diffSectionView) (string, error) {
	return executePromptTemplate("diff_block", view)
}

func renderInlineDiff(body string) (string, error) {
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return executePromptTemplate("inline_diff", inlineDiffView{Body: body})
}

func renderDirtyTruncatedDiffFallback(body string) (string, error) {
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return executePromptTemplate("dirty_truncated_diff_fallback", dirtyTruncatedDiffFallbackView{Body: body})
}

func previousReviewViews(contexts []ReviewContext) []previousReviewView {
	views := make([]previousReviewView, 0, len(contexts))
	for _, ctx := range contexts {
		view := previousReviewView{Commit: git.ShortSHA(ctx.SHA)}
		if ctx.Review != nil {
			view.Available = true
			view.Output = ctx.Review.Output
		}
		if len(ctx.Responses) > 0 {
			view.Comments = make([]reviewCommentView, 0, len(ctx.Responses))
			for _, resp := range ctx.Responses {
				view.Comments = append(view.Comments, reviewCommentView{Responder: resp.Responder, Response: resp.Response})
			}
		}
		views = append(views, view)
	}
	return views
}

func renderPreviousReviewsFromContexts(contexts []ReviewContext) (string, error) {
	return renderOptionalSectionsFromView(optionalSectionsView{PreviousReviews: previousReviewViews(contexts)})
}

func inRangeReviewViews(contexts []ReviewContext) []inRangeReviewView {
	views := make([]inRangeReviewView, 0, len(contexts))
	for _, ctx := range contexts {
		if ctx.Review == nil {
			continue
		}
		verdict := storage.ParseVerdict(ctx.Review.Output)
		verdictLabel := "unknown"
		switch verdict {
		case "P":
			verdictLabel = "passed"
		case "F":
			verdictLabel = "failed"
		}
		view := inRangeReviewView{
			Commit:  git.ShortSHA(ctx.SHA),
			Agent:   ctx.Review.Agent,
			Verdict: verdictLabel,
			Output:  ctx.Review.Output,
		}
		if len(ctx.Responses) > 0 {
			view.Comments = make([]reviewCommentView, 0, len(ctx.Responses))
			for _, resp := range ctx.Responses {
				view.Comments = append(view.Comments, reviewCommentView{Responder: resp.Responder, Response: resp.Response})
			}
		}
		views = append(views, view)
	}
	return views
}

func reviewAttemptViews(reviews []storage.Review) []reviewAttemptView {
	views := make([]reviewAttemptView, 0, len(reviews))
	for i, review := range reviews {
		when := ""
		if !review.CreatedAt.IsZero() {
			when = review.CreatedAt.Format("2006-01-02 15:04")
		}
		views = append(views, reviewAttemptView{
			Label:  "Review Attempt " + strconv.Itoa(i+1),
			Agent:  review.Agent,
			When:   when,
			Output: review.Output,
		})
	}
	return views
}

func renderPreviousAttemptsFromReviews(reviews []storage.Review) (string, error) {
	return renderOptionalSectionsFromView(optionalSectionsView{PreviousAttempts: reviewAttemptViews(reviews)})
}

func previousAttemptViewsFromContexts(attempts []reviewAttemptContext) []reviewAttemptView {
	views := make([]reviewAttemptView, 0, len(attempts))
	for i, attempt := range attempts {
		view := reviewAttemptView{
			Label:  "Review Attempt " + strconv.Itoa(i+1),
			Agent:  attempt.Review.Agent,
			Output: attempt.Review.Output,
		}
		if !attempt.Review.CreatedAt.IsZero() {
			view.When = attempt.Review.CreatedAt.Format("2006-01-02 15:04")
		}
		if len(attempt.Responses) > 0 {
			view.Comments = make([]reviewCommentView, 0, len(attempt.Responses))
			for _, resp := range attempt.Responses {
				view.Comments = append(view.Comments, reviewCommentView{Responder: resp.Responder, Response: resp.Response})
			}
		}
		views = append(views, view)
	}
	return views
}

func executePromptTemplate(name string, view any) (string, error) {
	var buf bytes.Buffer
	if err := promptTemplates.ExecuteTemplate(&buf, name, view); err != nil {
		return "", err
	}
	return buf.String(), nil
}
