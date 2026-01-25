package storage

import (
	"database/sql"
	"strings"
	"time"
)

// ParseVerdict extracts P (pass) or F (fail) from review output.
// Returns "P" only if a clear pass indicator appears at the start of a line.
// Rejects lines containing caveats like "but", "however", "except".
func ParseVerdict(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.ToLower(line))
		// Normalize curly apostrophes to straight apostrophes (LLMs sometimes use these)
		trimmed = strings.ReplaceAll(trimmed, "\u2018", "'") // left single quote
		trimmed = strings.ReplaceAll(trimmed, "\u2019", "'") // right single quote
		// Strip markdown formatting (bold, italic, headers)
		trimmed = stripMarkdown(trimmed)
		// Strip leading list markers (bullets, numbers, etc.)
		trimmed = stripListMarker(trimmed)

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

	clauses := strings.Split(normalized, "|")
	for _, clause := range clauses {
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
			start := idx - 30
			if start < 0 {
				start = 0
			}
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
			start := idx - 30
			if start < 0 {
				start = 0
			}
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
		if idx := strings.Index(lc, " still "); idx >= 0 {
			afterStill := lc[idx+7:]
			stillKeywords := []string{"crash", "panic", "fail", "break", "error", "bug"}
			for _, kw := range stillKeywords {
				if strings.HasPrefix(afterStill, kw) || strings.Contains(afterStill, " "+kw) {
					return true
				}
			}
		}

		// Check for contrastive markers with issue words AFTER the marker
		for _, marker := range []string{" however ", " but "} {
			if idx := strings.Index(lc, marker); idx >= 0 {
				tail := lc[idx+len(marker):]
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
		if w == "but" || w == "however" || w == "except" {
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
		for _, neg := range negators {
			if w == neg {
				return true
			}
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
		if prev == "not" {
			for _, v := range verbs {
				if w == v {
					return true
				}
			}
		}
		// Check for contraction followed by verb (e.g., "can't find", "couldn't see")
		if contractions[prev] {
			for _, v := range verbs {
				if w == v {
					return true
				}
			}
		}
		checked++
	}
	return false
}

// parseSQLiteTime parses a time string from SQLite which may be in different formats
func parseSQLiteTime(s string) time.Time {
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
	return time.Time{}
}

// EnqueueJob creates a new review job for a single commit
func (db *DB) EnqueueJob(repoID, commitID int64, gitRef, agent, reasoning string) (*ReviewJob, error) {
	if reasoning == "" {
		reasoning = "thorough"
	}
	uuid := GenerateUUID()
	machineID, _ := db.GetMachineID()
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	result, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, reasoning, status, uuid, source_machine_id, updated_at) VALUES (?, ?, ?, ?, ?, 'queued', ?, ?, ?)`,
		repoID, commitID, gitRef, agent, reasoning, uuid, machineID, nowStr)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &ReviewJob{
		ID:              id,
		RepoID:          repoID,
		CommitID:        &commitID,
		GitRef:          gitRef,
		Agent:           agent,
		Reasoning:       reasoning,
		Status:          JobStatusQueued,
		EnqueuedAt:      now,
		UUID:            uuid,
		SourceMachineID: machineID,
		UpdatedAt:       &now,
	}, nil
}

// EnqueueRangeJob creates a new review job for a commit range
func (db *DB) EnqueueRangeJob(repoID int64, gitRef, agent, reasoning string) (*ReviewJob, error) {
	if reasoning == "" {
		reasoning = "thorough"
	}
	uuid := GenerateUUID()
	machineID, _ := db.GetMachineID()
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	result, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, reasoning, status, uuid, source_machine_id, updated_at) VALUES (?, NULL, ?, ?, ?, 'queued', ?, ?, ?)`,
		repoID, gitRef, agent, reasoning, uuid, machineID, nowStr)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &ReviewJob{
		ID:              id,
		RepoID:          repoID,
		CommitID:        nil,
		GitRef:          gitRef,
		Agent:           agent,
		Reasoning:       reasoning,
		Status:          JobStatusQueued,
		EnqueuedAt:      now,
		UUID:            uuid,
		SourceMachineID: machineID,
		UpdatedAt:       &now,
	}, nil
}

// EnqueueDirtyJob creates a new review job for uncommitted (dirty) changes.
// The diff is captured at enqueue time since the working tree may change.
func (db *DB) EnqueueDirtyJob(repoID int64, gitRef, agent, reasoning, diffContent string) (*ReviewJob, error) {
	if reasoning == "" {
		reasoning = "thorough"
	}
	uuid := GenerateUUID()
	machineID, _ := db.GetMachineID()
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	result, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, reasoning, status, diff_content, uuid, source_machine_id, updated_at) VALUES (?, NULL, ?, ?, ?, 'queued', ?, ?, ?, ?)`,
		repoID, gitRef, agent, reasoning, diffContent, uuid, machineID, nowStr)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &ReviewJob{
		ID:              id,
		RepoID:          repoID,
		CommitID:        nil,
		GitRef:          gitRef,
		Agent:           agent,
		Reasoning:       reasoning,
		Status:          JobStatusQueued,
		EnqueuedAt:      now,
		DiffContent:     &diffContent,
		UUID:            uuid,
		SourceMachineID: machineID,
		UpdatedAt:       &now,
	}, nil
}

// EnqueuePromptJob creates a new job with a custom prompt (not a git review).
// The prompt is stored at enqueue time and used directly by the worker.
// If agentic is true, the agent will be allowed to edit files and run commands.
func (db *DB) EnqueuePromptJob(repoID int64, agent, reasoning, customPrompt string, agentic bool) (*ReviewJob, error) {
	if reasoning == "" {
		reasoning = "thorough"
	}
	agenticInt := 0
	if agentic {
		agenticInt = 1
	}
	uuid := GenerateUUID()
	machineID, _ := db.GetMachineID()
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	result, err := db.Exec(`INSERT INTO review_jobs (repo_id, commit_id, git_ref, agent, reasoning, status, prompt, agentic, uuid, source_machine_id, updated_at) VALUES (?, NULL, 'prompt', ?, ?, 'queued', ?, ?, ?, ?, ?)`,
		repoID, agent, reasoning, customPrompt, agenticInt, uuid, machineID, nowStr)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &ReviewJob{
		ID:              id,
		RepoID:          repoID,
		CommitID:        nil,
		GitRef:          "prompt",
		Agent:           agent,
		Reasoning:       reasoning,
		Status:          JobStatusQueued,
		EnqueuedAt:      now,
		Prompt:          customPrompt,
		Agentic:         agentic,
		UUID:            uuid,
		SourceMachineID: machineID,
		UpdatedAt:       &now,
	}, nil
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
	var agenticInt int
	err = db.QueryRow(`
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.reasoning, j.status, j.enqueued_at,
		       r.root_path, r.name, c.subject, j.diff_content, j.prompt, COALESCE(j.agentic, 0)
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.worker_id = ? AND j.status = 'running'
		ORDER BY j.started_at DESC
		LIMIT 1
	`, workerID).Scan(&job.ID, &job.RepoID, &commitID, &job.GitRef, &job.Agent, &job.Reasoning, &job.Status, &enqueuedAt,
		&job.RepoPath, &job.RepoName, &commitSubject, &diffContent, &prompt, &agenticInt)
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
	job.Agentic = agenticInt != 0
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
func (db *DB) CompleteJob(jobID int64, agent, prompt, output string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Format(time.RFC3339)
	machineID, _ := db.GetMachineID()
	reviewUUID := GenerateUUID()

	// Update job status only if still running (not canceled)
	result, err := tx.Exec(`UPDATE review_jobs SET status = 'done', finished_at = ?, updated_at = ? WHERE id = ? AND status = 'running'`, now, now, jobID)
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
	_, err = tx.Exec(`INSERT INTO reviews (job_id, agent, prompt, output, uuid, updated_by_machine_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		jobID, agent, prompt, output, reviewUUID, machineID, now)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// FailJob marks a job as failed with an error message.
// Only updates if job is still in 'running' state (respects cancellation).
func (db *DB) FailJob(jobID int64, errorMsg string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE review_jobs SET status = 'failed', finished_at = ?, error = ?, updated_at = ? WHERE id = ? AND status = 'running'`,
		now, errorMsg, now, jobID)
	return err
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
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete any existing review for this job (for done jobs being rerun)
	_, err = tx.Exec(`DELETE FROM reviews WHERE job_id = ?`, jobID)
	if err != nil {
		return err
	}

	// Reset job status
	result, err := tx.Exec(`
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

	return tx.Commit()
}

// RetryJob atomically resets a running job to queued for retry.
// Returns false if max retries reached or job is not in running state.
// maxRetries is the number of retries allowed (e.g., 3 means up to 4 total attempts).
func (db *DB) RetryJob(jobID int64, maxRetries int) (bool, error) {
	// Atomically update only if retry_count < maxRetries and status is running
	// This prevents race conditions with multiple workers
	result, err := db.Exec(`
		UPDATE review_jobs
		SET status = 'queued', worker_id = NULL, started_at = NULL, finished_at = NULL, error = NULL, retry_count = retry_count + 1
		WHERE id = ? AND retry_count < ? AND status = 'running'
	`, jobID, maxRetries)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return rows > 0, nil
}

// GetJobRetryCount returns the retry count for a job
func (db *DB) GetJobRetryCount(jobID int64) (int, error) {
	var count int
	err := db.QueryRow(`SELECT retry_count FROM review_jobs WHERE id = ?`, jobID).Scan(&count)
	return count, err
}

// ListJobs returns jobs with optional status and repo filters
func (db *DB) ListJobs(statusFilter string, repoFilter string, limit, offset int, gitRefFilter ...string) ([]ReviewJob, error) {
	query := `
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error, j.prompt, j.retry_count,
		       COALESCE(j.agentic, 0), r.root_path, r.name, c.subject, rv.addressed, rv.output,
		       j.source_machine_id, j.uuid
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		LEFT JOIN reviews rv ON rv.job_id = j.id
	`
	var args []interface{}
	var conditions []string

	if statusFilter != "" {
		conditions = append(conditions, "j.status = ?")
		args = append(args, statusFilter)
	}
	if repoFilter != "" {
		conditions = append(conditions, "r.root_path = ?")
		args = append(args, repoFilter)
	}
	if len(gitRefFilter) > 0 && gitRefFilter[0] != "" {
		conditions = append(conditions, "j.git_ref = ?")
		args = append(args, gitRefFilter[0])
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
		var startedAt, finishedAt, workerID, errMsg, prompt, output, sourceMachineID, jobUUID sql.NullString
		var commitID sql.NullInt64
		var commitSubject sql.NullString
		var addressed sql.NullInt64
		var agentic int

		err := rows.Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &j.Agent, &j.Reasoning, &j.Status, &enqueuedAt,
			&startedAt, &finishedAt, &workerID, &errMsg, &prompt, &j.RetryCount,
			&agentic, &j.RepoPath, &j.RepoName, &commitSubject, &addressed, &output,
			&sourceMachineID, &jobUUID)
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
			t, _ := time.Parse(time.RFC3339, startedAt.String)
			j.StartedAt = &t
		}
		if finishedAt.Valid {
			t, _ := time.Parse(time.RFC3339, finishedAt.String)
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
		if addressed.Valid {
			val := addressed.Int64 != 0
			j.Addressed = &val
		}
		// Compute verdict only for non-prompt jobs (prompt jobs don't have PASS/FAIL verdicts)
		// Prompt jobs are identified by having no commit_id (NULL) - this distinguishes them from
		// regular reviews of branches/commits that might be named "prompt"
		isPromptJob := j.CommitID == nil && j.GitRef == "prompt"
		if output.Valid && !isPromptJob {
			verdict := ParseVerdict(output.String)
			j.Verdict = &verdict
		}

		jobs = append(jobs, j)
	}

	return jobs, rows.Err()
}

// GetJobByID returns a job by ID with joined fields
func (db *DB) GetJobByID(id int64) (*ReviewJob, error) {
	var j ReviewJob
	var enqueuedAt string
	var startedAt, finishedAt, workerID, errMsg, prompt sql.NullString
	var commitID sql.NullInt64
	var commitSubject sql.NullString
	var agentic int

	err := db.QueryRow(`
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error, j.prompt, COALESCE(j.agentic, 0),
		       r.root_path, r.name, c.subject
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.id = ?
	`, id).Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &j.Agent, &j.Reasoning, &j.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg, &prompt, &agentic,
		&j.RepoPath, &j.RepoName, &commitSubject)
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
		t, _ := time.Parse(time.RFC3339, startedAt.String)
		j.StartedAt = &t
	}
	if finishedAt.Valid {
		t, _ := time.Parse(time.RFC3339, finishedAt.String)
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
