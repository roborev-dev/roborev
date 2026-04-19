package prompt

import (
	"fmt"
	"strings"
)

const classifySystemPrompt = `You are a routing classifier. Given a code change, decide whether it warrants a design review.

A design review is warranted when the change:
- Introduces new packages, modules, or external interfaces
- Modifies public APIs, data schemas, or protocols
- Changes architectural boundaries (package dependencies, cross-cutting concerns)
- Alters invariants, state machines, or concurrency primitives
- Introduces a new dependency with non-trivial surface area

A design review is NOT warranted when the change is:
- An isolated bugfix with no interface change
- Test-only or documentation-only (unless docs describe new design)
- Formatting, rename, or lint cleanup
- Straightforward performance or readability improvements within existing
  boundaries

Return your verdict as a single JSON object matching the provided schema.
`

// ClassifyInput is the structured input a classify prompt needs.
type ClassifyInput struct {
	Subject  string
	Body     string
	DiffStat string
	Diff     string
	MaxBytes int
}

// BuildClassifyPrompt constructs the compact classifier prompt.
func BuildClassifyPrompt(in ClassifyInput) (string, error) {
	var b strings.Builder
	b.WriteString(classifySystemPrompt)
	b.WriteString("\n---\n\n## Commit\n\n")
	b.WriteString("Subject: ")
	b.WriteString(in.Subject)
	b.WriteString("\n")
	if in.Body != "" {
		b.WriteString("\nBody:\n")
		b.WriteString(in.Body)
		b.WriteString("\n")
	}

	if in.DiffStat != "" {
		b.WriteString("\n## Stat\n\n")
		b.WriteString("```\n")
		b.WriteString(strings.TrimSpace(in.DiffStat))
		b.WriteString("\n```\n")
	}

	b.WriteString("\n## Diff\n\n")
	b.WriteString("```diff\n")
	diff := in.Diff
	if in.MaxBytes > 0 && b.Len()+len(diff)+20 > in.MaxBytes {
		avail := max(in.MaxBytes-b.Len()-40, 0)
		if avail < len(diff) {
			diff = diff[:avail]
			diff += "\n... (truncated)\n"
		}
	}
	b.WriteString(diff)
	if !strings.HasSuffix(diff, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```\n")

	out := b.String()
	if in.MaxBytes > 0 && len(out) > in.MaxBytes*2 {
		return "", fmt.Errorf("classify prompt exceeds hard cap (%d > %d)", len(out), in.MaxBytes*2)
	}
	return out, nil
}
