package storage

import "testing"

const (
	VerdictPass = "P"
	VerdictFail = "F"
)

type verdictTestCase struct {
	name   string
	output string
}

func runVerdictTests(t *testing.T, want string, tests []verdictTestCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseVerdict(tt.output)
			if got != want {
				t.Errorf("ParseVerdict() = %q, want %q", got, want)
			}
		})
	}
}

var simplePassTests = []verdictTestCase{
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
}

var fieldLabelsTests = []verdictTestCase{
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
}

var markdownFormattingTests = []verdictTestCase{
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
}

var phrasingVariationsTests = []verdictTestCase{
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
}

var benignPhrasesTests = []verdictTestCase{
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
}

var failImperativeTests = []verdictTestCase{
	{
		name:   "imperative fix is fail",
		output: "No issues found. Fix failing tests.",
	},
	{
		name:   "imperative avoid is fail",
		output: "No issues found. Avoid panics in slice operations.",
	},
	{
		name:   "imperative prevent is fail",
		output: "No issues found. Prevent errors by adding validation.",
	},
	{
		name:   "imperative after colon is fail",
		output: "No issues found: Fix failing tests.",
	},
	{
		name:   "imperative after comma is fail",
		output: "No issues found, Fix failing tests.",
	},
	{
		name:   "imperative in bullet list is fail",
		output: "No issues found. - Fix failing tests.",
	},
	{
		name:   "imperative in asterisk list is fail",
		output: "No issues found. * Avoid panics.",
	},
	{
		name:   "imperative in numbered list is fail",
		output: "No issues found. 1. Fix the failing tests.",
	},
}

var failCaveatsTests = []verdictTestCase{
	{
		name:   "review findings with caveat is fail",
		output: "2. **Review Findings**: No issues found, but consider refactoring.",
	},
	{
		name:   "errors after comma boundary is fail",
		output: "No issues found, errors remain.",
	},
	{
		name:   "errors after colon boundary is fail",
		output: "No issues found: errors remain.",
	},
	{
		name:   "bug after comma is fail",
		output: "No issues found, but there is a bug.",
	},
	{
		name:   "errors after period-quote boundary is fail",
		output: `No issues found." errors remain.`,
	},
	{
		name:   "errors after period-paren boundary is fail",
		output: "No issues found.) errors remain.",
	},
	{
		name:   "error handling is missing is fail",
		output: "No issues found. Error handling is missing in the auth module.",
	},
	{
		name:   "error codes are wrong is fail",
		output: "No issues found. Error codes are wrong in the API response.",
	},
	{
		name:   "error message needs improvement is fail",
		output: "No issues found. The error message needs to be more descriptive.",
	},
	{
		name:   "error handling is broken is fail",
		output: "No issues found. Error handling is broken after refactor.",
	},
	{
		name:   "mixed polarity error handling - positive then negative is fail",
		output: "No issues found. Error handling improved, but error handling is missing in auth.",
	},
	{
		name:   "mixed polarity error handling - negative then positive is fail",
		output: "No issues found. Error handling is broken, though error handling in utils is good.",
	},
	{
		name:   "found colon with spaces normalized",
		output: "No issues found. Found:   a bug.",
	},
	{
		name:   "found issues without article",
		output: "No issues found. I checked for bugs and found issues.",
	},
	{
		name:   "found errors without article",
		output: "No issues found. I looked for problems and found errors.",
	},
	{
		name:   "still crashes pattern",
		output: "No issues found. I checked for bugs and it still crashes.",
	},
	{
		name:   "still fails pattern",
		output: "No issues found. Checked for regressions but test still fails.",
	},
	{
		name:   "negation then positive finding",
		output: "No issues found. I checked for bugs, found no issues, but found a crash.",
	},
	{
		name:   "found nothing then found error",
		output: "No issues found. I looked for bugs and found nothing, but found an error later.",
	},
	{
		name:   "multiple negations then finding",
		output: "No issues found. Found no bugs, found nothing wrong, but found a race condition.",
	},
	{
		name:   "found multiple issues",
		output: "No issues found. I checked for bugs and found multiple issues.",
	},
	{
		name:   "found several bugs",
		output: "No issues found. I looked for problems and found several bugs.",
	},
	{
		name:   "found many errors",
		output: "No issues found. Checked for regressions and found many errors.",
	},
	{
		name:   "found a few bugs",
		output: "No issues found. I checked for problems and found a few bugs.",
	},
	{
		name:   "found two issues",
		output: "No issues found. I looked for regressions and found two issues.",
	},
	{
		name:   "found various problems",
		output: "No issues found. Checked for bugs and found various problems.",
	},
	{
		name:   "found multiple critical issues",
		output: "No issues found. I checked and found multiple critical issues.",
	},
	{
		name:   "found several severe bugs",
		output: "No issues found. Review found several severe bugs in the code.",
	},
	{
		name:   "found a potential vulnerability",
		output: "No issues found. I checked for security issues and found a potential vulnerability.",
	},
	{
		name:   "found multiple vulnerabilities plural",
		output: "No issues found. Security scan found multiple vulnerabilities.",
	},
	{
		name:   "found at start of clause",
		output: "No issues found. Found issues in the auth module.",
	},
	{
		name:   "found colon issues",
		output: "No issues found. Review result: found multiple bugs.",
	},
	{
		name:   "there are issues",
		output: "No issues found. There are issues with the implementation.",
	},
	{
		name:   "problems remain",
		output: "No issues found. Problems remain in the codebase.",
	},
	{
		name:   "has issues",
		output: "No issues found. The code has issues.",
	},
	{
		name:   "has vulnerabilities",
		output: "No issues found. The system has vulnerabilities.",
	},
	{
		name:   "have vulnerabilities",
		output: "No issues found. These modules have vulnerabilities.",
	},
	{
		name:   "many issues remain not negated by any substring",
		output: "No issues found. Many issues remain.",
	},
	{
		name:   "no doubt issues remain not negated by distant no",
		output: "No issues found. No doubt, issues remain.",
	},
	{
		name:   "no changes issues exist not negated",
		output: "No issues found. No changes; issues exist.",
	},
	{
		name:   "found issues with is caveat",
		output: "No issues found. We found issues with logging.",
	},
	{
		name:   "not only issues with is caveat",
		output: "No issues found. Not only issues with X but also Y.",
	},
	{
		name:   "distant negation does not negate later issues with",
		output: "No issues found. I did not find issues in the first run and there are issues with logging.",
	},
	{
		name:   "no issues mid-sentence should fail",
		output: "I found no issues with the formatting, but there are bugs.",
	},
	{
		name:   "no issues as part of larger phrase should fail",
		output: "There are no issues with X, but Y needs fixing.",
	},
	{
		name:   "findings before no issues mention",
		output: "Medium - Security issue\nOtherwise no issues found.",
	},
	{
		name:   "no issues found but caveat",
		output: "No issues found in module X, but Y needs fixing.",
	},
	{
		name:   "no issues found however caveat",
		output: "No issues found, however consider refactoring.",
	},
	{
		name:   "no issues found except caveat",
		output: "No issues found except for minor style issues.",
	},
	{
		name:   "no issues found beyond caveat",
		output: "No issues found beyond the two notes above.",
	},
	{
		name:   "no issues found but with period",
		output: "No issues found but.",
	},
	{
		name:   "no issues with em dash caveat",
		output: "No issues found—but there is a bug.",
	},
	{
		name:   "no issues with comma caveat",
		output: "No issues found, but consider refactoring.",
	},
	{
		name:   "no issues with semicolon then failure",
		output: "No issues with this change; tests failed.",
	},
	{
		name:   "no issues then second sentence with break",
		output: "No issues with this change. It breaks X.",
	},
	{
		name:   "no issues then crash",
		output: "No issues with this change—panic on start.",
	},
	{
		name:   "no issues then error",
		output: "No issues with lint. Error in tests.",
	},
	{
		name:   "no issues then bug mention",
		output: "No issues found. Bug in production.",
	},
	{
		name:   "parenthesized but caveat",
		output: "No issues found (but needs review).",
	},
	{
		name:   "quoted caveat",
		output: "No issues found \"but\" consider refactoring.",
	},
	{
		name:   "double negation not without",
		output: "No issues found. Not without errors.",
	},
	{
		name:   "question then error",
		output: "No issues found? Errors occurred.",
	},
	{
		name:   "exclamation then error",
		output: "No issues found! Error in tests.",
	},
}

