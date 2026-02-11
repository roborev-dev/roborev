---
name: roborev:design-review
description: Request a design review for a commit and present the results
---

# roborev:design-review

Request a design review for a commit and present the results.

## Usage

```
$roborev:design-review [commit]
```

## IMPORTANT

This skill requires you to **execute bash commands** to validate the commit and run the review. The task is not complete until the review finishes and you present the results to the user.

## Instructions

When the user invokes `$roborev:design-review [commit]`:

### 1. Validate inputs

If a commit ref is provided, verify it resolves to a valid commit:

```bash
git rev-parse --verify -- <commit>^{commit}
```

If validation fails, inform the user the ref is invalid. Do not proceed.

### 2. Build and run the command

Construct and execute the review command:

```bash
roborev review [commit] --wait --type design
```

- If no commit is specified, omit it (defaults to HEAD)

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

**Default design review of HEAD:**

User: `$roborev:design-review`

Agent:
1. Executes `roborev review --wait --type design`
2. Presents the verdict and findings grouped by severity
3. If findings exist: "Would you like me to address these findings? Run `$roborev:address 1042`"
4. If passed: "Design review passed with no findings."

**Design review of a specific commit:**

User: `$roborev:design-review abc123`

Agent:
1. Validates: `git rev-parse --verify -- abc123^{commit}`
2. Executes `roborev review abc123 --wait --type design`
3. Presents the verdict and findings
4. If findings exist: "Would you like me to address these findings? Run `$roborev:address 1043`"

## See also

- `$roborev:review --type design` — equivalent, with additional `--type` flexibility
- `$roborev:design-review-branch` — design review all commits on the current branch
- `$roborev:address` — fix a review's findings in code
