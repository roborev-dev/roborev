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

type addressAttemptView struct {
	Responder string
	Response  string
	When      string
}

type addressPromptView struct {
	ProjectGuidelines *markdownSectionView
	PreviousAttempts  []addressAttemptView
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

type singlePromptBodyView struct {
	OptionalContext string
	CurrentRequired string
	CurrentOverflow string
	DiffSection     string
}

type rangePromptBodyView struct {
	OptionalContext string
	CurrentRequired string
	CurrentOverflow string
	DiffSection     string
}

type dirtyPromptBodyView struct {
	OptionalContext string
	CurrentRequired string
	DiffSection     string
}

var promptTemplates = template.Must(template.New("prompt-templates").ParseFS(
	templateFS,
	"templates/prompt_sections.tmpl",
	"templates/assembled_single.tmpl",
	"templates/assembled_range.tmpl",
	"templates/assembled_dirty.tmpl",
	"templates/assembled_address.tmpl",
	"templates/default_review.tmpl",
	"templates/default_dirty.tmpl",
	"templates/default_range.tmpl",
	"templates/default_security.tmpl",
	"templates/default_address.tmpl",
	"templates/default_design_review.tmpl",
))

func renderSinglePrompt(view singlePromptView) (string, error) {
	return executePromptTemplate("assembled_single.tmpl", view)
}

func renderRangePrompt(view rangePromptView) (string, error) {
	return executePromptTemplate("assembled_range.tmpl", view)
}

func renderDirtyPrompt(view dirtyPromptView) (string, error) {
	return executePromptTemplate("assembled_dirty.tmpl", view)
}

func renderSinglePromptFromSections(optionalContext string, current currentCommitSectionView, diff diffSectionView) (string, error) {
	return renderSinglePrompt(singlePromptView{
		Optional: optionalSectionsView{AdditionalContext: optionalContext},
		Current:  current,
		Diff:     diff,
	})
}

func renderRangePromptFromSections(optionalContext string, current commitRangeSectionView, diff diffSectionView) (string, error) {
	return renderRangePrompt(rangePromptView{
		Optional: optionalSectionsView{AdditionalContext: optionalContext},
		Current:  current,
		Diff:     diff,
	})
}

func renderAddressPrompt(view addressPromptView) (string, error) {
	return executePromptTemplate("assembled_address.tmpl", view)
}

func fitSinglePromptSections(limit int, optional optionalSectionsView, current currentCommitSectionView, diff diffSectionView) (string, error) {
	optionalContext, err := renderOptionalSectionsPrefix(optional)
	if err != nil {
		return "", err
	}
	currentRequired, err := renderCurrentCommitRequired(current)
	if err != nil {
		return "", err
	}
	currentOverflow, err := renderCurrentCommitOverflow(current)
	if err != nil {
		return "", err
	}
	diffSection, err := renderDiffBlock(diff)
	if err != nil {
		return "", err
	}
	body, err := fitSinglePromptBody(limit, singlePromptBodyView{
		OptionalContext: optionalContext,
		CurrentRequired: currentRequired,
		CurrentOverflow: currentOverflow,
		DiffSection:     diffSection,
	})
	if err != nil {
		return "", err
	}

	rendered, err := renderSinglePrompt(singlePromptView{Optional: optional, Current: current, Diff: diff})
	if err != nil {
		return "", err
	}
	if len(rendered) > limit {
		return body, nil
	}
	return rendered, nil
}

func fitRangePromptSections(limit int, optional optionalSectionsView, current commitRangeSectionView, diff diffSectionView) (string, error) {
	optionalContext, err := renderOptionalSectionsPrefix(optional)
	if err != nil {
		return "", err
	}
	currentRequired, err := renderCommitRangeRequired(current)
	if err != nil {
		return "", err
	}
	currentOverflow, err := renderCommitRangeOverflow(current)
	if err != nil {
		return "", err
	}
	diffSection, err := renderDiffBlock(diff)
	if err != nil {
		return "", err
	}
	body, err := fitRangePromptBody(limit, rangePromptBodyView{
		OptionalContext: optionalContext,
		CurrentRequired: currentRequired,
		CurrentOverflow: currentOverflow,
		DiffSection:     diffSection,
	})
	if err != nil {
		return "", err
	}

	rendered, err := renderRangePrompt(rangePromptView{Optional: optional, Current: current, Diff: diff})
	if err != nil {
		return "", err
	}
	if len(rendered) > limit {
		return body, nil
	}
	return rendered, nil
}

func fitDirtyPromptView(limit int, view dirtyPromptView) (string, error) {
	optionalContext, err := renderOptionalSectionsPrefix(view.Optional)
	if err != nil {
		return "", err
	}
	currentRequired, err := renderDirtyChangesSection(view.Current)
	if err != nil {
		return "", err
	}
	diffSection, err := renderDiffBlock(view.Diff)
	if err != nil {
		return "", err
	}
	body, err := fitDirtyPromptBody(limit, dirtyPromptBodyView{
		OptionalContext: optionalContext,
		CurrentRequired: currentRequired,
		DiffSection:     diffSection,
	})
	if err != nil {
		return "", err
	}

	rendered, err := renderDirtyPrompt(view)
	if err != nil {
		return "", err
	}
	if len(rendered) > limit {
		return body, nil
	}
	return rendered, nil
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

func renderAdditionalContextBlock(additionalContext string) (string, error) {
	trimmed := strings.TrimSpace(additionalContext)
	if trimmed == "" {
		return "", nil
	}
	return trimmed + "\n\n", nil
}

func renderProjectGuidelinesBlock(guidelines string) (string, error) {
	trimmed := strings.TrimSpace(guidelines)
	if trimmed == "" {
		return "", nil
	}
	return "## Project Guidelines\n\n" + trimmed + "\n\n", nil
}

func renderDiffBlock(view diffSectionView) (string, error) {
	return executePromptTemplate("diff_block", view)
}

func renderSinglePromptBody(view singlePromptBodyView) (string, error) {
	return view.OptionalContext + view.CurrentRequired + view.CurrentOverflow + view.DiffSection, nil
}

func renderRangePromptBody(view rangePromptBodyView) (string, error) {
	return view.OptionalContext + view.CurrentRequired + view.CurrentOverflow + view.DiffSection, nil
}

func renderDirtyPromptBody(view dirtyPromptBodyView) (string, error) {
	return view.OptionalContext + view.CurrentRequired + view.DiffSection, nil
}

func fitSinglePromptBody(limit int, view singlePromptBodyView) (string, error) {
	body, err := renderSinglePromptBody(view)
	if err != nil {
		return "", err
	}
	if len(body) <= limit {
		return body, nil
	}

	overflow := len(body) - limit
	if overflow > 0 && len(view.OptionalContext) > 0 {
		originalLen := len(view.OptionalContext)
		view.OptionalContext = truncateUTF8(view.OptionalContext, max(0, len(view.OptionalContext)-overflow))
		overflow -= originalLen - len(view.OptionalContext)
	}
	if overflow > 0 && len(view.CurrentOverflow) > 0 {
		view.CurrentOverflow = truncateUTF8(view.CurrentOverflow, max(0, len(view.CurrentOverflow)-overflow))
	}

	body, err = renderSinglePromptBody(view)
	if err != nil {
		return "", err
	}
	return hardCapPrompt(body, limit), nil
}

func fitRangePromptBody(limit int, view rangePromptBodyView) (string, error) {
	body, err := renderRangePromptBody(view)
	if err != nil {
		return "", err
	}
	if len(body) <= limit {
		return body, nil
	}

	overflow := len(body) - limit
	if overflow > 0 && len(view.OptionalContext) > 0 {
		originalLen := len(view.OptionalContext)
		view.OptionalContext = truncateUTF8(view.OptionalContext, max(0, len(view.OptionalContext)-overflow))
		overflow -= originalLen - len(view.OptionalContext)
	}
	if overflow > 0 && len(view.CurrentOverflow) > 0 {
		view.CurrentOverflow = truncateUTF8(view.CurrentOverflow, max(0, len(view.CurrentOverflow)-overflow))
	}

	body, err = renderRangePromptBody(view)
	if err != nil {
		return "", err
	}
	return hardCapPrompt(body, limit), nil
}

func fitDirtyPromptBody(limit int, view dirtyPromptBodyView) (string, error) {
	body, err := renderDirtyPromptBody(view)
	if err != nil {
		return "", err
	}
	if len(body) <= limit {
		return body, nil
	}

	overflow := len(body) - limit
	if overflow > 0 && len(view.OptionalContext) > 0 {
		view.OptionalContext = truncateUTF8(view.OptionalContext, max(0, len(view.OptionalContext)-overflow))
	}

	body, err = renderDirtyPromptBody(view)
	if err != nil {
		return "", err
	}
	return hardCapPrompt(body, limit), nil
}

func executePromptTemplate(name string, view any) (string, error) {
	var buf bytes.Buffer
	if err := promptTemplates.ExecuteTemplate(&buf, name, view); err != nil {
		return "", err
	}
	return buf.String(), nil
}
