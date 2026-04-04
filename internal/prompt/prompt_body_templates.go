package prompt

import (
	"bytes"
	"strconv"
	"strings"
	"text/template"
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

func fitSinglePromptSections(limit int, optionalContext string, current currentCommitSectionView, diff diffSectionView) (string, error) {
	currentRequired, err := renderSinglePromptBody(singlePromptBodyView{
		CurrentRequired: "## Current Commit\n\n**Commit:** " + current.Commit + "\n\n",
	})
	if err != nil {
		return "", err
	}
	currentOverflow := ""
	if current.Subject != "" {
		currentOverflow += "**Subject:** " + current.Subject + "\n"
	}
	if current.Author != "" {
		currentOverflow += "**Author:** " + current.Author + "\n"
	}
	if current.Message != "" {
		currentOverflow += "\n**Message:**\n" + current.Message + "\n\n"
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

	renderedDiff := diff
	trimmedDiff := diffSection
	if after, ok := strings.CutPrefix(trimmedDiff, "### Diff\n\n"); ok {
		trimmedDiff = after
	}
	if strings.HasPrefix(trimmedDiff, "(Diff too large") {
		renderedDiff.Body = ""
		renderedDiff.Fallback = trimmedDiff
	} else {
		renderedDiff.Body = trimmedDiff
		renderedDiff.Fallback = ""
	}
	rendered, err := renderSinglePromptFromSections(optionalContext, current, renderedDiff)
	if err != nil {
		return "", err
	}
	if len(rendered) > limit {
		return body, nil
	}
	return rendered, nil
}

func fitRangePromptSections(limit int, optionalContext string, current commitRangeSectionView, diff diffSectionView) (string, error) {
	currentRequired := "## Commit Range\n\nReviewing " + strconv.Itoa(len(current.Entries)) + " commits:\n\n"
	var currentOverflow strings.Builder
	for _, entry := range current.Entries {
		currentOverflow.WriteString("- ")
		currentOverflow.WriteString(entry.Commit)
		if entry.Subject != "" {
			currentOverflow.WriteString(" ")
			currentOverflow.WriteString(entry.Subject)
		}
		currentOverflow.WriteString("\n")
	}
	currentOverflow.WriteString("\n")

	diffSection, err := renderDiffBlock(diff)
	if err != nil {
		return "", err
	}
	body, err := fitRangePromptBody(limit, rangePromptBodyView{
		OptionalContext: optionalContext,
		CurrentRequired: currentRequired,
		CurrentOverflow: currentOverflow.String(),
		DiffSection:     diffSection,
	})
	if err != nil {
		return "", err
	}

	renderedDiff := diff
	trimmedDiff := diffSection
	if after, ok := strings.CutPrefix(trimmedDiff, "### Combined Diff\n\n"); ok {
		trimmedDiff = after
	}
	if strings.HasPrefix(trimmedDiff, "(Diff too large") {
		renderedDiff.Body = ""
		renderedDiff.Fallback = trimmedDiff
	} else {
		renderedDiff.Body = trimmedDiff
		renderedDiff.Fallback = ""
	}
	rendered, err := renderRangePromptFromSections(optionalContext, current, renderedDiff)
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
