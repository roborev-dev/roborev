package storage

import (
	"database/sql"
	"strings"

	"github.com/roborev-dev/roborev/internal/config"
)

const (
	verdictPass = "P"
	verdictFail = "F"
)

// verdictToBool converts a ParseVerdict result ("P"/"F") to an integer
// for storage in the verdict_bool column (1=pass, 0=fail).
func verdictToBool(verdict string) int {
	if verdict == verdictPass {
		return 1
	}
	return 0
}

// verdictFromBoolOrParse returns the verdict string from a stored verdict_bool
// value. If the value is NULL (legacy row), falls back to ParseVerdict(output).
func verdictFromBoolOrParse(vb sql.NullInt64, output string) string {
	if vb.Valid {
		if vb.Int64 == 1 {
			return verdictPass
		}
		return verdictFail
	}
	return ParseVerdict(output)
}

func applyReviewVerdict(review *Review, verdictBool sql.NullInt64) {
	if verdictBool.Valid {
		v := int(verdictBool.Int64)
		review.VerdictBool = &v
	}
}

func applyJobVerdict(job *ReviewJob, verdictBool sql.NullInt64, output string) {
	if output == "" || job.Error != "" || job.IsTaskJob() {
		return
	}
	verdict := verdictFromBoolOrParse(verdictBool, output)
	job.Verdict = &verdict
}

// ParseVerdict extracts P (pass) or F (fail) from review output.
// It intentionally uses a small set of deterministic signals:
// clear severity/findings markers mean fail, and clear pass phrases mean pass.
// We do not try to interpret narrative caveats after "No issues found." because
// that quickly turns into a brittle natural-language parser. If agent output is
// too chatty or mixes process narration with findings, that should be fixed in
// the review prompt rather than by adding more verdict heuristics here.
func ParseVerdict(output string) string {
	// First check for severity labels which indicate actual findings
	// These appear as "- Medium —", "* Low:", "Critical -", etc.
	if hasSeverityLabel(output) {
		return verdictFail
	}

	// Marker signals pass ONLY when it stands alone. A loose
	// substring check would let prose findings without severity
	// labels (e.g. "the auth module leaks tokens") flip to pass
	// just because the agent echoed the marker in narration.
	if config.IsMarkerOnlyOutput(output) {
		return verdictPass
	}

	for line := range strings.SplitSeq(output, "\n") {
		normalized := normalizeVerdictLine(line)
		// Historical reviews sometimes include a stale "Verdict: Fail" header
		// before a later "No issues found." summary. Preserve the existing rule
		// that a later clear pass phrase wins over contradictory narration.
		if isExplicitVerdictValue(normalized, "pass") {
			return verdictPass
		}
		if !hasPassPrefix(normalized) {
			continue
		}
		return verdictPass
	}
	return verdictFail
}

func normalizeVerdictLine(line string) string {
	normalized := strings.TrimSpace(strings.ToLower(line))
	// Normalize curly apostrophes to straight apostrophes (LLMs sometimes use these)
	normalized = strings.ReplaceAll(normalized, "\u2018", "'") // left single quote
	normalized = strings.ReplaceAll(normalized, "\u2019", "'") // right single quote
	normalized = stripMarkdown(normalized)
	normalized = stripListMarker(normalized)
	return stripFieldLabel(normalized)
}

