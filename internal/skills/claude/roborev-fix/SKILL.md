---
name: roborev-fix
description: Fix multiple review findings in one pass by discovering open reviews and fixing them all
---

# roborev-fix

Fix all open review findings in one pass.

## Usage

```
/roborev-fix [job_id...]
```

## IMPORTANT

You must **execute bash commands** to complete this task. Skip steps already satisfied by conversation context. Defer to CLAUDE.md when it conflicts.

## Instructions

When the user invokes `/roborev-fix [job_id...]`:

### 1. Gather findings

**Check the conversation first.** If the user has already pasted review
findings (verdicts, severities, file paths, suggested fixes), use those
directly. Do not re-fetch reviews that are already present in the
conversation. When reusing pasted findings, collect any job IDs mentioned
alongside them — step 5 needs these to comment on and close the reviews.
If job IDs are missing from the pasted output, discover them via
`roborev fix --open --list` and match each pasted finding to the correct
job by commit SHA or reviewed file paths. If a finding cannot be
confidently matched to a specific job, ask the user for the job ID
rather than closing the wrong review.

If job IDs are provided and findings are NOT already in the conversation,
fetch them:

```bash
roborev show --job <job_id> --json
```

If no job IDs are provided and no findings are in the conversation, discover
open reviews:

```bash
roborev fix --open --list
```

This prints one line per open job with its ID, commit SHA, agent, and summary.
Collect the job IDs from the output.

If the command fails, report the error to the user. Common causes: the daemon
is not running, or the repo is not initialized (suggest `roborev init`).

If no open reviews are found, inform the user there is nothing to fix.

### 2. Fetch reviews (if needed)

Skip this step if findings are already available from step 1.

For each job ID, fetch the full review as JSON:

```bash
roborev show --job <job_id> --json
```

If the command fails for a job ID, report the error and continue with the remaining jobs.

The JSON output has this structure:
- `job_id`: the job ID
- `output`: the review text containing findings
- `job.verdict`: `"P"` for pass, `"F"` for fail (may be empty if the review errored)
- `job.git_ref`: the reviewed git ref (SHA, range, or synthetic ref)
- `closed`: whether this review has already been closed

Skip any reviews where `job.verdict` is `"P"` (passing reviews have no findings to fix).
Skip any reviews where `job.verdict` is empty or missing (the review may have errored and is not actionable).
Skip any reviews where `closed` is `true`, unless the user explicitly provided that job ID (in which case, warn them and ask to confirm).

If all reviews are skipped, inform the user there is nothing to fix.

### 3. Fix all findings

If a finding's context is unclear from the review output alone and `job.git_ref` is not `"dirty"`, run `git show <git_ref>` to see the original diff. Only do this when needed — the review output usually contains enough detail (file paths, line numbers, descriptions) to fix findings directly.

Parse findings from the `output` field of all failing reviews. Collect every finding with its severity, file path, and line number. Then:

1. **Sort by severity**: fix HIGH findings first, then MEDIUM, then LOW
2. **Group by file**: within each severity level, batch edits to the same file to minimize context switches
3. If the same file has findings from multiple reviews, fix them all together in one edit
4. If some findings cannot be fixed (false positives, intentional design), note them for the comment rather than silently skipping them

### 4. Run tests

Run the project's test suite to verify all fixes work:

```bash
go test ./...
```

Or whatever test command the project uses. If tests fail, fix the regressions before proceeding.

### 5. Record comments and close reviews

For each job that was fixed, record a summary comment and then close it.
Run these as **separate commands** (not chained):

```bash
roborev comment --job <job_id> "<summary of changes>"
roborev close <job_id>
```

The comment should reference each finding by severity and file, state what was fixed, and note any findings intentionally skipped. Keep it concise (1-3 sentences). Escape quotes and special characters in the bash command.

### 6. Commit

Follow the project's commit conventions (see CLAUDE.md). If the project
instructs you to always commit, do so without asking.

## Examples

**Auto-discovery:**

User: `/roborev-fix`

Agent:
1. Runs `roborev fix --open --list` and finds 2 open reviews: job 1019 and job 1021
2. Fetches both reviews with `roborev show --job 1019 --json` and `roborev show --job 1021 --json`
3. Runs `git show <git_ref>` for each to see the reviewed diffs
4. Fixes all 3 findings across both reviews, grouped by file, prioritized by severity
5. Runs `go test ./...` to verify
6. Records comments and closes reviews:
   - `roborev comment --job 1019 "Fixed null check and added error handling"`
   - `roborev close 1019`
   - `roborev comment --job 1021 "Fixed missing validation"`
   - `roborev close 1021`
7. Commits the changes per project conventions

**Explicit job IDs:**

User: `/roborev-fix 1019 1021`

Agent:
1. Skips discovery, fetches job 1019 and 1021 directly
2. Job 1019 is verdict Fail with 2 findings; job 1021 is verdict Pass — skips 1021, informs user
3. Fixes the 2 findings from job 1019
4. Runs `go test ./...` to verify
5. Records comment and closes review:
   - `roborev comment --job 1019 "Fixed null check in foo.go and error handling in bar.go"`
   - `roborev close 1019`
6. Commits the changes per project conventions

## See also

- `/roborev-respond` — comment on a review and close it without fixing code
