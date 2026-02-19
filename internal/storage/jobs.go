package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
)

// ParseVerdict extracts P (pass) or F (fail) from review output.
// Returns "P" only if a clear pass indicator appears at the start of a line.
// Rejects lines containing caveats like "but", "however", "except".
// Also fails if severity labels (Critical/High/Medium/Low) indicate findings.
func ParseVerdict(output string) string {
	// First check for severity labels which indicate actual findings
	// These appear as "- Medium —", "* Low:", "Critical -", etc.
	if hasSeverityLabel(output) {
		return "F"
	}

	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(strings.ToLower(line))
		// Normalize curly apostrophes to straight apostrophes (LLMs sometimes use these)
		trimmed = strings.ReplaceAll(trimmed, "\u2018", "'") // left single quote
		trimmed = strings.ReplaceAll(trimmed, "\u2019", "'") // right single quote
		// Strip markdown formatting (bold, italic, headers)
		trimmed = stripMarkdown(trimmed)
		// Strip leading list markers (bullets, numbers, etc.)
		trimmed = stripListMarker(trimmed)
		// Strip leading field label (e.g., "Review Findings: " or "Verdict: ")
		trimmed = stripFieldLabel(trimmed)

		// Check for pass indicators at start of line
		isPass := strings.HasPrefix(trimmed, "no issues") ||
			strings.HasPrefix(trimmed, "no findings") ||
			strings.HasPrefix(trimmed, "i didn't find any issues") ||
			strings.HasPrefix(trimmed, "i did not find any issues") ||
			strings.HasPrefix(trimmed, "i found no issues")

		if isPass {
			// Reject if line contains caveats (check for word boundaries)
			if hasCaveat(trimmed) {
				continue
			}
			return "P"
		}
	}
	return "F"
}

// stripMarkdown removes common markdown formatting from a line
func stripMarkdown(s string) string {
	// Strip leading markdown headers (##, ###, etc.)
	for strings.HasPrefix(s, "#") {
		s = strings.TrimPrefix(s, "#")
	}
	s = strings.TrimSpace(s)

	// Strip bold/italic markers (**, __, *, _)
	// Handle ** and __ first (bold), then * and _ (italic)
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	// Don't strip single * or _ as they might be intentional (e.g., bullet points handled separately)

	return strings.TrimSpace(s)
}

// stripListMarker removes leading bullet/number markers from a line
func stripListMarker(s string) string {
	// Handle: "- ", "* ", "1. ", "99) ", "100. ", etc.
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return s
	}
	// Check for bullet markers
	if s[0] == '-' || s[0] == '*' {
		return strings.TrimSpace(s[1:])
	}
	// Check for numbered lists - scan all leading digits
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			continue
		}
		if i > 0 && (s[i] == '.' || s[i] == ')' || s[i] == ':') {
			return strings.TrimSpace(s[i+1:])
		}
		break
	}
	return s
}

// stripFieldLabel removes a known leading field label from structured review output.
// Handles "Review Findings: No issues found." and similar patterns.
func stripFieldLabel(s string) string {
	labels := []string{
		"review findings",
		"findings",
		"review result",
		"result",
		"verdict",
		"review",
	}
	for _, label := range labels {
		if strings.HasPrefix(s, label) {
			rest := s[len(label):]
			if len(rest) > 0 && rest[0] == ':' {
				return strings.TrimSpace(rest[1:])
			}
		}
	}
	return s
}

