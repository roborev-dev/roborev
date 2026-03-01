package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

// setupRenderModel creates a standardized model for rendering tests
func setupRenderModel(
	view viewKind,
	review *storage.Review,
	opts ...testModelOption,
) model {
	base := []testModelOption{
		withCurrentView(view),
		withReview(review),
		withDimensions(100, 30),
	}
	return initTestModel(append(base, opts...)...)
}

func assertOutputContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("Expected output to contain %q, got:\n%s", want, got)
	}
}

func assertAbsent(t *testing.T, got, want string) {
	t.Helper()
	if strings.Contains(got, want) {
		t.Errorf("Expected output NOT to contain %q, got:\n%s", want, got)
	}
}

func TestTUIRenderViews(t *testing.T) {
	verdictPass := "P"

	tests := []struct {
		name                      string
		view                      viewKind
		branch                    string
		review                    *storage.Review
		wantContains              []string
		wantAbsent                []string
		checkContentStartsOnLine3 bool
		checkContentStartsOnLine4 bool
		checkNoVerdictOnLine3     bool
	}{
		{
			name:   "review view with branch and addressed",
			view:   viewReview,
			branch: "feature/test",
			review: &storage.Review{
				ID:        10,
				Output:    "Some review output",
				Addressed: true,
				Job: &storage.ReviewJob{
					ID:       1,
					GitRef:   "abc1234",
					RepoName: "myrepo",
					Agent:    "codex",
				},
			},
			wantContains: []string{"on feature/test", "[ADDRESSED]", "myrepo", "abc1234"},
		},
		{
			name: "review view with model",
			view: viewReview,
			review: &storage.Review{
				ID:     10,
				Agent:  "codex",
				Output: "Some review output",
				Job: &storage.ReviewJob{
					ID:       1,
					GitRef:   "abc1234",
					RepoName: "myrepo",
					Agent:    "codex",
					Model:    "o3",
				},
			},
			wantContains: []string{"(codex: o3)"},
		},
		{
			name: "review view without model",
			view: viewReview,
			review: &storage.Review{
				ID:     10,
				Agent:  "codex",
				Output: "Some review output",
				Job: &storage.ReviewJob{
					ID:       1,
					GitRef:   "abc1234",
					RepoName: "myrepo",
					Agent:    "codex",
					Model:    "",
				},
			},
			wantContains: []string{"(codex)"},
			wantAbsent:   []string{"(codex:"},
		},
		{
			name: "prompt view with model",
			view: viewKindPrompt,
			review: &storage.Review{
				ID:     10,
				Agent:  "codex",
				Prompt: "Review this code",
				Job: &storage.ReviewJob{
					ID:     1,
					GitRef: "abc1234",
					Agent:  "codex",
					Model:  "o3",
				},
			},
			wantContains: []string{"#1", "(codex: o3)"},
		},
		{
			name: "prompt view without model",
			view: viewKindPrompt,
			review: &storage.Review{
				ID:     10,
				Agent:  "codex",
				Prompt: "Review this code",
				Job: &storage.ReviewJob{
					ID:     1,
					GitRef: "abc1234",
					Agent:  "codex",
					Model:  "",
				},
			},
			wantContains: []string{"(codex)"},
			wantAbsent:   []string{"(codex:"},
		},
		{
			name:   "review view no branch for range",
			view:   viewReview,
			branch: "",
			review: &storage.Review{
				ID:     10,
				Output: "Some review output",
				Job: &storage.ReviewJob{
					ID:       1,
					GitRef:   "abc123..def456",
					RepoName: "myrepo",
					Agent:    "codex",
				},
			},
			wantContains: []string{"abc123..def456"},
			wantAbsent:   []string{" on "},
		},
		{
			name: "review view no blank line without verdict",
			view: viewReview,
			review: &storage.Review{
				ID:     10,
				Output: "Line 1\nLine 2\nLine 3",
				Job: &storage.ReviewJob{
					ID:       1,
					GitRef:   "abc1234",
					RepoName: "myrepo",
					Agent:    "codex",
					Verdict:  nil,
				},
			},
			wantContains:              []string{"Review", "abc1234"},
			checkContentStartsOnLine3: true,
		},
		{
			name: "review view verdict on line 2",
			view: viewReview,
			review: &storage.Review{
				ID:     10,
				Output: "Line 1\nLine 2\nLine 3",
				Job: &storage.ReviewJob{
					ID:       1,
					GitRef:   "abc1234",
					RepoName: "myrepo",
					Agent:    "codex",
					Verdict:  &verdictPass,
				},
			},
			wantContains:              []string{"Review", "abc1234", "Verdict"},
			checkContentStartsOnLine4: true,
		},
		{
			name: "review view addressed without verdict",
			view: viewReview,
			review: &storage.Review{
				ID:        10,
				Output:    "Line 1\n\nLine 2\n\nLine 3",
				Addressed: true,
				Job: &storage.ReviewJob{
					ID:       1,
					GitRef:   "abc1234",
					RepoName: "myrepo",
					Agent:    "codex",
					Verdict:  nil,
				},
			},
			wantContains:              []string{"Review", "abc1234", "[ADDRESSED]"},
			checkContentStartsOnLine4: true,
			checkNoVerdictOnLine3:     true,
		},
		{
			name:   "failed job no branch shown",
			view:   viewReview,
			branch: "",
			review: &storage.Review{
				Agent:  "codex",
				Output: "Job failed:\n\nsome error",
				Job: &storage.ReviewJob{
					ID:       1,
					GitRef:   "abc1234",
					RepoName: "myrepo",
					Agent:    "codex",
					Status:   storage.JobStatusFailed,
				},
			},
			wantAbsent: []string{" on "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := setupRenderModel(tt.view, tt.review, withBranchName(tt.branch))
			output := m.View()

			for _, want := range tt.wantContains {
				assertOutputContains(t, output, want)
			}
			for _, absent := range tt.wantAbsent {
				assertAbsent(t, output, absent)
			}

			// Specific check for layout tests (Lines 0, 1, 2)
			if tt.checkContentStartsOnLine3 {
				lines := strings.Split(output, "\n")
				foundContent := false
				for _, line := range lines[2:] {
					if strings.Contains(stripANSI(line), "Line 1") {
						foundContent = true
						break
					}
				}
				if !foundContent {
					t.Errorf("Content should contain 'Line 1' after header, output:\n%s", output)
				}
			}
			if tt.checkContentStartsOnLine4 {
				lines := strings.Split(output, "\n")
				foundContent := false
				for _, line := range lines[3:] {
					if strings.Contains(stripANSI(line), "Line 1") {
						foundContent = true
						break
					}
				}
				if !foundContent {
					t.Errorf("Content should contain 'Line 1' after addressed/verdict line, output:\n%s", output)
				}
			}
			if tt.checkNoVerdictOnLine3 {
				lines := strings.Split(output, "\n")
				if len(lines) > 2 && strings.Contains(lines[2], "Verdict") {
					t.Errorf("Line 2 should not contain 'Verdict' when no verdict is set, got: %s", lines[2])
				}
			}
		})
	}
}

