package prompt

import (
	"strings"
	"testing"
	"time"
)

func TestBuildInsightsPrompt_Basic(t *testing.T) {
	now := time.Now()
	finished := now.Add(-1 * time.Hour)
	data := InsightsData{
		Reviews: []InsightsReview{
			{
				JobID:      1,
				Agent:      "codex",
				GitRef:     "abc1234",
				Branch:     "main",
				FinishedAt: &finished,
				Output:     "Found SQL injection in handler.go:42",
				Closed:     false,
				Verdict:    "F",
			},
			{
				JobID:      2,
				Agent:      "gemini",
				GitRef:     "def5678",
				FinishedAt: &finished,
				Output:     "Missing error handling in parser.go:10",
				Closed:     true,
				Verdict:    "F",
			},
		},
		Guidelines: "Always check error returns",
		RepoName:   "myrepo",
		Since:      now.Add(-30 * 24 * time.Hour),
	}

	result := BuildInsightsPrompt(data)

	// Should contain the system prompt
	if !strings.Contains(result, "code review insights analyst") {
		t.Error("missing system prompt")
	}

	// Should contain guidelines
	if !strings.Contains(result, "Always check error returns") {
		t.Error("missing guidelines")
	}

	// Should contain review data
	if !strings.Contains(result, "SQL injection") {
		t.Error("missing review 1 output")
	}
	if !strings.Contains(result, "Missing error handling") {
		t.Error("missing review 2 output")
	}

	// Should show addressed status
	if !strings.Contains(result, "unaddressed") {
		t.Error("missing unaddressed status")
	}
	if !strings.Contains(result, "addressed/closed") {
		t.Error("missing addressed status")
	}

	// Should contain agents
	if !strings.Contains(result, "codex") {
		t.Error("missing agent name")
	}
}

func TestBuildInsightsPrompt_NoGuidelines(t *testing.T) {
	data := InsightsData{
		Reviews: []InsightsReview{
			{JobID: 1, Agent: "test", Output: "finding"},
		},
		Since: time.Now().Add(-7 * 24 * time.Hour),
	}

	result := BuildInsightsPrompt(data)

	if !strings.Contains(result, "No review guidelines configured") {
		t.Error("should indicate no guidelines")
	}
}

func TestBuildInsightsPrompt_PrioritizesOpen(t *testing.T) {
	data := InsightsData{
		MaxReviews: 2,
		Reviews: []InsightsReview{
			{JobID: 1, Agent: "test", Output: "open finding 1", Closed: false},
			{JobID: 2, Agent: "test", Output: "open finding 2", Closed: false},
			{JobID: 3, Agent: "test", Output: "open finding 3", Closed: false},
			{JobID: 4, Agent: "test", Output: "closed finding", Closed: true},
		},
		Since: time.Now().Add(-7 * 24 * time.Hour),
	}

	result := BuildInsightsPrompt(data)

	// Should include open findings
	if !strings.Contains(result, "open finding 1") {
		t.Error("missing open finding 1")
	}
	if !strings.Contains(result, "open finding 2") {
		t.Error("missing open finding 2")
	}

	// Should NOT include closed finding (exceeded cap)
	if strings.Contains(result, "closed finding") {
		t.Error("should not include closed finding when open reviews fill cap")
	}

	// Should note omitted reviews
	if !strings.Contains(result, "additional failing reviews were omitted") {
		t.Error("should mention omitted reviews")
	}
}

func TestBuildInsightsPrompt_TruncatesLongOutput(t *testing.T) {
	longOutput := strings.Repeat("x", 10000)
	data := InsightsData{
		Reviews: []InsightsReview{
			{JobID: 1, Agent: "test", Output: longOutput},
		},
		MaxOutputPerReview: 100,
		Since:              time.Now().Add(-7 * 24 * time.Hour),
	}

	result := BuildInsightsPrompt(data)

	if !strings.Contains(result, "... (truncated)") {
		t.Error("should truncate long output")
	}

	// Should not contain the full output
	if strings.Contains(result, longOutput) {
		t.Error("should not contain full long output")
	}
}

func TestBuildInsightsPrompt_Empty(t *testing.T) {
	data := InsightsData{
		Since: time.Now().Add(-7 * 24 * time.Hour),
	}

	result := BuildInsightsPrompt(data)

	if !strings.Contains(result, "0 failing review(s)") {
		t.Error("should show 0 reviews")
	}
}

func TestBuildInsightsPrompt_RespectsPromptBudget(t *testing.T) {
	// Create reviews that would exceed a small budget
	var reviews []InsightsReview
	for i := range 20 {
		reviews = append(reviews, InsightsReview{
			JobID:  int64(i + 1),
			Agent:  "test",
			Output: strings.Repeat("finding text ", 100), // ~1300 bytes each
		})
	}

	budget := 5000
	data := InsightsData{
		Reviews:       reviews,
		Since:         time.Now().Add(-7 * 24 * time.Hour),
		MaxPromptSize: budget,
	}

	result := BuildInsightsPrompt(data)

	if len(result) > budget {
		t.Errorf("prompt size %d exceeds budget %d", len(result), budget)
	}

	// Should mention omitted reviews
	if !strings.Contains(result, "omitted due to prompt size limits") {
		t.Error("should mention size-limited omissions")
	}
}