func hasPassPrefix(line string) bool {
	passPrefixes := []string{
		"no issues",
		"no findings",
		"i didn't find any issues",
		"i did not find any issues",
		"i found no issues",
	}
	for _, prefix := range passPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func isExplicitVerdictValue(line, value string) bool {
	return line == value
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

// classifySeverityLine inspects a single line in context and returns the bucket
// it belongs to: "critical", "high", "medium", "low", "info", or "" if the
// line is not a severity-tagged finding. Mirrors the recognition rules
// previously inlined in hasSeverityLabel; "info" is added so CountFindings
// can fold info-level mentions into the low bucket.
func classifySeverityLine(lines []string, i int) string {
	trimmed := strings.TrimSpace(lines[i])
	if trimmed == "" {
		return ""
	}

	// Strip leading bullet/number markers
	first := trimmed[0]
	hasBullet := first == '-' || first == '*' || (first >= '0' && first <= '9') ||
		strings.HasPrefix(trimmed, "•")

	checkText := trimmed
	if hasBullet {
		checkText = strings.TrimLeft(trimmed, "-*•0123456789.) ")
		checkText = strings.TrimSpace(checkText)
	}
	checkText = stripMarkdown(checkText)

	// "info" is included so CountFindings can collapse it into the low bucket.
	// hasSeverityLabel filters it out separately to preserve verdict semantics.
	severities := []string{"critical", "high", "medium", "low", "info"}
	for _, sev := range severities {
		if !strings.HasPrefix(checkText, sev) {
			continue
		}
		rest := strings.TrimSpace(checkText[len(sev):])
		if len(rest) == 0 {
			continue
		}
		hasValidSep := strings.HasPrefix(rest, "—") || strings.HasPrefix(rest, "–") ||
			rest[0] == ':' || rest[0] == '|' ||
			(rest[0] == '-' && len(rest) > 1 && rest[1] == ' ')
		if !hasValidSep {
			continue
		}
		if isLegendEntry(lines, i) {
			continue
		}
		return sev
	}

	// "Severity: <level>" pattern (e.g. "**Severity**: High")
	if strings.HasPrefix(checkText, "severity") {
		rest := strings.TrimSpace(checkText[len("severity"):])
		hasSep := len(rest) > 0 && (rest[0] == ':' || rest[0] == '|' ||
			strings.HasPrefix(rest, "—") || strings.HasPrefix(rest, "–"))
		if !hasSep && len(rest) > 1 && rest[0] == '-' && rest[1] == ' ' {
			hasSep = true
		}
		if hasSep {
			rest = strings.TrimLeft(rest, ":-–—| ")
			rest = strings.TrimSpace(rest)
			for _, sev := range severities {
				if strings.HasPrefix(rest, sev) && !isLegendEntry(lines, i) {
					return sev
				}
			}
		}
	}
	return ""
}

// CountFindings parses review output for severity-prefixed findings and returns
// counts in three buckets:
//
//	high   = critical + high
//	medium = medium
//	low    = low + info
//
// Reuses the same line-classification rules as hasSeverityLabel.
func CountFindings(output string) (high, medium, low int) {
	lc := strings.ToLower(output)
	lines := strings.Split(lc, "\n")
	for i := range lines {
		switch classifySeverityLine(lines, i) {
		case "critical", "high":
			high++
		case "medium":
			medium++
		case "low", "info":
			low++
		}
	}
	return high, medium, low
}

// hasSeverityLabel returns true if the output contains any severity-tagged
// finding among critical/high/medium/low. Info-only reviews are NOT treated
// as having findings, preserving the original verdict semantics where
// "Info: ..." notes do not flip a review from pass to fail.
func hasSeverityLabel(output string) bool {
	lc := strings.ToLower(output)
	lines := strings.Split(lc, "\n")
	for i := range lines {
		sev := classifySeverityLine(lines, i)
		if sev != "" && sev != "info" {
			return true
		}
	}
	return false
}

// isLegendEntry checks if a line at index i appears to be part of a severity legend/rubric
// by looking at preceding lines for legend indicators. Scans up to 10 lines back,
// skipping empty lines, severity lines, and description lines that may appear
// between legend entries. Stops at any non-legend section header (a line ending
// with ":" that does not contain a legend keyword) to avoid false positives when
// a legend block is followed by a real findings section separated by a header.
func isLegendEntry(lines []string, i int) bool {
	legendKeywords := []string{"severity", "level", "legend", "priority", "rubric", "rating", "scale"}

	for j := i - 1; j >= 0 && j >= i-10; j-- {
		prev := strings.TrimSpace(lines[j])
		if len(prev) == 0 {
			continue
		}

		// Strip markdown and list markers so bolded headers like
		// "**Severity levels:**" are recognized the same as plain text.
		prev = stripMarkdown(stripListMarker(prev))

		// Lines ending with ":" are section headers.
		if strings.HasSuffix(prev, ":") || strings.HasSuffix(prev, "：") {
			for _, kw := range legendKeywords {
				if strings.Contains(prev, kw) {
					return true
				}
			}
			// A non-legend section header (e.g. "Findings:") marks a boundary:
			// stop scanning so lines below it are not attributed to a legend above.
			return false
		}
	}
	return false
}