// hasSeverityLabel checks if the output contains severity labels indicating findings.
// Matches patterns like "- Medium —", "* Low:", "Critical — issue", etc.
// Checks lines that start with bullets/numbers OR directly with severity words.
// Requires separators to be followed by space to avoid "High-level overview".
// Skips lines that appear to be part of a severity legend/rubric.
func hasSeverityLabel(output string) bool {
	lc := strings.ToLower(output)
	severities := []string{"critical", "high", "medium", "low"}
	lines := strings.Split(lc, "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}

		// Check if line starts with bullet/number - if so, strip it
		first := trimmed[0]
		hasBullet := first == '-' || first == '*' || (first >= '0' && first <= '9') ||
			strings.HasPrefix(trimmed, "•")

		checkText := trimmed
		if hasBullet {
			// Strip leading bullets/asterisks/numbers
			checkText = strings.TrimLeft(trimmed, "-*•0123456789.) ")
			checkText = strings.TrimSpace(checkText)
		}

		// Strip markdown formatting (bold, headers) before checking
		checkText = stripMarkdown(checkText)

		// Check if text starts with a severity word
		for _, sev := range severities {
			if !strings.HasPrefix(checkText, sev) {
				continue
			}

			// Check if followed by separator (dash, em-dash, colon, pipe)
			rest := checkText[len(sev):]
			rest = strings.TrimSpace(rest)
			if len(rest) == 0 {
				continue
			}

			// Check for valid separator
			hasValidSep := false
			// Check for em-dash or en-dash (these are unambiguous)
			if strings.HasPrefix(rest, "—") || strings.HasPrefix(rest, "–") {
				hasValidSep = true
			}
			// Check for colon or pipe (unambiguous separators)
			if rest[0] == ':' || rest[0] == '|' {
				hasValidSep = true
			}
			// For hyphen, require space after to avoid "High-level"
			if rest[0] == '-' && len(rest) > 1 && rest[1] == ' ' {
				hasValidSep = true
			}

			if !hasValidSep {
				continue
			}

			// Skip if this looks like a legend/rubric entry
			// Check if previous non-empty line is a legend header
			if isLegendEntry(lines, i) {
				continue
			}

			return true
		}

		// Check for "severity: <level>" pattern (e.g., "**Severity**: High")
		if strings.HasPrefix(checkText, "severity") {
			rest := checkText[len("severity"):]
			rest = strings.TrimSpace(rest)
			hasSep := len(rest) > 0 && (rest[0] == ':' || rest[0] == '|' ||
				strings.HasPrefix(rest, "—") || strings.HasPrefix(rest, "–"))
			// Accept hyphen-minus when followed by space (mirrors the severity-word branch)
			if !hasSep && len(rest) > 1 && rest[0] == '-' && rest[1] == ' ' {
				hasSep = true
			}
			if hasSep {
				// Skip separator and whitespace
				rest = strings.TrimLeft(rest, ":-–—| ")
				rest = strings.TrimSpace(rest)
				for _, sev := range severities {
					if strings.HasPrefix(rest, sev) {
						if !isLegendEntry(lines, i) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// isLegendEntry checks if a line at index i appears to be part of a severity legend/rubric
// by looking at preceding lines for legend indicators. Scans up to 10 lines back,
// skipping empty lines, severity lines, and description lines that may appear
// between legend entries.
func isLegendEntry(lines []string, i int) bool {
	for j := i - 1; j >= 0 && j >= i-10; j-- {
		prev := strings.TrimSpace(lines[j])
		if len(prev) == 0 {
			continue
		}

		// Strip markdown and list markers so bolded headers like
		// "**Severity levels:**" are recognized the same as plain text.
		prev = stripMarkdown(stripListMarker(prev))

		// Check for legend header patterns (ends with ":" and contains indicator word)
		if strings.HasSuffix(prev, ":") || strings.HasSuffix(prev, "：") {
			if strings.Contains(prev, "severity") || strings.Contains(prev, "level") ||
				strings.Contains(prev, "legend") || strings.Contains(prev, "scale") ||
				strings.Contains(prev, "rating") || strings.Contains(prev, "priority") {
				return true
			}
		}
	}
	return false
}

// hasCaveat checks if the line contains contrastive words or additional sentences with issues
func hasCaveat(s string) bool {
	// Split on clause boundaries first, then check each clause
	// Replace clause separators with a marker we can split on
	normalized := s
	normalized = strings.ReplaceAll(normalized, "—", "|")
	normalized = strings.ReplaceAll(normalized, "–", "|")
	normalized = strings.ReplaceAll(normalized, ";", "|")
	normalized = strings.ReplaceAll(normalized, ": ", "|")
	normalized = strings.ReplaceAll(normalized, ", ", "|")
	normalized = strings.ReplaceAll(normalized, ". ", "|")
	normalized = strings.ReplaceAll(normalized, "? ", "|")
	normalized = strings.ReplaceAll(normalized, "! ", "|")

	for clause := range strings.SplitSeq(normalized, "|") {
		if checkClauseForCaveat(clause) {
			return true
		}
	}
	return false
}

// checkClauseForCaveat checks a single clause for caveats
func checkClauseForCaveat(clause string) bool {
	// Normalize punctuation and collapse whitespace
	normalized := strings.ReplaceAll(clause, ",", " ")
	normalized = strings.ReplaceAll(normalized, ":", " ")
	// Collapse multiple spaces to single space
	for strings.Contains(normalized, "  ") {
		normalized = strings.ReplaceAll(normalized, "  ", " ")
	}
	normalized = strings.TrimSpace(normalized)
	lc := strings.ToLower(normalized)

	// Benign phrases that contain issue keywords but aren't actual issues
	benignPhrases := []string{
		"problem statement", "problem domain", "problem space", "problem definition",
		"issue tracker", "issue tracking", "issue number", "issue #",
		"vulnerability disclosure", "vulnerability report", "vulnerability scan",
		"error handling", "error message", "error messages", "error code", "error codes",
		"error type", "error types", "error response", "error responses",
	}
	// Negative qualifiers that indicate the benign phrase is actually a problem
	negativeQualifiers := []string{
		"is missing", "are missing", "missing",
		"is wrong", "are wrong", "wrong",
		"is incorrect", "are incorrect", "incorrect",
		"is broken", "are broken", "broken",
		"is bad", "are bad",
		"need", "needs", "needed",
	}
	// Process each benign phrase, checking each occurrence individually
	for _, bp := range benignPhrases {
		var result strings.Builder
		remaining := lc
		for {
			idx := strings.Index(remaining, bp)
			if idx < 0 {
				result.WriteString(remaining)
				break
			}
			// Copy everything before the match
			result.WriteString(remaining[:idx])
			afterPhrase := remaining[idx+len(bp):]

			// Check if this is a complete phrase (followed by boundary or end)
			isCompleteBoundary := len(afterPhrase) == 0 ||
				afterPhrase[0] == ' ' || afterPhrase[0] == '.' ||
				afterPhrase[0] == ',' || afterPhrase[0] == ';' || afterPhrase[0] == ':'

			// Check if followed by negative qualifier
			hasNegative := false
			if isCompleteBoundary {
				for _, nq := range negativeQualifiers {
					if strings.HasPrefix(strings.TrimSpace(afterPhrase), nq) {
						hasNegative = true
						break
					}
				}
			}

			// Only remove if complete boundary match AND no negative qualifier
			if isCompleteBoundary && !hasNegative {
				result.WriteString(" ") // Replace with space
			} else {
				result.WriteString(bp) // Keep the phrase
			}
			remaining = afterPhrase
		}
		lc = result.String()
	}
	// Collapse multiple spaces after removals
	for strings.Contains(lc, "  ") {
		lc = strings.ReplaceAll(lc, "  ", " ")
	}
	lc = strings.TrimSpace(lc)

	// Check for "found <issue>" pattern - handle both mid-clause and start-of-clause
	issueKeywords := []string{
		"issue", "issues", "bug", "bugs", "error", "errors",
		"crash", "crashes", "panic", "panics", "fail", "failure", "failures",
		"break", "breaks", "race", "races", "problem", "problems",
		"vulnerability", "vulnerabilities",
	}
	quantifiers := []string{"", "a ", "an ", "the ", "some ", "multiple ", "several ", "many ", "a few ", "few ", "two ", "three ", "various ", "numerous "}
	adjectives := []string{"", "critical ", "severe ", "serious ", "major ", "minor ", "potential ", "possible ", "obvious ", "subtle ", "important ", "significant "}

	// Check for "found" at start of clause
	if strings.HasPrefix(lc, "found ") {
		afterFound := lc[6:]
		if !strings.HasPrefix(afterFound, "no ") && !strings.HasPrefix(afterFound, "none") &&
			!strings.HasPrefix(afterFound, "nothing") && !strings.HasPrefix(afterFound, "0 ") &&
			!strings.HasPrefix(afterFound, "zero ") && !strings.HasPrefix(afterFound, "without ") {
			for _, kw := range issueKeywords {
				for _, q := range quantifiers {
					for _, adj := range adjectives {
						if strings.HasPrefix(afterFound, q+adj+kw) {
							return true
						}
					}
				}
			}
		}
	}

	// Check for " found " mid-clause
	remaining := lc
	for {
		idx := strings.Index(remaining, " found ")
		if idx < 0 {
			break
		}
		afterFound := remaining[idx+7:]
		remaining = afterFound

		isNegated := strings.HasPrefix(afterFound, "no ") ||
			strings.HasPrefix(afterFound, "none") ||
			strings.HasPrefix(afterFound, "nothing") ||
			strings.HasPrefix(afterFound, "0 ") ||
			strings.HasPrefix(afterFound, "zero ") ||
			strings.HasPrefix(afterFound, "without ")
		if isNegated {
			continue
		}

		for _, kw := range issueKeywords {
			for _, q := range quantifiers {
				for _, adj := range adjectives {
					if strings.HasPrefix(afterFound, q+adj+kw) {
						return true
					}
				}
			}
		}
	}

	// Check for contextual issue/problem/vulnerability patterns
	// Only match if not negated (not preceded by "no ")
	contextualPatterns := []string{
		"there are issue", "there are problem", "there is a issue", "there is a problem",
		"there is an issue", "there are vulnerabilit",
		"issues remain", "problems remain", "vulnerabilities remain",
		"issues exist", "problems exist", "vulnerabilities exist",
		"has issue", "has problem", "have issue", "have problem",
		"has issues", "has problems", "have issues", "have problems",
		"has vulnerabilit", "have vulnerabilit",
	}
	// Negators must appear near the pattern (within last 3 words, allowing adjectives)
	contextNegators := []string{"no", "don't", "doesn't"}
	for _, pattern := range contextualPatterns {
		if idx := strings.Index(lc, pattern); idx >= 0 {
			// Check if preceded by negation within last few words
			start := max(idx-30, 0)
			prefix := strings.TrimSpace(lc[start:idx])
			if !hasNegatorInLastWords(prefix, contextNegators, 3) {
				return true
			}
		}
	}
	// Check "issues/problems with/in" but only if not negated
	withInPatterns := []string{"issues with", "problems with", "issue with", "problem with",
		"issues in", "problems in", "issue in", "problem in"}
	// Simple negators that work as single tokens
	withInNegators := []string{"no", "didn't"}
	for _, pattern := range withInPatterns {
		if idx := strings.Index(lc, pattern); idx >= 0 {
			// Check if preceded by negation within last few words
			start := max(idx-30, 0)
			prefix := strings.TrimSpace(lc[start:idx])
			isNegated := hasNegatorInLastWords(prefix, withInNegators, 4)
			if !isNegated {
				// Check for "not" followed by verb in last few words
				isNegated = hasNotVerbPattern(prefix)
			}
			if !isNegated {
				return true
			}
		}
	}

	// Check if clause describes what was checked (not findings)
	// e.g., "I checked for bugs, security issues..."
	hasCheckPhrase := strings.Contains(lc, "checked for") ||
		strings.Contains(lc, "looking for") ||
		strings.Contains(lc, "looked for") ||
		strings.Contains(lc, "searching for") ||
		strings.Contains(lc, "searched for")

	if hasCheckPhrase {
		// Check for "still <issue>" pattern (e.g., "it still crashes")
		if _, afterStill, found := strings.Cut(lc, " still "); found {
			stillKeywords := []string{"crash", "panic", "fail", "break", "error", "bug"}
			for _, kw := range stillKeywords {
				if strings.HasPrefix(afterStill, kw) || strings.Contains(afterStill, " "+kw) {
					return true
				}
			}
		}

		// Check for contrastive markers with issue words AFTER the marker
		for _, marker := range []string{" however ", " but "} {
			if _, tail, found := strings.Cut(lc, marker); found {
				if strings.Contains(tail, "crash") || strings.Contains(tail, "panic") ||
					strings.Contains(tail, "error") || strings.Contains(tail, "bug") ||
					strings.Contains(tail, "fail") || strings.Contains(tail, "break") ||
					strings.Contains(tail, "race") || strings.Contains(tail, "issue") ||
					strings.Contains(tail, "problem") || strings.Contains(tail, "vulnerabilit") {
					// Make sure it's not negated like "found none"
					if !strings.Contains(tail, "found none") && !strings.Contains(tail, "found nothing") &&
						!strings.Contains(tail, "no ") && !strings.Contains(tail, "none") {
						return true
					}
				}
			}
		}

		// Check phrase with no findings - not a caveat
		return false
	}

	words := strings.Fields(lc)
	for i, w := range words {
		// Strip punctuation from both sides for word matching
		w = strings.Trim(w, ".,;:!?()[]\"'")
		// Contrastive words
		if w == "but" || w == "however" || w == "except" || w == "beyond" {
			return true
		}
		// Negative indicators that suggest problems (unless negated)
		// Note: "issue", "problem", "vulnerability" are too ambiguous for unconditional
		// matching (e.g., "problem statement", "issue tracker", "vulnerability disclosure").
		// They're handled in check-phrase context and contrast detection instead.
		if w == "fail" || w == "fails" || w == "failed" || w == "failing" ||
			w == "break" || w == "breaks" || w == "broken" ||
			w == "crash" || w == "crashes" || w == "panic" || w == "panics" ||
			w == "error" || w == "errors" || w == "bug" || w == "bugs" {
			// Check if preceded by negation within this clause
			if isNegated(words, i) {
				continue
			}
			return true
		}
	}
	return false
}

// isNegated checks if a negative indicator at position i is preceded by a negation word
// within the same clause. Skips common stopwords when looking back.
// Handles double-negation: "not without errors" means errors exist, so returns false.
func isNegated(words []string, i int) bool {
	stopwords := map[string]bool{
		"the": true, "a": true, "an": true,
		"of": true, "to": true, "in": true,
		"have": true, "has": true, "had": true,
		"been": true, "be": true, "is": true, "are": true, "was": true, "were": true,
		"tests": true, "test": true, "code": true, "build": true,
	}
	negators := map[string]bool{
		"no": true, "not": true, "never": true, "none": true,
		"zero": true, "0": true, "without": true,
		"didn't": true, "didnt": true, "doesn't": true, "doesnt": true,
		"hasn't": true, "hasnt": true, "haven't": true, "havent": true,
		"won't": true, "wont": true, "wouldn't": true, "wouldnt": true,
		"can't": true, "cant": true, "cannot": true,
		// Words indicating the issue is being fixed/prevented, not reported
		// Conjugated/gerund/past forms are unconditional negators
		"avoids": true, "avoiding": true, "avoided": true,
		"prevents": true, "preventing": true, "prevented": true,
		"fixes": true, "fixing": true, "fixed": true,
	}
	// Base verb forms that could be imperatives at clause start
	baseVerbForms := map[string]bool{
		"avoid": true, "fix": true, "prevent": true,
	}

	// Look back up to 5 non-stopwords, stopping at clause boundaries
	checked := 0
	for j := i - 1; j >= 0 && checked < 5; j-- {
		raw := words[j]
		w := strings.Trim(raw, ".,;:!?()[]\"'")

		// Stop at clause boundaries (words ending with sentence/clause punctuation)
		// Include comma and colon to prevent negation bleeding across clauses
		if endsWithClauseBoundary(raw) {
			break
		}

		if stopwords[w] {
			continue // Skip stopwords
		}
		checked++
		if negators[w] {
			// Handle double-negation: "not without" means the problem exists
			if w == "without" && j > 0 {
				prev := strings.Trim(words[j-1], ".,;:!?()[]\"'")
				if prev == "not" {
					return false // Double-negative = problem exists
				}
			}
			return true
		}
		// Base verb forms are only negators when NOT at clause start (imperatives)
		// "Avoid errors." = imperative (command), not a negator
		// "This will avoid errors." = descriptive, is a negator
		if baseVerbForms[w] {
			isAtClauseStart := j == 0 // Start of input
			if j > 0 {
				prevRaw := words[j-1]
				// Check for sentence/clause ending punctuation (trailing only, not mid-token)
				if endsWithClauseBoundary(prevRaw) {
					isAtClauseStart = true
				}
				// Check for list markers: -, *, or numbered lists (1., 2., etc.)
				trimmed := strings.Trim(prevRaw, ".,;:!?()[]\"'")
				if trimmed == "-" || trimmed == "*" || isNumberedListMarker(prevRaw) {
					isAtClauseStart = true
				}
			}
			if !isAtClauseStart {
				return true // Mid-clause base form = negator
			}
			// At clause start = imperative, continue searching
		}
	}
	return false
}

// endsWithClauseBoundary checks if a word ends with clause-boundary punctuation.
// This checks trailing characters only to avoid false positives on tokens like
// "10:30", "1,000", or URLs that contain punctuation mid-token.
// It also handles punctuation followed by closing quotes/parens (e.g., `found."`, `found.)`).
func endsWithClauseBoundary(word string) bool {
	if len(word) == 0 {
		return false
	}
	// Strip trailing wrappers (quotes, parens, brackets) to find the actual punctuation
	stripped := strings.TrimRight(word, "\"'`)]}»")
	if len(stripped) == 0 {
		return false
	}
	lastChar := stripped[len(stripped)-1]
	return lastChar == '.' || lastChar == ';' || lastChar == '?' ||
		lastChar == '!' || lastChar == ',' || lastChar == ':'
}

// isNumberedListMarker checks if a word is a numbered list marker like "1.", "2.", "10."
func isNumberedListMarker(word string) bool {
	if len(word) < 2 || word[len(word)-1] != '.' {
		return false
	}
	// Check if everything before the dot is digits
	for i := 0; i < len(word)-1; i++ {
		if word[i] < '0' || word[i] > '9' {
			return false
		}
	}
	return true
}

// hasNegatorInLastWords checks if any negator appears in the last n words of the prefix.
// This allows adjectives between negator and pattern (e.g., "no significant issues").
// Stops at clause boundaries (punctuation like comma, semicolon).
func hasNegatorInLastWords(prefix string, negators []string, n int) bool {
	words := strings.Fields(prefix)
	if len(words) == 0 {
		return false
	}
	// Check last n words, but stop at clause boundaries
	checked := 0
	for i := len(words) - 1; i >= 0 && checked < n; i-- {
		raw := words[i]
		// Stop at clause boundaries (words ending with comma, semicolon, etc.)
		if strings.ContainsAny(raw, ",;:") {
			break
		}
		w := strings.Trim(raw, ".,;:!?()[]\"'")
		if slices.Contains(negators, w) {
			return true
		}
		checked++
	}
	return false
}

// hasNotVerbPattern checks if the last few words contain negation followed by a verb like "find".
// Handles: "did not find", "not finding", "can't find", "cannot find", "couldn't find".
func hasNotVerbPattern(prefix string) bool {
	words := strings.Fields(prefix)
	if len(words) < 2 {
		return false
	}
	verbs := []string{"find", "finding", "found", "see", "seeing", "detect", "detecting", "have"}
	// Contractions that imply "not" - only can't/cannot/couldn't which express inability
	// Excludes won't/wouldn't which are often conditional ("wouldn't have issues if...")
	contractions := map[string]bool{
		"can't": true, "cant": true, "cannot": true,
		"couldn't": true, "couldnt": true,
	}
	// Only check last 5 words, stop at clause boundaries
	checked := 0
	for i := len(words) - 1; i >= 1 && checked < 5; i-- {
		raw := words[i]
		if strings.ContainsAny(raw, ",;:") {
			break
		}
		w := strings.Trim(raw, ".,;:!?()[]\"'")
		prevRaw := words[i-1]
		prev := strings.Trim(prevRaw, ".,;:!?()[]\"'")
		// Check for "not" followed by verb
		if prev == "not" && slices.Contains(verbs, w) {
			return true
		}
		// Check for contraction followed by verb (e.g., "can't find", "couldn't see")
		if contractions[prev] && slices.Contains(verbs, w) {
			return true
		}
		checked++
	}
	return false
}

// parseSQLiteTime parses a time string from SQLite which may be in different formats.
// Handles RFC3339 (what we write), SQLite datetime('now') format, and timezone variants.
// Returns zero time for empty strings. Logs a warning for non-empty unrecognized formats
// to surface driver/schema issues instead of silently producing zero times.
func parseSQLiteTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try RFC3339 first (what we write for started_at, finished_at)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	// Try SQLite datetime format (from datetime('now'))
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
	}
	// Try with timezone
	if t, err := time.Parse("2006-01-02T15:04:05Z07:00", s); err == nil {
		return t
	}
	log.Printf("storage: warning: unrecognized time format %q", s)
	return time.Time{}
}

// EnqueueOpts contains options for creating any type of review job.
// The job type is inferred from which fields are set (in priority order):
//   - Prompt != "" → "task" (custom prompt job)
//   - DiffContent != "" → "dirty" (uncommitted changes)
//   - CommitID > 0 → "review" (single commit)
//   - otherwise → "range" (commit range)
type EnqueueOpts struct {
	RepoID       int64
	CommitID     int64  // >0 for single-commit reviews
	GitRef       string // SHA, "start..end" range, or "dirty"
	Branch       string
	Agent        string
	Model        string
	Reasoning    string
	ReviewType   string // e.g. "security" — changes which system prompt is used
	PatchID      string // Stable patch-id for rebase tracking
	DiffContent  string // For dirty reviews (captured at enqueue time)
	Prompt       string // For task jobs (pre-stored prompt)
	OutputPrefix string // Prefix to prepend to review output
	Agentic      bool   // Allow file edits and command execution
	Label        string // Display label in TUI for task jobs (default: "prompt")
	JobType      string // Explicit job type (review/range/dirty/task/compact); inferred if empty
}

// EnqueueJob creates a new review job. The job type is inferred from opts.
func (db *DB) EnqueueJob(opts EnqueueOpts) (*ReviewJob, error) {
	reasoning := opts.Reasoning
	if reasoning == "" {
		reasoning = "thorough"
	}

	// Determine job type from fields (use explicit type if provided)
	var jobType string
	if opts.JobType != "" {
		jobType = opts.JobType
	} else {
		switch {
		case opts.Prompt != "":
			jobType = JobTypeTask
		case opts.DiffContent != "":
			jobType = JobTypeDirty
		case opts.CommitID > 0:
			jobType = JobTypeReview
		default:
			jobType = JobTypeRange
		}
	}

	// For task jobs, use Label as git_ref display value
	gitRef := opts.GitRef
	if jobType == JobTypeTask {
		if opts.Label != "" {
			gitRef = opts.Label
		} else if gitRef == "" {
			gitRef = "prompt"
		}
	}

	agenticInt := 0
	if opts.Agentic {
		agenticInt = 1
	}

	uid := GenerateUUID()
	machineID, _ := db.GetMachineID()
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	// Use NULL for commit_id when not a single-commit review
	var commitIDParam any
	if opts.CommitID > 0 {
		commitIDParam = opts.CommitID
	}

	result, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, branch, agent, model, reasoning,
			status, job_type, review_type, patch_id, diff_content, prompt, agentic, output_prefix,
			uuid, source_machine_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		opts.RepoID, commitIDParam, gitRef, nullString(opts.Branch),
		opts.Agent, nullString(opts.Model), reasoning,
		jobType, opts.ReviewType, nullString(opts.PatchID),
		nullString(opts.DiffContent), nullString(opts.Prompt), agenticInt,
		nullString(opts.OutputPrefix),
		uid, machineID, nowStr)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	job := &ReviewJob{
		ID:              id,
		RepoID:          opts.RepoID,
		GitRef:          gitRef,
		Branch:          opts.Branch,
		Agent:           opts.Agent,
		Model:           opts.Model,
		Reasoning:       reasoning,
		JobType:         jobType,
		ReviewType:      opts.ReviewType,
		PatchID:         opts.PatchID,
		Status:          JobStatusQueued,
		EnqueuedAt:      now,
		Prompt:          opts.Prompt,
		Agentic:         opts.Agentic,
		OutputPrefix:    opts.OutputPrefix,
		UUID:            uid,
		SourceMachineID: machineID,
		UpdatedAt:       &now,
	}
	if opts.CommitID > 0 {
		job.CommitID = &opts.CommitID
	}
	if opts.DiffContent != "" {
		job.DiffContent = &opts.DiffContent
	}
	return job, nil
}

// ClaimJob atomically claims the next queued job for a worker
func (db *DB) ClaimJob(workerID string) (*ReviewJob, error) {
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	// Atomically claim a job by updating it in a single statement
	// This prevents race conditions where two workers select the same job
	result, err := db.Exec(`
		UPDATE review_jobs
		SET status = 'running', worker_id = ?, started_at = ?, updated_at = ?
		WHERE id = (
			SELECT id FROM review_jobs
			WHERE status = 'queued'
			ORDER BY enqueued_at
			LIMIT 1
		)
	`, workerID, nowStr, nowStr)
	if err != nil {
		return nil, err
	}

	// Check if we claimed anything
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rowsAffected == 0 {
		return nil, nil // No jobs available
	}

	// Now fetch the job we just claimed
	var job ReviewJob
	var enqueuedAt string
	var commitID sql.NullInt64
	var commitSubject sql.NullString
	var diffContent sql.NullString
	var prompt sql.NullString
	var model, branch sql.NullString
	var agenticInt int
	var jobType sql.NullString
	var reviewType sql.NullString
	var outputPrefix sql.NullString
	var patchID sql.NullString
	err = db.QueryRow(`
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.branch, j.agent, j.model, j.reasoning, j.status, j.enqueued_at,
		       r.root_path, r.name, c.subject, j.diff_content, j.prompt, COALESCE(j.agentic, 0), j.job_type, j.review_type,
		       j.output_prefix, j.patch_id
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.worker_id = ? AND j.status = 'running'
		ORDER BY j.started_at DESC
		LIMIT 1
	`, workerID).Scan(&job.ID, &job.RepoID, &commitID, &job.GitRef, &branch, &job.Agent, &model, &job.Reasoning, &job.Status, &enqueuedAt,
		&job.RepoPath, &job.RepoName, &commitSubject, &diffContent, &prompt, &agenticInt, &jobType, &reviewType,
		&outputPrefix, &patchID)
	if err != nil {
		return nil, err
	}

	if commitID.Valid {
		job.CommitID = &commitID.Int64
	}
	if commitSubject.Valid {
		job.CommitSubject = commitSubject.String
	}
	if diffContent.Valid {
		job.DiffContent = &diffContent.String
	}
	if prompt.Valid {
		job.Prompt = prompt.String
	}
	if model.Valid {
		job.Model = model.String
	}
	if branch.Valid {
		job.Branch = branch.String
	}
	job.Agentic = agenticInt != 0
	if jobType.Valid {
		job.JobType = jobType.String
	}
	if reviewType.Valid {
		job.ReviewType = reviewType.String
	}
	if outputPrefix.Valid {
		job.OutputPrefix = outputPrefix.String
	}
	if patchID.Valid {
		job.PatchID = patchID.String
	}
	job.EnqueuedAt = parseSQLiteTime(enqueuedAt)
	job.Status = JobStatusRunning
	job.WorkerID = workerID
	job.StartedAt = &now
	return &job, nil
}

// SaveJobPrompt stores the prompt for a running job
func (db *DB) SaveJobPrompt(jobID int64, prompt string) error {
	_, err := db.Exec(`UPDATE review_jobs SET prompt = ? WHERE id = ?`, prompt, jobID)
	return err
}

// CompleteJob marks a job as done and stores the review.
// Only updates if job is still in 'running' state (respects cancellation).
// If the job has an output_prefix, it will be prepended to the output.
func (db *DB) CompleteJob(jobID int64, agent, prompt, output string) error {
	// Get machine ID and generate UUIDs before starting transaction
	// to avoid potential lock conflicts with GetMachineID's writes
	now := time.Now().Format(time.RFC3339)
	machineID, _ := db.GetMachineID()
	reviewUUID := GenerateUUID()

	// Use BEGIN IMMEDIATE to acquire write lock upfront, avoiding deadlocks
	// when concurrent goroutines (workers, sync) try to upgrade from read to write.
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
				log.Printf("jobs CompleteJob: rollback failed: %v", err)
			}
		}
	}()

	// Fetch output_prefix from job (if any)
	var outputPrefix sql.NullString
	err = conn.QueryRowContext(ctx, `SELECT output_prefix FROM review_jobs WHERE id = ?`, jobID).Scan(&outputPrefix)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	// Prepend output_prefix if present
	finalOutput := output
	if outputPrefix.Valid && outputPrefix.String != "" {
		finalOutput = outputPrefix.String + output
	}

	// Update job status only if still running (not canceled)
	result, err := conn.ExecContext(ctx, `UPDATE review_jobs SET status = 'done', finished_at = ?, updated_at = ? WHERE id = ? AND status = 'running'`, now, now, jobID)
	if err != nil {
		return err
	}

	// Check if we actually updated (job wasn't canceled)
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		// Job was canceled or in unexpected state, don't store review
		return nil
	}

	// Insert review with sync columns
	_, err = conn.ExecContext(ctx, `INSERT INTO reviews (job_id, agent, prompt, output, uuid, updated_by_machine_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		jobID, agent, prompt, finalOutput, reviewUUID, machineID, now)
	if err != nil {
		return err
	}

	_, err = conn.ExecContext(ctx, "COMMIT")
	if err != nil {
		return err
	}
	committed = true
	return nil
}

// FailJob marks a job as failed with an error message.
// Only updates if job is still in 'running' state and owned by the given worker
// (respects cancellation and prevents stale workers from failing reclaimed jobs).
// Pass empty workerID to skip the ownership check (for admin/test callers).
// Returns true if the job was actually updated (false when ownership or status
// check prevented the update).
func (db *DB) FailJob(jobID int64, workerID string, errorMsg string) (bool, error) {
	now := time.Now().Format(time.RFC3339)
	var result sql.Result
	var err error
	if workerID != "" {
		result, err = db.Exec(`UPDATE review_jobs SET status = 'failed', finished_at = ?, error = ?, updated_at = ? WHERE id = ? AND status = 'running' AND worker_id = ?`,
			now, errorMsg, now, jobID, workerID)
	} else {
		result, err = db.Exec(`UPDATE review_jobs SET status = 'failed', finished_at = ?, error = ?, updated_at = ? WHERE id = ? AND status = 'running'`,
			now, errorMsg, now, jobID)
	}
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// CancelJob marks a running or queued job as canceled
func (db *DB) CancelJob(jobID int64) error {
	now := time.Now().Format(time.RFC3339)
	result, err := db.Exec(`
		UPDATE review_jobs
		SET status = 'canceled', finished_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('queued', 'running')
	`, now, now, jobID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ReenqueueJob resets a completed, failed, or canceled job back to queued status.
// This allows manual re-running of jobs to get a fresh review.
// For done jobs, the existing review is deleted to avoid unique constraint violations.
func (db *DB) ReenqueueJob(jobID int64) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
				log.Printf("jobs ReenqueueJob: rollback failed: %v", err)
			}
		}
	}()

	// Delete any existing review for this job (for done jobs being rerun)
	_, err = conn.ExecContext(ctx, `DELETE FROM reviews WHERE job_id = ?`, jobID)
	if err != nil {
		return err
	}

	// Reset job status
	result, err := conn.ExecContext(ctx, `
		UPDATE review_jobs
		SET status = 'queued', worker_id = NULL, started_at = NULL, finished_at = NULL, error = NULL, retry_count = 0
		WHERE id = ? AND status IN ('done', 'failed', 'canceled')
	`, jobID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}

	_, err = conn.ExecContext(ctx, "COMMIT")
	if err != nil {
		return err
	}
	committed = true
	return nil
}

// RetryJob requeues a running job for retry if retry_count < maxRetries.
// When workerID is non-empty the update is scoped to the owning worker,
// preventing a stale/zombie worker from requeuing a reclaimed job.
// Pass empty workerID to skip the ownership check (for admin/test callers).
func (db *DB) RetryJob(jobID int64, workerID string, maxRetries int) (bool, error) {
	var result sql.Result
	var err error
	if workerID != "" {
		result, err = db.Exec(`
			UPDATE review_jobs
			SET status = 'queued', worker_id = NULL, started_at = NULL, finished_at = NULL, error = NULL, retry_count = retry_count + 1
			WHERE id = ? AND retry_count < ? AND status = 'running' AND worker_id = ?
		`, jobID, maxRetries, workerID)
	} else {
		result, err = db.Exec(`
			UPDATE review_jobs
			SET status = 'queued', worker_id = NULL, started_at = NULL, finished_at = NULL, error = NULL, retry_count = retry_count + 1
			WHERE id = ? AND retry_count < ? AND status = 'running'
		`, jobID, maxRetries)
	}
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return rows > 0, nil
}

// FailoverJob atomically switches a running job to the given backup agent
// and requeues it. Returns false if the job is not in running state, the
// worker doesn't own the job, or the backup agent is the same as the
// current agent.
func (db *DB) FailoverJob(jobID int64, workerID string, backupAgent string) (bool, error) {
	if backupAgent == "" {
		return false, nil
	}
	result, err := db.Exec(`
		UPDATE review_jobs
		SET agent = ?,
		    model = NULL,
		    retry_count = 0,
		    status = 'queued',
		    worker_id = NULL,
		    started_at = NULL,
		    finished_at = NULL,
		    error = NULL
		WHERE id = ?
		  AND status = 'running'
		  AND worker_id = ?
		  AND agent != ?
	`, backupAgent, jobID, workerID, backupAgent)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

// GetJobRetryCount returns the retry count for a job
func (db *DB) GetJobRetryCount(jobID int64) (int, error) {
	var count int
	err := db.QueryRow(`SELECT retry_count FROM review_jobs WHERE id = ?`, jobID).Scan(&count)
	return count, err
}

// ListJobsOption configures optional filters for ListJobs.
type ListJobsOption func(*listJobsOptions)

type listJobsOptions struct {
	gitRef             string
	branch             string
	branchIncludeEmpty bool
	addressed          *bool
}

// WithGitRef filters jobs by git ref.
func WithGitRef(ref string) ListJobsOption {
	return func(o *listJobsOptions) { o.gitRef = ref }
}

// WithBranch filters jobs by exact branch name.
func WithBranch(branch string) ListJobsOption {
	return func(o *listJobsOptions) { o.branch = branch }
}

// WithBranchOrEmpty filters jobs by branch name, also including jobs
// with no branch set (empty string or NULL).
func WithBranchOrEmpty(branch string) ListJobsOption {
	return func(o *listJobsOptions) {
		o.branch = branch
		o.branchIncludeEmpty = true
	}
}

// WithAddressed filters jobs by addressed state (true/false).
func WithAddressed(addressed bool) ListJobsOption {
	return func(o *listJobsOptions) { o.addressed = &addressed }
}

// ListJobs returns jobs with optional status, repo, branch, and addressed filters.
// addressedFilter: nil = no filter, non-nil bool = filter by addressed state.
func (db *DB) ListJobs(statusFilter string, repoFilter string, limit, offset int, opts ...ListJobsOption) ([]ReviewJob, error) {
	query := `
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.branch, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error, j.prompt, j.retry_count,
		       COALESCE(j.agentic, 0), r.root_path, r.name, c.subject, rv.addressed, rv.output,
		       j.source_machine_id, j.uuid, j.model, j.job_type, j.review_type, j.patch_id
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		LEFT JOIN reviews rv ON rv.job_id = j.id
	`
	var args []any
	var conditions []string

	if statusFilter != "" {
		conditions = append(conditions, "j.status = ?")
		args = append(args, statusFilter)
	}
	if repoFilter != "" {
		conditions = append(conditions, "r.root_path = ?")
		args = append(args, repoFilter)
	}
	var o listJobsOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.gitRef != "" {
		conditions = append(conditions, "j.git_ref = ?")
		args = append(args, o.gitRef)
	}
	if o.branch != "" {
		if o.branchIncludeEmpty {
			conditions = append(conditions, "(j.branch = ? OR j.branch = '' OR j.branch IS NULL)")
		} else {
			conditions = append(conditions, "j.branch = ?")
		}
		args = append(args, o.branch)
	}
	if o.addressed != nil {
		if *o.addressed {
			conditions = append(conditions, "rv.addressed = 1")
		} else {
			conditions = append(conditions, "(rv.addressed IS NULL OR rv.addressed = 0)")
		}
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY j.id DESC"

	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
		// OFFSET requires LIMIT in SQLite
		if offset > 0 {
			query += " OFFSET ?"
			args = append(args, offset)
		}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []ReviewJob
	for rows.Next() {
		var j ReviewJob
		var enqueuedAt string
		var startedAt, finishedAt, workerID, errMsg, prompt, output, sourceMachineID, jobUUID, model, branch, jobTypeStr, reviewTypeStr, patchIDStr sql.NullString
		var commitID sql.NullInt64
		var commitSubject sql.NullString
		var addressed sql.NullInt64
		var agentic int

		err := rows.Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &branch, &j.Agent, &j.Reasoning, &j.Status, &enqueuedAt,
			&startedAt, &finishedAt, &workerID, &errMsg, &prompt, &j.RetryCount,
			&agentic, &j.RepoPath, &j.RepoName, &commitSubject, &addressed, &output,
			&sourceMachineID, &jobUUID, &model, &jobTypeStr, &reviewTypeStr, &patchIDStr)
		if err != nil {
			return nil, err
		}

		if jobUUID.Valid {
			j.UUID = jobUUID.String
		}
		if commitID.Valid {
			j.CommitID = &commitID.Int64
		}
		if commitSubject.Valid {
			j.CommitSubject = commitSubject.String
		}
		j.Agentic = agentic != 0
		j.EnqueuedAt = parseSQLiteTime(enqueuedAt)
		if startedAt.Valid {
			t := parseSQLiteTime(startedAt.String)
			j.StartedAt = &t
		}
		if finishedAt.Valid {
			t := parseSQLiteTime(finishedAt.String)
			j.FinishedAt = &t
		}
		if workerID.Valid {
			j.WorkerID = workerID.String
		}
		if errMsg.Valid {
			j.Error = errMsg.String
		}
		if prompt.Valid {
			j.Prompt = prompt.String
		}
		if sourceMachineID.Valid {
			j.SourceMachineID = sourceMachineID.String
		}
		if model.Valid {
			j.Model = model.String
		}
		if jobTypeStr.Valid {
			j.JobType = jobTypeStr.String
		}
		if reviewTypeStr.Valid {
			j.ReviewType = reviewTypeStr.String
		}
		if patchIDStr.Valid {
			j.PatchID = patchIDStr.String
		}
		if branch.Valid {
			j.Branch = branch.String
		}
		if addressed.Valid {
			val := addressed.Int64 != 0
			j.Addressed = &val
		}
		// Compute verdict only for non-task jobs (task jobs don't have PASS/FAIL verdicts)
		// Task jobs (run, analyze, custom) are identified by having no commit_id and not being dirty
		if output.Valid && !j.IsTaskJob() {
			verdict := ParseVerdict(output.String)
			j.Verdict = &verdict
		}

		jobs = append(jobs, j)
	}

	return jobs, rows.Err()
}

// GetJobByID returns a job by ID with joined fields
// JobStats holds aggregate counts for the queue status line.
type JobStats struct {
	Done        int `json:"done"`
	Addressed   int `json:"addressed"`
	Unaddressed int `json:"unaddressed"`
}

// CountJobStats returns aggregate done/addressed/unaddressed counts
// using the same filter logic as ListJobs (repo, branch, addressed).
func (db *DB) CountJobStats(repoFilter string, opts ...ListJobsOption) (JobStats, error) {
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN j.status = 'done' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN j.status = 'done' AND rv.addressed = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN j.status = 'done' AND (rv.addressed IS NULL OR rv.addressed = 0) THEN 1 ELSE 0 END), 0)
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN reviews rv ON rv.job_id = j.id
	`
	var args []any
	var conditions []string

	if repoFilter != "" {
		conditions = append(conditions, "r.root_path = ?")
		args = append(args, repoFilter)
	}
	var o listJobsOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.branch != "" {
		if o.branchIncludeEmpty {
			conditions = append(conditions, "(j.branch = ? OR j.branch = '' OR j.branch IS NULL)")
		} else {
			conditions = append(conditions, "j.branch = ?")
		}
		args = append(args, o.branch)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	var stats JobStats
	err := db.QueryRow(query, args...).Scan(&stats.Done, &stats.Addressed, &stats.Unaddressed)
	return stats, err
}

func (db *DB) GetJobByID(id int64) (*ReviewJob, error) {
	var j ReviewJob
	var enqueuedAt string
	var startedAt, finishedAt, workerID, errMsg, prompt sql.NullString
	var commitID sql.NullInt64
	var commitSubject sql.NullString
	var agentic int

	var model, branch, jobTypeStr, reviewTypeStr, patchIDStr sql.NullString
	err := db.QueryRow(`
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.branch, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error, j.prompt, COALESCE(j.agentic, 0),
		       r.root_path, r.name, c.subject, j.model, j.job_type, j.review_type, j.patch_id
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.id = ?
	`, id).Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &branch, &j.Agent, &j.Reasoning, &j.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg, &prompt, &agentic,
		&j.RepoPath, &j.RepoName, &commitSubject, &model, &jobTypeStr, &reviewTypeStr, &patchIDStr)
	if err != nil {
		return nil, err
	}

	if commitID.Valid {
		j.CommitID = &commitID.Int64
	}
	if commitSubject.Valid {
		j.CommitSubject = commitSubject.String
	}
	j.Agentic = agentic != 0
	j.EnqueuedAt = parseSQLiteTime(enqueuedAt)
	if startedAt.Valid {
		t := parseSQLiteTime(startedAt.String)
		j.StartedAt = &t
	}
	if finishedAt.Valid {
		t := parseSQLiteTime(finishedAt.String)
		j.FinishedAt = &t
	}
	if workerID.Valid {
		j.WorkerID = workerID.String
	}
	if errMsg.Valid {
		j.Error = errMsg.String
	}
	if prompt.Valid {
		j.Prompt = prompt.String
	}
	if model.Valid {
		j.Model = model.String
	}
	if jobTypeStr.Valid {
		j.JobType = jobTypeStr.String
	}
	if reviewTypeStr.Valid {
		j.ReviewType = reviewTypeStr.String
	}
	if patchIDStr.Valid {
		j.PatchID = patchIDStr.String
	}
	if branch.Valid {
		j.Branch = branch.String
	}

	return &j, nil
}