func TestTUIVisibleLinesCalculationTable(t *testing.T) {
	verdictPass := "P"
	verdictFail := "F"

	tests := []struct {
		name                     string
		width                    int
		height                   int
		branch                   string
		reviewAgent              string
		jobRef                   string
		jobRepoName              string
		jobAgent                 string
		jobVerdict               *string
		addressed                bool
		wantVisibleLines         int
		wantContains             []string
		checkVisibleContentCount bool
	}{
		{
			name:                     "no verdict",
			width:                    120,
			height:                   10,
			jobRef:                   "abc1234",
			jobAgent:                 "codex",
			jobVerdict:               nil,
			wantVisibleLines:         5, // height 10 - 5 non-content = 5
			checkVisibleContentCount: true,
		},
		{
			name:             "with verdict",
			width:            120,
			height:           10,
			jobRef:           "abc1234",
			jobAgent:         "codex",
			jobVerdict:       &verdictPass,
			wantVisibleLines: 4, // height 10 - 6 non-content = 4
		},
		{
			name:             "narrow terminal",
			width:            50,
			height:           10,
			jobRef:           "abc1234",
			jobAgent:         "codex",
			jobVerdict:       nil,
			wantVisibleLines: 4, // height 10 - 6 non-content = 4
		},
		{
			name:             "narrow terminal with verdict",
			width:            50,
			height:           10,
			jobRef:           "abc1234",
			jobAgent:         "codex",
			jobVerdict:       &verdictFail,
			wantVisibleLines: 3, // height 10 - 7 non-content = 3
			wantContains:     []string{"Verdict"},
		},
		{
			name:             "long title wraps",
			width:            50,
			height:           12,
			branch:           "feature/very-long-branch-name",
			reviewAgent:      "claude-code",
			jobRef:           "abc1234567890..def5678901234",
			jobRepoName:      "very-long-repository-name-here",
			jobAgent:         "claude-code",
			jobVerdict:       nil,
			addressed:        true,
			wantVisibleLines: 3, // height 12 - 9 non-content = 3
			wantContains:     []string{"very-long-repository-name-here", "feature/very-long-branch-name", "[ADDRESSED]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review := &storage.Review{
				ID:        10,
				Output:    "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10\nL11\nL12\nL13\nL14\nL15\nL16\nL17\nL18\nL19\nL20",
				Addressed: tt.addressed,
				Agent:     tt.reviewAgent,
				Job: &storage.ReviewJob{
					ID:       1,
					GitRef:   tt.jobRef,
					RepoName: tt.jobRepoName,
					Agent:    tt.jobAgent,
					Verdict:  tt.jobVerdict,
				},
			}

			m := setupRenderModel(viewReview, review, withBranchName(tt.branch))
			m.width = tt.width
			m.height = tt.height

			output := m.View()

			expectedIndicator := fmt.Sprintf("[1-%d of %d lines]", tt.wantVisibleLines, 21)
			if !strings.Contains(output, expectedIndicator) {
				t.Errorf("Expected scroll indicator '%s', output: %s", expectedIndicator, output)
			}

			if tt.checkVisibleContentCount {
				contentCount := 0
				for line := range strings.SplitSeq(output, "\n") {
					trimmed := strings.TrimSpace(stripANSI(line))
					if len(trimmed) >= 2 && trimmed[0] == 'L' && trimmed[1] >= '0' && trimmed[1] <= '9' {
						contentCount++
					}
				}
				if contentCount == 0 {
					t.Error("Expected at least some content lines visible")
				}
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(output, want) {
					t.Errorf("Expected output to contain %q", want)
				}
			}
		})
	}
}
