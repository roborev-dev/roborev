---
name: roborev:design-review-branch
description: Request a design review for all commits on the current branch and present the results
---

# roborev:design-review-branch

Request a design review for all commits on the current branch and present the results.

## Usage

```
$roborev:design-review-branch [--base <branch>]
```

## IMPORTANT

This skill requires you to **execute bash commands** to validate inputs and run the review. The task is not complete until the review finishes and you present the results to the user.

## Instructions

When the user invokes `$roborev:design-review-branch [--base <branch>]`:

### 1. Validate inputs

If a base branch is provided, verify it resolves to a valid ref:

```bash
git rev-parse --verify -- <branch>
```

If validation fails, inform the user the ref is invalid. Do not proceed.

### 2. Build and run the command

Construct and execute the review command:

```bash
roborev review --branch --wait --type design [--base <branch>]
```

- If `--base` is specified, include it (otherwise auto-detects the base branch)

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

**Default branch design review:**

User: `$roborev:design-review-branch`

Agent:
1. Executes `roborev review --branch --wait --type design`
2. Presents the verdict and findings grouped by severity
3. If findings exist: "Would you like me to address these findings? Run `$roborev:address 1042`"
4. If passed: "Branch design review passed with no findings."

**Design review against a specific base:**

User: `$roborev:design-review-branch --base develop`

Agent:
1. Validates: `git rev-parse --verify -- develop`
2. Executes `roborev review --branch --wait --type design --base develop`
3. Presents the verdict and findings
4. If findings exist: "Would you like me to address these findings? Run `$roborev:address 1043`"

## See also

- `$roborev:review-branch --type design` — equivalent, with additional `--type` flexibility
- `$roborev:design-review` — design review a single commit
- `$roborev:address` — fix a review's findings in code
