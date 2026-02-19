package storage

import "testing"

const (
	VerdictPass = "P"
	VerdictFail = "F"
)

type verdictTestCase struct {
	name   string
	output string
	want   string
}

func runVerdictTests(t *testing.T, tests []verdictTestCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseVerdict(tt.output)
			if got != tt.want {
				t.Errorf("ParseVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseVerdict(t *testing.T) {
	t.Run("SimplePass", func(t *testing.T) {
		runVerdictTests(t, []verdictTestCase{
			{
				name:   "no issues found at start",
				output: "No issues found. This commit adds a new feature.",
				want:   VerdictPass,
			},
			{
				name:   "no issues found on own line",
				output: "Review complete.\n\nNo issues found.\n\nThe code looks good.",
				want:   VerdictPass,
			},
			{
				name:   "no issues found with leading whitespace",
				output: "  No issues found. Great work!",
				want:   VerdictPass,
			},
			{
				name:   "no issues found lowercase",
				output: "no issues found. The code is clean.",
				want:   VerdictPass,
			},
			{
				name:   "no issues found mixed case",
				output: "NO ISSUES FOUND. Excellent!",
				want:   VerdictPass,
			},
			{
				name:   "no issues with period",
				output: "No issues. The code is clean.",
				want:   VerdictPass,
			},
			{
				name:   "no issues standalone",
				output: "No issues",
				want:   VerdictPass,
			},
			{
				name:   "no findings at start of line",
				output: "No findings to report.",
				want:   VerdictPass,
			},
			{
				name:   "bullet no issues found",
				output: "- No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "asterisk bullet no issues",
				output: "* No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "no issues with this commit",
				output: "No issues with this commit.",
				want:   VerdictPass,
			},
			{
				name:   "no issues in this change",
				output: "No issues in this change. Looks good.",
				want:   VerdictPass,
			},
			{
				name:   "numbered list no issues",
				output: "1. No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "bullet with extra spaces",
				output: "-   No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "large numbered list",
				output: "100. No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "no issues remain is pass",
				output: "No issues found. No issues remain.",
				want:   VerdictPass,
			},
			{
				name:   "no problems exist is pass",
				output: "No issues found. No problems exist.",
				want:   VerdictPass,
			},
			{
				name:   "doesn't have issues is pass",
				output: "No issues found. The code doesn't have issues.",
				want:   VerdictPass,
			},
			{
				name:   "doesn't have any problems is pass",
				output: "No issues found. Code doesn't have any problems.",
				want:   VerdictPass,
			},
			{
				name:   "don't have vulnerabilities is pass",
				output: "No issues found. We don't have vulnerabilities.",
				want:   VerdictPass,
			},
			{
				name:   "no significant issues remain is pass",
				output: "No issues found. No significant issues remain.",
				want:   VerdictPass,
			},
			{
				name:   "no known issues exist is pass",
				output: "No issues found. No known issues exist.",
				want:   VerdictPass,
			},
			{
				name:   "no open issues remain is pass",
				output: "No issues found. No open issues remain.",
				want:   VerdictPass,
			},
			{
				name:   "found no critical issues with module is pass",
				output: "No issues found. Found no critical issues with the module.",
				want:   VerdictPass,
			},
			{
				name:   "didn't find any major issues in code is pass",
				output: "No issues found. I didn't find any major issues in the code.",
				want:   VerdictPass,
			},
			{
				name:   "not finding issues with is pass",
				output: "No issues found. Not finding issues with the code.",
				want:   VerdictPass,
			},
			{
				name:   "did not see issues in module is pass",
				output: "No issues found. I did not see issues in the module.",
				want:   VerdictPass,
			},
			{
				name:   "can't find issues with is pass",
				output: "No issues found. I can't find issues with the code.",
				want:   VerdictPass,
			},
			{
				name:   "cannot find issues in is pass",
				output: "No issues found. Cannot find issues in the module.",
				want:   VerdictPass,
			},
			{
				name:   "couldn't find issues with is pass",
				output: "No issues found. I couldn't find issues with the implementation.",
				want:   VerdictPass,
			},
		})
	})

	t.Run("FieldLabels", func(t *testing.T) {
		runVerdictTests(t, []verdictTestCase{
			{
				name:   "review findings label",
				output: "1. **Summary**: Adds features.\n2. **Review Findings**: No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "findings label",
				output: "**Findings**: No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "verdict label pass",
				output: "**Verdict**: No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "verdict label no space after colon",
				output: "**Verdict**:No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "review result label no space after colon",
				output: "**Review Result**:No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "review findings label no space after colon",
				output: "2. **Review Findings**:No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "findings label no space after colon",
				output: "**Findings**:No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "result label no space after colon",
				output: "**Result**:No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "review label no space after colon",
				output: "**Review**:No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "verdict label tab after colon",
				output: "**Verdict**:\tNo issues found.",
				want:   VerdictPass,
			},
		})
	})

	t.Run("MarkdownFormatting", func(t *testing.T) {
		runVerdictTests(t, []verdictTestCase{
			{
				name:   "bold no issues found",
				output: "**No issues found.**",
				want:   VerdictPass,
			},
			{
				name:   "bold no issues in sentence",
				output: "**No issues found.** The code looks good.",
				want:   VerdictPass,
			},
			{
				name:   "markdown header no issues",
				output: "## No issues found",
				want:   VerdictPass,
			},
			{
				name:   "markdown h3 no issues",
				output: "### No issues found.",
				want:   VerdictPass,
			},
			{
				name:   "underscore bold no issues",
				output: "__No issues found.__",
				want:   VerdictPass,
			},
		})
	})

	t.Run("PhrasingVariations", func(t *testing.T) {
		runVerdictTests(t, []verdictTestCase{
			{
				name:   "no tests failed is pass",
				output: "No issues found; no tests failed.",
				want:   VerdictPass,
			},
			{
				name:   "zero errors is pass",
				output: "No issues found, 0 errors.",
				want:   VerdictPass,
			},
			{
				name:   "without bugs is pass",
				output: "No issues found, without bugs.",
				want:   VerdictPass,
			},
			{
				name:   "avoid panics is pass",
				output: "No issues found. This commit hardens the code to avoid slicing panics.",
				want:   VerdictPass,
			},
			{
				name:   "fix errors is pass",
				output: "No issues found. The changes fix potential errors in the parser.",
				want:   VerdictPass,
			},
			{
				name:   "prevents crashes is pass",
				output: "No issues found. This update prevents crashes when input is nil.",
				want:   VerdictPass,
			},
			{
				name:   "will avoid is pass",
				output: "No issues found. This will avoid panics.",
				want:   VerdictPass,
			},
			{
				name:   "no tests have failed",
				output: "No issues found; no tests have failed.",
				want:   VerdictPass,
			},
			{
				name:   "none of the tests failed",
				output: "No issues found. None of the tests failed.",
				want:   VerdictPass,
			},
			{
				name:   "never fails",
				output: "No issues found. Build never fails.",
				want:   VerdictPass,
			},
			{
				name:   "didn't fail contraction",
				output: "No issues found. Tests didn't fail.",
				want:   VerdictPass,
			},
			{
				name:   "hasn't crashed",
				output: "No issues found. Code hasn't crashed.",
				want:   VerdictPass,
			},
			{
				name:   "i didn't find any issues",
				output: "I didn't find any issues in this commit.",
				want:   VerdictPass,
			},
			{
				name:   "i didnt find any issues curly apostrophe",
				output: "I didn\u2019t find any issues in this commit.",
				want:   VerdictPass,
			},
			{
				name:   "i didn't find any issues with checked for",
				output: "I didn't find any issues. I checked for bugs, security issues, testing gaps, regressions, and code quality concerns.",
				want:   VerdictPass,
			},
			{
				name:   "i didn't find any issues multiline",
				output: "I didn't find any issues. I checked for bugs, security issues, testing gaps, regressions, and code\nquality concerns.",
				want:   VerdictPass,
			},
			{
				name:   "exact review 583 text",
				output: "I didn't find any issues. I checked for bugs, security issues, testing gaps, regressions, and code\nquality concerns.\nThe change updates selection revalidation during job refresh to respect visibility (repo filter and\n`hideAddressed`) in `cmd/roborev/tui.go`, and adds a focused set of `hideAddressed` tests (toggle,\nfiltering, selection movement, refresh, navigation, and repo filter interaction) in\n`cmd/roborev/tui_test.go`.",
				want:   VerdictPass,
			},
			{
				name:   "i did not find any issues",
				output: "I did not find any issues with the code.",
				want:   VerdictPass,
			},
			{
				name:   "i found no issues",
				output: "I found no issues.",
				want:   VerdictPass,
			},
			{
				name:   "i found no issues in this commit",
				output: "I found no issues in this commit. The changes are well-structured.",
				want:   VerdictPass,
			},
			{
				name:   "no issues with checked for context",
				output: "No issues found. I checked for bugs, security issues, testing gaps, regressions, and code quality concerns.",
				want:   VerdictPass,
			},
			{
				name:   "no issues with looking for context",
				output: "No issues. I was looking for bugs and errors but found none.",
				want:   VerdictPass,
			},
			{
				name:   "no issues with looked for context",
				output: "No issues found. I looked for crashes and panics.",
				want:   VerdictPass,
			},
			{
				name:   "checked for and found no issues",
				output: "No issues found. I checked for bugs and found no issues.",
				want:   VerdictPass,
			},
			{
				name:   "checked for and found no bugs",
				output: "No issues found. I checked for security issues and found no bugs.",
				want:   VerdictPass,
			},
			{
				name:   "checked for and found nothing",
				output: "No issues found. I checked for errors and found nothing.",
				want:   VerdictPass,
			},
			{
				name:   "checked for and found none",
				output: "No issues found. I checked for crashes and found none.",
				want:   VerdictPass,
			},
			{
				name:   "checked for and found 0 issues",
				output: "No issues found. I checked for bugs and found 0 issues.",
				want:   VerdictPass,
			},
			{
				name:   "checked for and found zero errors",
				output: "No issues found. I looked for problems and found zero errors.",
				want:   VerdictPass,
			},
		})
	})

	t.Run("BenignPhrases", func(t *testing.T) {
		runVerdictTests(t, []verdictTestCase{
			{
				name:   "benign problem statement",
				output: "No issues found. The problem statement is clear.",
				want:   VerdictPass,
			},
			{
				name:   "benign issue tracker",
				output: "No issues found. Issue tracker updated.",
				want:   VerdictPass,
			},
			{
				name:   "benign vulnerability disclosure",
				output: "No issues found. Vulnerability disclosure policy reviewed.",
				want:   VerdictPass,
			},
			{
				name:   "benign problem domain",
				output: "No issues found. The problem domain is well understood.",
				want:   VerdictPass,
			},
			{
				name:   "error handling in description is pass",
				output: "No issues found. The commit hardens the test setup with error handling around filesystem operations.",
				want:   VerdictPass,
			},
			{
				name:   "error messages in description is pass",
				output: "No issues found. The code improves error messages for better debugging.",
				want:   VerdictPass,
			},
			{
				name:   "multiple occurrences all positive is pass",
				output: "No issues found. Error handling added to auth. Error handling also improved in utils.",
				want:   VerdictPass,
			},
			{
				name:   "partial word match problem domains is pass",
				output: "No issues found. The problem domains are well-defined.",
				want:   VerdictPass,
			},
			{
				name:   "partial word match errorhandling is pass",
				output: "No issues found. The errorhandling module works well.",
				want:   VerdictPass,
			},
			{
				name:   "problem domain vs problem domains mixed is pass",
				output: "No issues found. The problem domain is clear, and the problem domains are complex.",
				want:   VerdictPass,
			},
			{
				name:   "found a way is benign",
				output: "No issues found. I checked for bugs and found a way to improve the docs.",
				want:   VerdictPass,
			},
			{
				name:   "severity in prose not a finding",
				output: "No issues found. I rate this as Medium importance for the project.",
				want:   VerdictPass,
			},
			{
				name:   "medium without separator not a finding",
				output: "No issues found. The medium was oil on canvas.",
				want:   VerdictPass,
			},
			{
				name:   "severity legend not a finding",
				output: "No issues found.\n\nSeverity levels:\nHigh - immediate action required.\nMedium - should be addressed.\nLow - minor concern.",
				want:   VerdictPass,
			},
			{
				name:   "priority scale not a finding",
				output: "No issues found.\n\nPriority scale:\nCritical: system down\nHigh: major feature broken",
				want:   VerdictPass,
			},
			{
				name:   "severity legend with descriptions not a finding",
				output: "No issues found.\n\nSeverity levels:\nHigh - immediate action required.\n  These issues block release.\nMedium - should be addressed.",
				want:   VerdictPass,
			},
			{
				name:   "high-level overview not a finding",
				output: "No issues found. This is a high-level overview of the changes.",
				want:   VerdictPass,
			},
			{
				name:   "low-level details not a finding",
				output: "No issues found. The commit adds low-level optimizations.",
				want:   VerdictPass,
			},
			{
				name:   "severity label value in legend is not finding",
				output: "No issues found.\n\nSeverity levels:\nSeverity: High - immediate action required.\nSeverity: Low - minor concern.",
				want:   VerdictPass,
			},
			{
				name:   "markdown legend header not a finding",
				output: "No issues found.\n\n**Severity levels:**\n- **High** - immediate action required.\n- **Medium** - should be addressed.\n- **Low** - minor concern.",
				want:   VerdictPass,
			},
			{
				name:   "markdown legend header with severity label not a finding",
				output: "No issues found.\n\n**Severity levels:**\nSeverity: High - immediate action required.\nSeverity: Low - minor concern.",
				want:   VerdictPass,
			},
		})
	})

	t.Run("FailImperative", func(t *testing.T) {
		runVerdictTests(t, []verdictTestCase{
			{
				name:   "imperative fix is fail",
				output: "No issues found. Fix failing tests.",
				want:   VerdictFail,
			},
			{
				name:   "imperative avoid is fail",
				output: "No issues found. Avoid panics in slice operations.",
				want:   VerdictFail,
			},
			{
				name:   "imperative prevent is fail",
				output: "No issues found. Prevent errors by adding validation.",
				want:   VerdictFail,
			},
			{
				name:   "imperative after colon is fail",
				output: "No issues found: Fix failing tests.",
				want:   VerdictFail,
			},
			{
				name:   "imperative after comma is fail",
				output: "No issues found, Fix failing tests.",
				want:   VerdictFail,
			},
			{
				name:   "imperative in bullet list is fail",
				output: "No issues found. - Fix failing tests.",
				want:   VerdictFail,
			},
			{
				name:   "imperative in asterisk list is fail",
				output: "No issues found. * Avoid panics.",
				want:   VerdictFail,
			},
			{
				name:   "imperative in numbered list is fail",
				output: "No issues found. 1. Fix the failing tests.",
				want:   VerdictFail,
			},
		})
	})

	t.Run("FailCaveats", func(t *testing.T) {
		runVerdictTests(t, []verdictTestCase{
			{
				name:   "review findings with caveat is fail",
				output: "2. **Review Findings**: No issues found, but consider refactoring.",
				want:   VerdictFail,
			},
			{
				name:   "errors after comma boundary is fail",
				output: "No issues found, errors remain.",
				want:   VerdictFail,
			},
			{
				name:   "errors after colon boundary is fail",
				output: "No issues found: errors remain.",
				want:   VerdictFail,
			},
			{
				name:   "bug after comma is fail",
				output: "No issues found, but there is a bug.",
				want:   VerdictFail,
			},
			{
				name:   "errors after period-quote boundary is fail",
				output: `No issues found." errors remain.`,
				want:   VerdictFail,
			},
			{
				name:   "errors after period-paren boundary is fail",
				output: "No issues found.) errors remain.",
				want:   VerdictFail,
			},
			{
				name:   "error handling is missing is fail",
				output: "No issues found. Error handling is missing in the auth module.",
				want:   VerdictFail,
			},
			{
				name:   "error codes are wrong is fail",
				output: "No issues found. Error codes are wrong in the API response.",
				want:   VerdictFail,
			},
			{
				name:   "error message needs improvement is fail",
				output: "No issues found. The error message needs to be more descriptive.",
				want:   VerdictFail,
			},
			{
				name:   "error handling is broken is fail",
				output: "No issues found. Error handling is broken after refactor.",
				want:   VerdictFail,
			},
			{
				name:   "mixed polarity error handling - positive then negative is fail",
				output: "No issues found. Error handling improved, but error handling is missing in auth.",
				want:   VerdictFail,
			},
			{
				name:   "mixed polarity error handling - negative then positive is fail",
				output: "No issues found. Error handling is broken, though error handling in utils is good.",
				want:   VerdictFail,
			},
			{
				name:   "found colon with spaces normalized",
				output: "No issues found. Found:   a bug.",
				want:   VerdictFail,
			},
			{
				name:   "found issues without article",
				output: "No issues found. I checked for bugs and found issues.",
				want:   VerdictFail,
			},
			{
				name:   "found errors without article",
				output: "No issues found. I looked for problems and found errors.",
				want:   VerdictFail,
			},
			{
				name:   "still crashes pattern",
				output: "No issues found. I checked for bugs and it still crashes.",
				want:   VerdictFail,
			},
			{
				name:   "still fails pattern",
				output: "No issues found. Checked for regressions but test still fails.",
				want:   VerdictFail,
			},
			{
				name:   "negation then positive finding",
				output: "No issues found. I checked for bugs, found no issues, but found a crash.",
				want:   VerdictFail,
			},
			{
				name:   "found nothing then found error",
				output: "No issues found. I looked for bugs and found nothing, but found an error later.",
				want:   VerdictFail,
			},
			{
				name:   "multiple negations then finding",
				output: "No issues found. Found no bugs, found nothing wrong, but found a race condition.",
				want:   VerdictFail,
			},
			{
				name:   "found multiple issues",
				output: "No issues found. I checked for bugs and found multiple issues.",
				want:   VerdictFail,
			},
			{
				name:   "found several bugs",
				output: "No issues found. I looked for problems and found several bugs.",
				want:   VerdictFail,
			},
			{
				name:   "found many errors",
				output: "No issues found. Checked for regressions and found many errors.",
				want:   VerdictFail,
			},
			{
				name:   "found a few bugs",
				output: "No issues found. I checked for problems and found a few bugs.",
				want:   VerdictFail,
			},
			{
				name:   "found two issues",
				output: "No issues found. I looked for regressions and found two issues.",
				want:   VerdictFail,
			},
			{
				name:   "found various problems",
				output: "No issues found. Checked for bugs and found various problems.",
				want:   VerdictFail,
			},
			{
				name:   "found multiple critical issues",
				output: "No issues found. I checked and found multiple critical issues.",
				want:   VerdictFail,
			},
			{
				name:   "found several severe bugs",
				output: "No issues found. Review found several severe bugs in the code.",
				want:   VerdictFail,
			},
			{
				name:   "found a potential vulnerability",
				output: "No issues found. I checked for security issues and found a potential vulnerability.",
				want:   VerdictFail,
			},
			{
				name:   "found multiple vulnerabilities plural",
				output: "No issues found. Security scan found multiple vulnerabilities.",
				want:   VerdictFail,
			},
			{
				name:   "found at start of clause",
				output: "No issues found. Found issues in the auth module.",
				want:   VerdictFail,
			},
			{
				name:   "found colon issues",
				output: "No issues found. Review result: found multiple bugs.",
				want:   VerdictFail,
			},
			{
				name:   "there are issues",
				output: "No issues found. There are issues with the implementation.",
				want:   VerdictFail,
			},
			{
				name:   "problems remain",
				output: "No issues found. Problems remain in the codebase.",
				want:   VerdictFail,
			},
			{
				name:   "has issues",
				output: "No issues found. The code has issues.",
				want:   VerdictFail,
			},
			{
				name:   "has vulnerabilities",
				output: "No issues found. The system has vulnerabilities.",
				want:   VerdictFail,
			},
			{
				name:   "have vulnerabilities",
				output: "No issues found. These modules have vulnerabilities.",
				want:   VerdictFail,
			},
			{
				name:   "many issues remain not negated by any substring",
				output: "No issues found. Many issues remain.",
				want:   VerdictFail,
			},
			{
				name:   "no doubt issues remain not negated by distant no",
				output: "No issues found. No doubt, issues remain.",
				want:   VerdictFail,
			},
			{
				name:   "no changes issues exist not negated",
				output: "No issues found. No changes; issues exist.",
				want:   VerdictFail,
			},
			{
				name:   "found issues with is caveat",
				output: "No issues found. We found issues with logging.",
				want:   VerdictFail,
			},
			{
				name:   "not only issues with is caveat",
				output: "No issues found. Not only issues with X but also Y.",
				want:   VerdictFail,
			},
			{
				name:   "distant negation does not negate later issues with",
				output: "No issues found. I did not find issues in the first run and there are issues with logging.",
				want:   VerdictFail,
			},
			{
				name:   "no issues mid-sentence should fail",
				output: "I found no issues with the formatting, but there are bugs.",
				want:   VerdictFail,
			},
			{
				name:   "no issues as part of larger phrase should fail",
				output: "There are no issues with X, but Y needs fixing.",
				want:   VerdictFail,
			},
			{
				name:   "findings before no issues mention",
				output: "Medium - Security issue\nOtherwise no issues found.",
				want:   VerdictFail,
			},
			{
				name:   "no issues found but caveat",
				output: "No issues found in module X, but Y needs fixing.",
				want:   VerdictFail,
			},
			{
				name:   "no issues found however caveat",
				output: "No issues found, however consider refactoring.",
				want:   VerdictFail,
			},
			{
				name:   "no issues found except caveat",
				output: "No issues found except for minor style issues.",
				want:   VerdictFail,
			},
			{
				name:   "no issues found beyond caveat",
				output: "No issues found beyond the two notes above.",
				want:   VerdictFail,
			},
			{
				name:   "no issues found but with period",
				output: "No issues found but.",
				want:   VerdictFail,
			},
			{
				name:   "no issues with em dash caveat",
				output: "No issues found—but there is a bug.",
				want:   VerdictFail,
			},
			{
				name:   "no issues with comma caveat",
				output: "No issues found, but consider refactoring.",
				want:   VerdictFail,
			},
			{
				name:   "no issues with semicolon then failure",
				output: "No issues with this change; tests failed.",
				want:   VerdictFail,
			},
			{
				name:   "no issues then second sentence with break",
				output: "No issues with this change. It breaks X.",
				want:   VerdictFail,
			},
			{
				name:   "no issues then crash",
				output: "No issues with this change—panic on start.",
				want:   VerdictFail,
			},
			{
				name:   "no issues then error",
				output: "No issues with lint. Error in tests.",
				want:   VerdictFail,
			},
			{
				name:   "no issues then bug mention",
				output: "No issues found. Bug in production.",
				want:   VerdictFail,
			},
			{
				name:   "parenthesized but caveat",
				output: "No issues found (but needs review).",
				want:   VerdictFail,
			},
			{
				name:   "quoted caveat",
				output: "No issues found \"but\" consider refactoring.",
				want:   VerdictFail,
			},
			{
				name:   "double negation not without",
				output: "No issues found. Not without errors.",
				want:   VerdictFail,
			},
			{
				name:   "question then error",
				output: "No issues found? Errors occurred.",
				want:   VerdictFail,
			},
			{
				name:   "exclamation then error",
				output: "No issues found! Error in tests.",
				want:   VerdictFail,
			},
		})
	})

	t.Run("FailExplicit", func(t *testing.T) {
		runVerdictTests(t, []verdictTestCase{
			{
				name:   "checked for but found issue",
				output: "No issues found. I checked for bugs but found a race condition.",
				want:   VerdictFail,
			},
			{
				name:   "looked for and found crash",
				output: "No issues found. I looked for crashes and found a panic.",
				want:   VerdictFail,
			},
			{
				name:   "checked for however found problem",
				output: "No issues found. I checked for errors however there is a crash.",
				want:   VerdictFail,
			},
			{
				name:   "has findings with severity",
				output: "Medium - Bug in line 42\nThe code has issues.",
				want:   VerdictFail,
			},
			{
				name:   "empty output",
				output: "",
				want:   VerdictFail,
			},
			{
				name:   "ambiguous language",
				output: "The commit looks mostly fine but could use some cleanup.",
				want:   VerdictFail,
			},
			{
				name:   "severity label medium em dash",
				output: "**Findings**\n- Medium — Possible regression in deploy.\nNo issues found beyond the notes above.",
				want:   VerdictFail,
			},
			{
				name:   "severity label low with colon",
				output: "- Low: Minor style issue.\nOtherwise no issues.",
				want:   VerdictFail,
			},
			{
				name:   "severity label high with dash",
				output: "* High - Security vulnerability found.\nNo issues found.",
				want:   VerdictFail,
			},
			{
				name:   "severity label critical with bullet",
				output: "- Critical — Data loss possible.\nNo issues otherwise.",
				want:   VerdictFail,
			},
			{
				name:   "severity label critical without bullet",
				output: "Critical — Data loss possible.\nNo issues otherwise.",
				want:   VerdictFail,
			},
			{
				name:   "severity label high without bullet",
				output: "High: Security vulnerability in auth module.\nNo issues found.",
				want:   VerdictFail,
			},
			{
				name:   "severity label value format high",
				output: "- **Severity**: High\n- **Location**: file.go\n- **Problem**: Bug found.",
				want:   VerdictFail,
			},
			{
				name:   "severity label value format low",
				output: "- **Severity**: Low\n- **Problem**: Minor style issue.",
				want:   VerdictFail,
			},
			{
				name:   "severity label value with no issues in other file",
				output: "- **Severity**: High\n- **Problem**: Bug found.\n\n- **No issues found** in test_file.go.",
				want:   VerdictFail,
			},
			{
				name:   "severity label value plain text",
				output: "Severity: High\nLocation: file.go\nProblem: Bug found.",
				want:   VerdictFail,
			},
			{
				name:   "severity label value hyphen separator",
				output: "Severity - High\nLocation: file.go\nProblem: Bug found.",
				want:   VerdictFail,
			},
		})
	})
}
