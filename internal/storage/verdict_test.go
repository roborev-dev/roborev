package storage

import "testing"

const (
	VerdictPass = "P"
	VerdictFail = "F"
)

func TestParseVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		want     string
		subtests []struct {
			name   string
			output string
		}
	}{
		{
			name: "SimplePass",
			want: VerdictPass,
			subtests: []struct {
				name   string
				output string
			}{

				{
					name:   "no issues found at start",
					output: "No issues found. This commit adds a new feature.",
				},
				{
					name:   "no issues found on own line",
					output: "Review complete.\n\nNo issues found.\n\nThe code looks good.",
				},
				{
					name:   "no issues found with leading whitespace",
					output: "  No issues found. Great work!",
				},
				{
					name:   "no issues found lowercase",
					output: "no issues found. The code is clean.",
				},
				{
					name:   "no issues found mixed case",
					output: "NO ISSUES FOUND. Excellent!",
				},
				{
					name:   "no issues with period",
					output: "No issues. The code is clean.",
				},
				{
					name:   "no issues standalone",
					output: "No issues",
				},
				{
					name:   "no findings at start of line",
					output: "No findings to report.",
				},
				{
					name:   "bullet no issues found",
					output: "- No issues found.",
				},
				{
					name:   "asterisk bullet no issues",
					output: "* No issues found.",
				},
				{
					name:   "no issues with this commit",
					output: "No issues with this commit.",
				},
				{
					name:   "no issues in this change",
					output: "No issues in this change. Looks good.",
				},
				{
					name:   "numbered list no issues",
					output: "1. No issues found.",
				},
				{
					name:   "bullet with extra spaces",
					output: "-   No issues found.",
				},
				{
					name:   "large numbered list",
					output: "100. No issues found.",
				},
				{
					name:   "no issues remain is pass",
					output: "No issues found. No issues remain.",
				},
				{
					name:   "no problems exist is pass",
					output: "No issues found. No problems exist.",
				},
				{
					name:   "doesn't have issues is pass",
					output: "No issues found. The code doesn't have issues.",
				},
				{
					name:   "doesn't have any problems is pass",
					output: "No issues found. Code doesn't have any problems.",
				},
				{
					name:   "don't have vulnerabilities is pass",
					output: "No issues found. We don't have vulnerabilities.",
				},
				{
					name:   "no significant issues remain is pass",
					output: "No issues found. No significant issues remain.",
				},
				{
					name:   "no known issues exist is pass",
					output: "No issues found. No known issues exist.",
				},
				{
					name:   "no open issues remain is pass",
					output: "No issues found. No open issues remain.",
				},
				{
					name:   "found no critical issues with module is pass",
					output: "No issues found. Found no critical issues with the module.",
				},
				{
					name:   "didn't find any major issues in code is pass",
					output: "No issues found. I didn't find any major issues in the code.",
				},
				{
					name:   "not finding issues with is pass",
					output: "No issues found. Not finding issues with the code.",
				},
				{
					name:   "did not see issues in module is pass",
					output: "No issues found. I did not see issues in the module.",
				},
				{
					name:   "can't find issues with is pass",
					output: "No issues found. I can't find issues with the code.",
				},
				{
					name:   "cannot find issues in is pass",
					output: "No issues found. Cannot find issues in the module.",
				},
				{
					name:   "couldn't find issues with is pass",
					output: "No issues found. I couldn't find issues with the implementation.",
				},
			},
		},
		{
			name: "FieldLabels",
			want: VerdictPass,
			subtests: []struct {
				name   string
				output string
			}{

				{
					name:   "review findings label",
					output: "1. **Summary**: Adds features.\n2. **Review Findings**: No issues found.",
				},
				{
					name:   "findings label",
					output: "**Findings**: No issues found.",
				},
				{
					name:   "verdict label pass",
					output: "**Verdict**: No issues found.",
				},
				{
					name:   "verdict label no space after colon",
					output: "**Verdict**:No issues found.",
				},
				{
					name:   "review result label no space after colon",
					output: "**Review Result**:No issues found.",
				},
				{
					name:   "review findings label no space after colon",
					output: "2. **Review Findings**:No issues found.",
				},
				{
					name:   "findings label no space after colon",
					output: "**Findings**:No issues found.",
				},
				{
					name:   "result label no space after colon",
					output: "**Result**:No issues found.",
				},
				{
					name:   "review label no space after colon",
					output: "**Review**:No issues found.",
				},
				{
					name:   "verdict label tab after colon",
					output: "**Verdict**:\tNo issues found.",
				},
			},
		},
		{
			name: "MarkdownFormatting",
			want: VerdictPass,
			subtests: []struct {
				name   string
				output string
			}{

				{
					name:   "bold no issues found",
					output: "**No issues found.**",
				},
				{
					name:   "bold no issues in sentence",
					output: "**No issues found.** The code looks good.",
				},
				{
					name:   "markdown header no issues",
					output: "## No issues found",
				},
				{
					name:   "markdown h3 no issues",
					output: "### No issues found.",
				},
				{
					name:   "underscore bold no issues",
					output: "__No issues found.__",
				},
			},
		},
		{
			name: "PhrasingVariations",
			want: VerdictPass,
			subtests: []struct {
				name   string
				output string
			}{

				{
					name:   "no tests failed is pass",
					output: "No issues found; no tests failed.",
				},
				{
					name:   "zero errors is pass",
					output: "No issues found, 0 errors.",
				},
				{
					name:   "without bugs is pass",
					output: "No issues found, without bugs.",
				},
				{
					name:   "avoid panics is pass",
					output: "No issues found. This commit hardens the code to avoid slicing panics.",
				},
				{
					name:   "fix errors is pass",
					output: "No issues found. The changes fix potential errors in the parser.",
				},
				{
					name:   "prevents crashes is pass",
					output: "No issues found. This update prevents crashes when input is nil.",
				},
				{
					name:   "will avoid is pass",
					output: "No issues found. This will avoid panics.",
				},
				{
					name:   "no tests have failed",
					output: "No issues found; no tests have failed.",
				},
				{
					name:   "none of the tests failed",
					output: "No issues found. None of the tests failed.",
				},
				{
					name:   "never fails",
					output: "No issues found. Build never fails.",
				},
				{
					name:   "didn't fail contraction",
					output: "No issues found. Tests didn't fail.",
				},
				{
					name:   "hasn't crashed",
					output: "No issues found. Code hasn't crashed.",
				},
				{
					name:   "i didn't find any issues",
					output: "I didn't find any issues in this commit.",
				},
				{
					name:   "i didnt find any issues curly apostrophe",
					output: "I didn\u2019t find any issues in this commit.",
				},
				{
					name:   "i didn't find any issues with checked for",
					output: "I didn't find any issues. I checked for bugs, security issues, testing gaps, regressions, and code quality concerns.",
				},
				{
					name:   "i didn't find any issues multiline",
					output: "I didn't find any issues. I checked for bugs, security issues, testing gaps, regressions, and code\nquality concerns.",
				},
				{
					name:   "exact review 583 text",
					output: "I didn't find any issues. I checked for bugs, security issues, testing gaps, regressions, and code\nquality concerns.\nThe change updates selection revalidation during job refresh to respect visibility (repo filter and\n`hideAddressed`) in `cmd/roborev/tui.go`, and adds a focused set of `hideAddressed` tests (toggle,\nfiltering, selection movement, refresh, navigation, and repo filter interaction) in\n`cmd/roborev/tui_test.go`.",
				},
				{
					name:   "i did not find any issues",
					output: "I did not find any issues with the code.",
				},
				{
					name:   "i found no issues",
					output: "I found no issues.",
				},
				{
					name:   "i found no issues in this commit",
					output: "I found no issues in this commit. The changes are well-structured.",
				},
				{
					name:   "no issues with checked for context",
					output: "No issues found. I checked for bugs, security issues, testing gaps, regressions, and code quality concerns.",
				},
				{
					name:   "no issues with looking for context",
					output: "No issues. I was looking for bugs and errors but found none.",
				},
				{
					name:   "no issues with looked for context",
					output: "No issues found. I looked for crashes and panics.",
				},
				{
					name:   "checked for and found no issues",
					output: "No issues found. I checked for bugs and found no issues.",
				},
				{
					name:   "checked for and found no bugs",
					output: "No issues found. I checked for security issues and found no bugs.",
				},
				{
					name:   "checked for and found nothing",
					output: "No issues found. I checked for errors and found nothing.",
				},
				{
					name:   "checked for and found none",
					output: "No issues found. I checked for crashes and found none.",
				},
				{
					name:   "checked for and found 0 issues",
					output: "No issues found. I checked for bugs and found 0 issues.",
				},
				{
					name:   "checked for and found zero errors",
					output: "No issues found. I looked for problems and found zero errors.",
				},
			},
		},
		{
			name: "BenignPhrases",
			want: VerdictPass,
			subtests: []struct {
				name   string
				output string
			}{

				{
					name:   "benign problem statement",
					output: "No issues found. The problem statement is clear.",
				},
				{
					name:   "benign issue tracker",
					output: "No issues found. Issue tracker updated.",
				},
				{
					name:   "benign vulnerability disclosure",
					output: "No issues found. Vulnerability disclosure policy reviewed.",
				},
				{
					name:   "benign problem domain",
					output: "No issues found. The problem domain is well understood.",
				},
				{
					name:   "error handling in description is pass",
					output: "No issues found. The commit hardens the test setup with error handling around filesystem operations.",
				},
				{
					name:   "error messages in description is pass",
					output: "No issues found. The code improves error messages for better debugging.",
				},
				{
					name:   "multiple occurrences all positive is pass",
					output: "No issues found. Error handling added to auth. Error handling also improved in utils.",
				},
				{
					name:   "partial word match problem domains is pass",
					output: "No issues found. The problem domains are well-defined.",
				},
				{
					name:   "partial word match errorhandling is pass",
					output: "No issues found. The errorhandling module works well.",
				},
				{
					name:   "problem domain vs problem domains mixed is pass",
					output: "No issues found. The problem domain is clear, and the problem domains are complex.",
				},
				{
					name:   "found a way is benign",
					output: "No issues found. I checked for bugs and found a way to improve the docs.",
				},
				{
					name:   "severity in prose not a finding",
					output: "No issues found. I rate this as Medium importance for the project.",
				},
				{
					name:   "medium without separator not a finding",
					output: "No issues found. The medium was oil on canvas.",
				},
				{
					name:   "severity legend not a finding",
					output: "No issues found.\n\nSeverity levels:\nHigh - immediate action required.\nMedium - should be addressed.\nLow - minor concern.",
				},
				{
					name:   "priority scale not a finding",
					output: "No issues found.\n\nPriority scale:\nCritical: system down\nHigh: major feature broken",
				},
				{
					name:   "severity legend with descriptions not a finding",
					output: "No issues found.\n\nSeverity levels:\nHigh - immediate action required.\n  These issues block release.\nMedium - should be addressed.",
				},
				{
					name:   "high-level overview not a finding",
					output: "No issues found. This is a high-level overview of the changes.",
				},
				{
					name:   "low-level details not a finding",
					output: "No issues found. The commit adds low-level optimizations.",
				},
				{
					name:   "severity label value in legend is not finding",
					output: "No issues found.\n\nSeverity levels:\nSeverity: High - immediate action required.\nSeverity: Low - minor concern.",
				},
				{
					name:   "markdown legend header not a finding",
					output: "No issues found.\n\n**Severity levels:**\n- **High** - immediate action required.\n- **Medium** - should be addressed.\n- **Low** - minor concern.",
				},
				{
					name:   "markdown legend header with severity label not a finding",
					output: "No issues found.\n\n**Severity levels:**\nSeverity: High - immediate action required.\nSeverity: Low - minor concern.",
				},
			},
		},
		{
			name: "PassPhraseWins",
			want: VerdictPass,
			subtests: []struct {
				name   string
				output string
			}{

				{
					name:   "historical broken path after pass is benign",
					output: "Review #8609 roborev (codex: gpt-5.4)\nVerdict: Fail\n\nNo issues found. The guard on cmd.Flags().Changed(\"sha\") matches the intended behavior, and the added test exercises the previously broken quiet-mode path.",
				},
				{
					name:   "caveat prose after pass phrase is still pass",
					output: "No issues found, but consider refactoring.",
				},
				{
					name:   "review findings label with caveat prose is still pass",
					output: "2. **Review Findings**: No issues found, but consider refactoring.",
				},
				{
					name:   "process narration after explicit pass phrase is still pass",
					output: "No issues found. I checked for bugs, security issues, testing gaps, regressions, and code quality concerns.",
				},
			},
		},
		{
			name: "FailFallback",
			want: VerdictFail,
			subtests: []struct {
				name   string
				output string
			}{

				{
					name:   "empty output",
					output: "",
				},
				{
					name:   "ambiguous language",
					output: "The commit looks mostly fine but could use some cleanup.",
				},
				{
					name:   "narrative front matter without final verdict defaults to fail",
					output: "Reviewing the diff in context first. I'm opening the touched storage parsing code and adjacent tests to check for regressions.",
				},
				{
					name:   "unstructured issue statement defaults to fail",
					output: "The code has issues.",
				},
			},
		},
		{
			name: "StructuredFail",
			want: VerdictFail,
			subtests: []struct {
				name   string
				output string
			}{

				{
					name:   "findings before no issues mention",
					output: "Medium - Security issue\nOtherwise no issues found.",
				},
				{
					name:   "severity label medium em dash",
					output: "**Findings**\n- Medium \u2014 Possible regression in deploy.\nNo issues found beyond the notes above.",
				},
				{
					name:   "severity label low with colon",
					output: "- Low: Minor style issue.\nOtherwise no issues.",
				},
				{
					name:   "severity label high with dash",
					output: "* High - Security vulnerability found.\nNo issues found.",
				},
				{
					name:   "severity label critical with bullet",
					output: "- Critical \u2014 Data loss possible.\nNo issues otherwise.",
				},
				{
					name:   "severity label critical without bullet",
					output: "Critical \u2014 Data loss possible.\nNo issues otherwise.",
				},
				{
					name:   "severity label high without bullet",
					output: "High: Security vulnerability in auth module.\nNo issues found.",
				},
				{
					name:   "severity label value format high",
					output: "- **Severity**: High\n- **Location**: file.go\n- **Problem**: Bug found.",
				},
				{
					name:   "severity label value format low",
					output: "- **Severity**: Low\n- **Problem**: Minor style issue.",
				},
				{
					name:   "severity label value with no issues in other file",
					output: "- **Severity**: High\n- **Problem**: Bug found.\n\n- **No issues found** in test_file.go.",
				},
				{
					name:   "severity label value plain text",
					output: "Severity: High\nLocation: file.go\nProblem: Bug found.",
				},
				{
					name:   "severity label value hyphen separator",
					output: "Severity - High\nLocation: file.go\nProblem: Bug found.",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, st := range tc.subtests {
				t.Run(st.name, func(t *testing.T) {
					t.Parallel()
					got := ParseVerdict(st.output)
					if got != tc.want {
						t.Errorf("ParseVerdict() = %q, want %q", got, tc.want)
					}
				})
			}
		})
	}
}
