package review

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSynthesize_AllFailed(t *testing.T) {
	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "failed",
			Error:      "agent crashed",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			HeadSHA: "abc123456789",
		})
	if !errors.Is(err, ErrAllFailed) {
		t.Fatalf(
			"expected ErrAllFailed, got: %v", err)
	}

	if !strings.Contains(comment, "Review Failed") {
		t.Error("expected 'Review Failed' in comment")
	}
}

func TestSynthesize_SingleSuccess(t *testing.T) {
	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "No issues found.",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			HeadSHA: "abc123456789",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(comment, "Review Passed") {
		t.Error("expected 'Review Passed' in comment")
	}
	if !strings.Contains(
		comment, "No issues found.") {
		t.Error("expected output in comment")
	}
	if !strings.Contains(comment, "Agent: codex") {
		t.Error("expected agent name in metadata")
	}
}

func TestSynthesize_MultipleResults_FallsBackToRaw(t *testing.T) {
	// Without a real agent for synthesis, it should fall back
	// to raw format.
	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "Found issue A",
		},
		{
			Agent:      "codex",
			ReviewType: "design",
			Status:     "done",
			Output:     "Design looks good",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			Agent:   "nonexistent-synthesis-agent",
			HeadSHA: "def456789012",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should fall back to raw format
	if !strings.Contains(
		comment, "Synthesis unavailable") {
		t.Error(
			"expected raw fallback when synthesis fails")
	}
	if !strings.Contains(
		comment, "Found issue A") {
		t.Error("expected first review output")
	}
	if !strings.Contains(
		comment, "Design looks good") {
		t.Error("expected second review output")
	}
}

func TestSynthesize_AllQuota(t *testing.T) {
	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "failed",
			Error:      QuotaErrorPrefix + "exhausted",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			HeadSHA: "abc123456789",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(comment, "Review Skipped") {
		t.Error("expected 'Review Skipped' in comment")
	}
}

func TestSynthesize_MixedSuccessAndFailure(t *testing.T) {
	results := []ReviewResult{
		{
			Agent:      "codex",
			ReviewType: "security",
			Status:     "done",
			Output:     "Found vulnerability",
		},
		{
			Agent:      "gemini",
			ReviewType: "security",
			Status:     "failed",
			Error:      "agent crashed",
		},
	}

	comment, err := Synthesize(
		context.Background(), results, SynthesizeOpts{
			Agent:   "nonexistent-synthesis-agent",
			HeadSHA: "abc123456789",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should fall back to raw format since synthesis agent
	// doesn't exist
	if !strings.Contains(
		comment, "Combined Review") {
		t.Error("expected combined review header")
	}
}
