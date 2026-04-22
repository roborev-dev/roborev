package autotype

import (
	"fmt"
	"regexp"

	"github.com/bmatcuk/doublestar/v4"
)

// Heuristics holds the thresholds and patterns consulted before falling back
// to the classifier.
type Heuristics struct {
	MinDiffLines   int
	LargeDiffLines int
	LargeFileCount int

	TriggerPaths []string
	SkipPaths    []string

	TriggerMessagePatterns []string
	SkipMessagePatterns    []string
}

// DefaultHeuristics returns the baked-in defaults that ship when the user
// hasn't overridden anything in config.
func DefaultHeuristics() Heuristics {
	return Heuristics{
		MinDiffLines:   10,
		LargeDiffLines: 500,
		LargeFileCount: 10,
		TriggerPaths: []string{
			"**/migrations/**",
			"**/schema/**",
			"**/*.sql",
			"docs/superpowers/specs/**",
			"docs/design/**",
			"docs/plans/**",
			"**/*-design.md",
			"**/*-plan.md",
		},
		SkipPaths: []string{
			"**/*.md",
			"**/*_test.go",
			"**/*.spec.*",
			"**/testdata/**",
		},
		TriggerMessagePatterns: []string{
			`\b(refactor|redesign|rewrite|architect|breaking)\b`,
		},
		SkipMessagePatterns: []string{
			`^(docs|test|style|chore)(\(.+\))?:`,
		},
	}
}

// Validate compiles each regex and checks each glob. Returns an error with the
// offending pattern so misconfigurations surface loudly at load time.
func (h Heuristics) Validate() error {
	for _, p := range h.TriggerPaths {
		if _, err := doublestar.Match(p, ""); err != nil {
			return fmt.Errorf("invalid trigger_paths glob %q: %w", p, err)
		}
	}
	for _, p := range h.SkipPaths {
		if _, err := doublestar.Match(p, ""); err != nil {
			return fmt.Errorf("invalid skip_paths glob %q: %w", p, err)
		}
	}
	for _, p := range h.TriggerMessagePatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("invalid trigger_message_patterns regex %q: %w", p, err)
		}
	}
	for _, p := range h.SkipMessagePatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("invalid skip_message_patterns regex %q: %w", p, err)
		}
	}
	return nil
}
