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

func executePromptBodyTemplate(name string, view any) (string, error) {
	var buf bytes.Buffer
	if err := promptBodyTemplates.ExecuteTemplate(&buf, name, view); err != nil {
		return "", err
	}
	return buf.String(), nil
}
