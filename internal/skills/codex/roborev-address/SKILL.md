---
name: roborev:address
description: Address findings from a roborev code review by fetching the review and making necessary code changes
---

# roborev:address

Fetch a code review and fix its findings.

## Usage

```
$roborev:address <job_id>
```

## IMPORTANT

This skill requires you to **execute bash commands** to fetch the review, fix code, record comments, and mark the review as addressed. The task is not complete until you run all commands and see confirmation output.

## Instructions

When the user invokes `$roborev:address <job_id>`:

### 1. Validate input

If no job_id is provided, inform the user that a job ID is required. Suggest `roborev status` or `roborev fix --unaddressed --list` to find job IDs.

### 2. Fetch the review

Execute:
```bash
roborev show --job <job_id> --json
```

If the command fails, report the error to the user. Common causes: the daemon is not running, the job ID does not exist, or the repo is not initialized (suggest `roborev init`).

The JSON output has this structure:
- `job_id`: the job ID
- `output`: the review text containing findings
- `job.verdict`: `"P"` for pass, `"F"` for fail (may be empty if the review errored)
- `job.git_ref`: the reviewed git ref (SHA, range, or synthetic ref)
- `addressed`: whether this review has already been addressed

### 3. Check the verdict

- If `addressed` is `true`: Inform the user this review was already addressed. Ask if they want to proceed anyway.
- If `job.verdict` is `"P"`: Inform the user no action is needed.
- If `job.verdict` is `"F"`: Continue to address the findings.
- If `job.verdict` is empty or missing: Inform the user the review is not actionable (it may have errored). Do not proceed.

### 4. Fix the findings

Use `job.git_ref` to understand the scope — run `git show <git_ref>` to see the original changes that were reviewed.

Parse the findings from the `output` field (severity, file paths, line numbers), then:

1. Read the relevant files
2. Fix issues by priority (high severity first)
3. If some findings cannot be fixed (false positives, intentional design), note them for the comment

### 5. Run tests

Run the project's test suite to verify fixes:
```bash
go test ./...
```
Or whatever test command the project uses. If tests fail, fix the regressions before proceeding.

### 6. Complete the workflow

After fixing, **record what was done and mark the review addressed** by executing:
```bash
roborev comment --job <job_id> "<summary of changes>"
roborev address <job_id>
```

The comment should briefly describe what was changed and why, referencing specific files. Keep it under 2-3 sentences.

Then ask the user if they want to commit the changes.

## Example

User: `$roborev:address 1019`

Agent:
1. Executes `roborev show --job 1019 --json`
2. Sees verdict is Fail with 2 findings, not yet addressed
3. Runs `git show <git_ref>` to see the reviewed diff
4. Reads files, fixes the issues, runs `go test ./...`
5. Executes `roborev comment --job 1019 "Fixed null check in foo.go and added error handling in bar.go"` then `roborev address 1019`
6. Asks: "I've addressed both findings and recorded a comment. Tests pass. Would you like me to commit these changes?"

## See also

- `$roborev:respond` — comment on a review and mark addressed without fixing code
- `$roborev:fix` — batch-fix all unaddressed reviews in one pass
