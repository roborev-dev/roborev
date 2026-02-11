---
name: roborev:fix
description: Fix multiple review findings in one pass by discovering unaddressed reviews and addressing them all
---

# roborev:fix

Fix all unaddressed review findings in one pass.

## Usage

```
/roborev:fix [job_id...]
```

## IMPORTANT

This skill requires you to **execute bash commands** to discover reviews, fetch them, fix code, record comments, and mark reviews as addressed. The task is not complete until you run all commands and see confirmation output.

## Instructions

When the user invokes `/roborev:fix [job_id...]`:

### 1. Discover reviews

If job IDs are provided, use those. Otherwise, discover unaddressed reviews:

```bash
roborev fix --unaddressed --list
```

This prints one line per unaddressed job with its ID, commit SHA, agent, and summary. Collect the job IDs from the output.

If the command fails, report the error to the user. Common causes: the daemon is not running, or the repo is not initialized (suggest `roborev init`).

If no unaddressed reviews are found, inform the user there is nothing to fix.

### 2. Fetch all reviews

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
- `addressed`: whether this review has already been addressed

Skip any reviews where `job.verdict` is `"P"` (passing reviews have no findings to fix).
Skip any reviews where `job.verdict` is empty or missing (the review may have errored and is not actionable).
Skip any reviews where `addressed` is `true`, unless the user explicitly provided that job ID (in which case, warn them and ask to confirm).

If all reviews are skipped, inform the user there is nothing to fix.

### 3. Fix all findings

For each review, use `job.git_ref` to understand the scope of the reviewed changes — run `git show <git_ref>` to see the original diff.

Parse findings from the `output` field of all failing reviews. Collect every finding with its severity, file path, and line number. Then:

1. Group findings by file to minimize context switches
2. Fix issues by priority (high severity first)
3. If the same file has findings from multiple reviews, fix them all together
4. If some findings cannot be fixed (false positives, intentional design), note them for the comment rather than silently skipping them

### 4. Run tests

Run the project's test suite to verify all fixes work:

```bash
go test ./...
```

Or whatever test command the project uses. If tests fail, fix the regressions before proceeding.

### 5. Record comments and mark addressed

For each job that was addressed, record a summary comment and mark it as addressed:

```bash
roborev comment --job <job_id> "<summary of changes>"
roborev address <job_id>
```

The comment should briefly describe what was changed and why, referencing specific files and findings. Keep it under 2-3 sentences per review.

### 6. Ask to commit

Ask the user if they want to commit all the changes together.

## Examples

**Auto-discovery:**

User: `/roborev:fix`

Agent:
1. Runs `roborev fix --unaddressed --list` and finds 2 unaddressed reviews: job 1019 and job 1021
2. Fetches both reviews with `roborev show --job 1019 --json` and `roborev show --job 1021 --json`
3. Runs `git show <git_ref>` for each to see the reviewed diffs
4. Fixes all 3 findings across both reviews, grouped by file, prioritized by severity
5. Runs `go test ./...` to verify
6. Records comments and marks addressed:
   - `roborev comment --job 1019 "Fixed null check and added error handling"` then `roborev address 1019`
   - `roborev comment --job 1021 "Fixed missing validation"` then `roborev address 1021`
7. Asks: "I've addressed 3 findings across 2 reviews. Tests pass. Would you like me to commit these changes?"

**Explicit job IDs:**

User: `/roborev:fix 1019 1021`

Agent:
1. Skips discovery, fetches job 1019 and 1021 directly
2. Job 1019 is verdict Fail with 2 findings; job 1021 is verdict Pass — skips 1021, informs user
3. Fixes the 2 findings from job 1019
4. Runs `go test ./...` to verify
5. Records: `roborev comment --job 1019 "Fixed null check in foo.go and error handling in bar.go"` then `roborev address 1019`
6. Asks: "I've addressed 2 findings from 1 review (skipped job 1021 — already passing). Tests pass. Would you like me to commit?"

## See also

- `/roborev:address` — address a single review's findings
