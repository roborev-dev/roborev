package prompt

import (
	"bytes"
	"text/template"
)

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

var promptBodyTemplates = template.Must(template.New("prompt-bodies").ParseFS(
	templateFS,
	"templates/assembled_single.tmpl",
	"templates/assembled_range.tmpl",
	"templates/assembled_dirty.tmpl",
))

func renderSinglePromptBody(view singlePromptBodyView) (string, error) {
	return executePromptBodyTemplate("assembled_single.tmpl", view)
}

func renderRangePromptBody(view rangePromptBodyView) (string, error) {
	return executePromptBodyTemplate("assembled_range.tmpl", view)
}

func renderDirtyPromptBody(view dirtyPromptBodyView) (string, error) {
	return executePromptBodyTemplate("assembled_dirty.tmpl", view)
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

func fitSinglePromptBodyViewToVariants(limit int, view singlePromptBodyView, variants ...string) singlePromptBodyView {
	if len(variants) == 0 {
		return view
	}

	shortestLen := len(variants[len(variants)-1])
	softBudget := max(0, limit-len(view.CurrentRequired)-shortestLen)
	softLen := len(view.OptionalContext) + len(view.CurrentOverflow)
	if softLen > softBudget {
		overflow := softLen - softBudget
		if overflow > 0 && len(view.OptionalContext) > 0 {
			originalLen := len(view.OptionalContext)
			view.OptionalContext = truncateUTF8(view.OptionalContext, max(0, len(view.OptionalContext)-overflow))
			overflow -= originalLen - len(view.OptionalContext)
		}
		if overflow > 0 && len(view.CurrentOverflow) > 0 {
			view.CurrentOverflow = truncateUTF8(view.CurrentOverflow, max(0, len(view.CurrentOverflow)-overflow))
		}
	}

	remaining := max(0, limit-len(view.OptionalContext)-len(view.CurrentRequired)-len(view.CurrentOverflow))
	for _, variant := range variants {
		if len(variant) <= remaining {
			view.DiffSection = variant
			return view
		}
	}
	view.DiffSection = truncateUTF8(variants[len(variants)-1], remaining)
	return view
}

func executePromptBodyTemplate(name string, view any) (string, error) {
	var buf bytes.Buffer
	if err := promptBodyTemplates.ExecuteTemplate(&buf, name, view); err != nil {
		return "", err
	}
	return buf.String(), nil
}
