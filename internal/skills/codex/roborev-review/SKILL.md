---
name: roborev:review
description: Request a code review for a commit and present the results
---

# roborev:review

Request a code review for a commit and present the results.

## Usage

```
$roborev:review [commit] [--type security|design]
```

## IMPORTANT

This skill requires you to **execute bash commands** to validate the commit and run the review. The task is not complete until the review finishes and you present the results to the user.

## Instructions

When the user invokes `$roborev:review [commit] [--type security|design]`:

### 1. Validate inputs

If a commit ref is provided, verify it resolves to a valid commit:

```bash
git rev-parse --verify -- <commit>^{commit}
```

If validation fails, inform the user the ref is invalid. Do not proceed.

### 2. Build and run the command

Construct and execute the review command:

```bash
roborev review [commit] --wait [--type <type>]
```

- If no commit is specified, omit it (defaults to HEAD)
- If `--type` is specified, include it

The `--wait` flag blocks until the review completes.

### 3. Present the results

If the command output contains an error (e.g., daemon not running, repo not initialized, review errored), report it to the user. Suggest `roborev status` to check the daemon, `roborev init` if the repo is not initialized, or re-running the review.

Otherwise, present the review to the user:
- Show the verdict prominently (Pass or Fail)
- If there are findings, list them grouped by severity with file paths and line numbers so the user can navigate directly
- If the review passed, a brief confirmation is sufficient

### 4. Offer next steps

If the review has findings (verdict is Fail), offer to address them:

- "Would you like me to address these findings? You can run `$roborev:address <job_id>`"

Extract the job ID from the review output to include in the suggestion. Look for it in the `Enqueued job <id> for ...` line or in the review header.

If the review passed, confirm the result and do not offer `$roborev:address`.

## Examples

**Default review of HEAD:**

User: `$roborev:review`

Agent:
1. Executes `roborev review --wait`
2. Presents the verdict and findings grouped by severity
3. If findings exist: "Would you like me to address these findings? Run `$roborev:address 1042`"
4. If passed: "Review passed with no findings."

**Security review of a specific commit:**

User: `$roborev:review abc123 --type security`

Agent:
1. Validates: `git rev-parse --verify -- abc123^{commit}`
2. Executes `roborev review abc123 --wait --type security`
3. Presents the verdict and findings
4. If findings exist: "Would you like me to address these findings? Run `$roborev:address 1043`"

## See also

- `$roborev:design-review` — shorthand for `$roborev:review --type design`
- `$roborev:address` — fix a review's findings in code
- `$roborev:review-branch` — review all commits on the current branch
