package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestBuildSynthesisPrompt(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: "No issues found.", Status: "done"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Output: "Consider error handling in foo.go:42", Status: "done"},
		{JobID: 3, Agent: "codex", ReviewType: "review", Status: "failed", Error: "timeout"},
	}

	prompt := buildSynthesisPrompt(reviews)

	// Should contain instructions
	if !strings.Contains(prompt, "Deduplicate findings") {
		t.Error("prompt missing deduplication instruction")
	}
	if !strings.Contains(prompt, "Organize by severity") {
		t.Error("prompt missing severity instruction")
	}

	// Should contain review headers
	if !strings.Contains(prompt, "### Review 1: Agent=codex, Type=security") {
		t.Error("prompt missing review 1 header")
	}
	if !strings.Contains(prompt, "### Review 2: Agent=gemini, Type=review") {
		t.Error("prompt missing review 2 header")
	}
	if !strings.Contains(prompt, "[FAILED: timeout]") {
		t.Error("prompt missing failure annotation")
	}

	// Should contain review outputs
	if !strings.Contains(prompt, "No issues found.") {
		t.Error("prompt missing review 1 output")
	}
	if !strings.Contains(prompt, "foo.go:42") {
		t.Error("prompt missing review 2 output")
	}
	if !strings.Contains(prompt, "(no output") {
		t.Error("prompt missing failed review placeholder")
	}
}

func TestFormatRawBatchComment(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: "Finding A", Status: "done"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Status: "failed", Error: "timeout"},
	}

	comment := formatRawBatchComment(reviews)

	// Header
	if !strings.Contains(comment, "## roborev: Combined Review") {
		t.Error("missing header")
	}
	if !strings.Contains(comment, "Synthesis unavailable") {
		t.Error("missing synthesis unavailable note")
	}

	// Details blocks
	if !strings.Contains(comment, "<details>") {
		t.Error("missing details block")
	}
	if !strings.Contains(comment, "Agent: codex | Type: security | Status: done") {
		t.Error("missing first review summary")
	}
	if !strings.Contains(comment, "Finding A") {
		t.Error("missing first review output")
	}
	if !strings.Contains(comment, "Agent: gemini | Type: review | Status: failed") {
		t.Error("missing second review summary")
	}
	if !strings.Contains(comment, "**Error:** timeout") {
		t.Error("missing error for failed review")
	}
}

func TestFormatSynthesizedComment(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Status: "done"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Status: "done"},
	}

	output := "All clean. No critical findings."
	comment := formatSynthesizedComment(output, reviews)

	// Header
	if !strings.Contains(comment, "## roborev: Combined Review") {
		t.Error("missing header")
	}

	// Synthesized content
	if !strings.Contains(comment, "All clean. No critical findings.") {
		t.Error("missing synthesized output")
	}

	// Metadata
	if !strings.Contains(comment, "Synthesized from 2 reviews") {
		t.Error("missing metadata")
	}
	if !strings.Contains(comment, "codex") {
		t.Error("missing agent name in metadata")
	}
	if !strings.Contains(comment, "gemini") {
		t.Error("missing agent name in metadata")
	}
	if !strings.Contains(comment, "security") {
		t.Error("missing review type in metadata")
	}
	if !strings.Contains(comment, "review") {
		t.Error("missing review type in metadata")
	}
}

func TestFormatAllFailedComment(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Status: "failed", Error: "timeout"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Status: "failed", Error: "api error"},
	}

	comment := formatAllFailedComment(reviews)

	if !strings.Contains(comment, "## roborev: Review Failed") {
		t.Error("missing header")
	}
	if !strings.Contains(comment, "All review jobs in this batch failed") {
		t.Error("missing failure message")
	}
	if !strings.Contains(comment, "**codex** (security): timeout") {
		t.Error("missing first failure detail")
	}
	if !strings.Contains(comment, "**gemini** (review): api error") {
		t.Error("missing second failure detail")
	}
}

func TestGhEnv_FiltersExistingTokens(t *testing.T) {
	// Set up a CIPoller with a pre-cached token (avoids JWT/API calls)
	provider := &GitHubAppTokenProvider{
		token:   "ghs_app_token_123",
		expires: time.Now().Add(1 * time.Hour),
	}
	p := &CIPoller{tokenProvider: provider}

	// Plant GH_TOKEN and GITHUB_TOKEN in env
	t.Setenv("GH_TOKEN", "personal_token")
	t.Setenv("GITHUB_TOKEN", "another_personal_token")

	env := p.ghEnv()

	// Should contain our app token
	found := false
	for _, e := range env {
		if e == "GH_TOKEN=ghs_app_token_123" {
			found = true
		}
		if strings.HasPrefix(e, "GITHUB_TOKEN=") {
			t.Error("GITHUB_TOKEN should have been filtered out")
		}
		if strings.HasPrefix(e, "GH_TOKEN=personal_token") {
			t.Error("original GH_TOKEN should have been filtered out")
		}
	}
	if !found {
		t.Error("expected GH_TOKEN=ghs_app_token_123 in env")
	}
}

func TestGhEnv_NilProvider(t *testing.T) {
	p := &CIPoller{tokenProvider: nil}
	if env := p.ghEnv(); env != nil {
		t.Errorf("expected nil env when no token provider, got %v", env)
	}
}

func TestFormatRawBatchComment_Truncation(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: strings.Repeat("x", 20000), Status: "done"},
	}

	comment := formatRawBatchComment(reviews)
	if !strings.Contains(comment, "...(truncated)") {
		t.Error("expected truncation for large output")
	}
}