var failExplicitTests = []verdictTestCase{
	{
		name:   "checked for but found issue",
		output: "No issues found. I checked for bugs but found a race condition.",
	},
	{
		name:   "looked for and found crash",
		output: "No issues found. I looked for crashes and found a panic.",
	},
	{
		name:   "checked for however found problem",
		output: "No issues found. I checked for errors however there is a crash.",
	},
	{
		name:   "has findings with severity",
		output: "Medium - Bug in line 42\nThe code has issues.",
	},
	{
		name:   "empty output",
		output: "",
	},
	{
		name:   "ambiguous language",
		output: "The commit looks mostly fine but could use some cleanup.",
	},
	{
		name:   "severity label medium em dash",
		output: "**Findings**\n- Medium — Possible regression in deploy.\nNo issues found beyond the notes above.",
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
		output: "- Critical — Data loss possible.\nNo issues otherwise.",
	},
	{
		name:   "severity label critical without bullet",
		output: "Critical — Data loss possible.\nNo issues otherwise.",
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
}

func TestParseVerdict(t *testing.T) {
	t.Run("SimplePass", func(t *testing.T) {
		runVerdictTests(t, VerdictPass, simplePassTests)
	})

	t.Run("FieldLabels", func(t *testing.T) {
		runVerdictTests(t, VerdictPass, fieldLabelsTests)
	})

	t.Run("MarkdownFormatting", func(t *testing.T) {
		runVerdictTests(t, VerdictPass, markdownFormattingTests)
	})

	t.Run("PhrasingVariations", func(t *testing.T) {
		runVerdictTests(t, VerdictPass, phrasingVariationsTests)
	})

	t.Run("BenignPhrases", func(t *testing.T) {
		runVerdictTests(t, VerdictPass, benignPhrasesTests)
	})

	t.Run("FailImperative", func(t *testing.T) {
		runVerdictTests(t, VerdictFail, failImperativeTests)
	})

	t.Run("FailCaveats", func(t *testing.T) {
		runVerdictTests(t, VerdictFail, failCaveatsTests)
	})

	t.Run("FailExplicit", func(t *testing.T) {
		runVerdictTests(t, VerdictFail, failExplicitTests)
	})
}