// GetJobCounts returns counts of jobs by status
func (db *DB) GetJobCounts() (queued, running, done, failed, canceled int, err error) {
	rows, err := db.Query(`SELECT status, COUNT(*) FROM review_jobs GROUP BY status`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err = rows.Scan(&status, &count); err != nil {
			return
		}
		switch JobStatus(status) {
		case JobStatusQueued:
			queued = count
		case JobStatusRunning:
			running = count
		case JobStatusDone:
			done = count
		case JobStatusFailed:
			failed = count
		case JobStatusCanceled:
			canceled = count
		}
	}
	err = rows.Err()
	return
}

// UpdateJobBranch sets the branch field for a job that doesn't have one.
// This is used to backfill the branch when it's derived from git.
// Only updates if the current branch is NULL or empty.
// Returns the number of rows affected (0 if branch was already set or job not found, 1 if updated).
func (db *DB) UpdateJobBranch(jobID int64, branch string) (int64, error) {
	result, err := db.Exec(`UPDATE review_jobs SET branch = ? WHERE id = ? AND (branch IS NULL OR branch = '')`, branch, jobID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// RemapJobGitRef updates git_ref and commit_id for jobs matching
// oldSHA in a repo, used after rebases to preserve review history.
// If a job has a stored patch_id that differs from the provided one,
// that job is skipped (the commit's content changed).
// Returns the number of rows updated.
func (db *DB) RemapJobGitRef(
	repoID int64, oldSHA, newSHA, patchID string, newCommitID int64,
) (int, error) {
	now := time.Now().Format(time.RFC3339)
	result, err := db.Exec(`
		UPDATE review_jobs
		SET git_ref = ?, commit_id = ?, patch_id = ?, updated_at = ?
		WHERE git_ref = ? AND repo_id = ?
		AND status != 'running'
		AND (patch_id IS NULL OR patch_id = '' OR patch_id = ?)
	`, newSHA, newCommitID, nullString(patchID), now, oldSHA, repoID, patchID)
	if err != nil {
		return 0, fmt.Errorf("remap job git_ref: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// RemapJob atomically checks for matching jobs, creates the commit
// row, and updates git_ref — all in a single transaction to prevent
// orphan commit rows or races between concurrent remaps.
func (db *DB) RemapJob(
	repoID int64, oldSHA, newSHA, patchID string,
	author, subject string, timestamp time.Time,
) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin remap tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var matchCount int
	err = tx.QueryRow(`
		SELECT COUNT(*) FROM review_jobs
		WHERE git_ref = ? AND repo_id = ?
		AND status != 'running'
		AND (patch_id IS NULL OR patch_id = '' OR patch_id = ?)
	`, oldSHA, repoID, patchID).Scan(&matchCount)
	if err != nil {
		return 0, fmt.Errorf("count matching jobs: %w", err)
	}
	if matchCount == 0 {
		return 0, nil
	}

	// Create or find commit row for the new SHA
	var commitID int64
	err = tx.QueryRow(
		`SELECT id FROM commits WHERE repo_id = ? AND sha = ?`,
		repoID, newSHA,
	).Scan(&commitID)
	if err == sql.ErrNoRows {
		result, insertErr := tx.Exec(`
			INSERT INTO commits (repo_id, sha, author, subject, timestamp)
			VALUES (?, ?, ?, ?, ?)
		`, repoID, newSHA, author, subject,
			timestamp.Format(time.RFC3339))
		if insertErr != nil {
			return 0, fmt.Errorf("create commit: %w", insertErr)
		}
		commitID, _ = result.LastInsertId()
	} else if err != nil {
		return 0, fmt.Errorf("find commit: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	result, err := tx.Exec(`
		UPDATE review_jobs
		SET git_ref = ?, commit_id = ?, patch_id = ?, updated_at = ?
		WHERE git_ref = ? AND repo_id = ?
		AND status != 'running'
		AND (patch_id IS NULL OR patch_id = '' OR patch_id = ?)
	`, newSHA, commitID, nullString(patchID), now,
		oldSHA, repoID, patchID)
	if err != nil {
		return 0, fmt.Errorf("remap job git_ref: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit remap tx: %w", err)
	}
	return int(n), nil
}
