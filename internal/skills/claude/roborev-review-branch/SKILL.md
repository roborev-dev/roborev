---
name: roborev:review-branch
description: Request a code review for all commits on the current branch and present the results
---

# roborev:review-branch

Request a code review for all commits on the current branch and present the results.

## Usage

```
/roborev:review-branch [--base <branch>] [--type security|design]
```

## IMPORTANT

This skill requires you to **execute bash commands** to validate inputs and launch the review. The task is not complete until the background review finishes and you present the results to the user.

## Instructions

When the user invokes `/roborev:review-branch [--base <branch>] [--type security|design]`:

### 1. Validate inputs

If a base branch is provided, verify it resolves to a valid ref:

```bash
git rev-parse --verify -- <branch>
```

If validation fails, inform the user the ref is invalid. Do not proceed.

### 2. Build the command

Construct the review command:

```
roborev review --branch --wait [--base <branch>] [--type <type>]
```

- If `--base` is specified, include it (otherwise auto-detects the base branch)
- If `--type` is specified, include it

### 3. Run the review in the background

Launch a background task that runs the command. This lets the user continue working while the review runs.

Use the `Task` tool with `run_in_background: true` and `subagent_type: "Bash"`:

```
roborev review --branch --wait [--base <branch>] [--type <type>]
```

Tell the user that the branch review has been submitted and they can continue working. You will present the results when the review completes.

### 4. Present the results

When the background task completes, read the output.

If the command output contains an error (e.g., daemon not running, repo not initialized, review errored), report it to the user. Suggest `roborev status` to check the daemon, `roborev init` if the repo is not initialized, or re-running the review.

Otherwise, present the review to the user:
- Show the verdict prominently (Pass or Fail)
- If there are findings, list them grouped by severity with file paths and line numbers so the user can navigate directly
- If the review passed, a brief confirmation is sufficient

### 5. Offer next steps

If the review has findings (verdict is Fail), offer to address them:

- "Would you like me to address these findings? You can run `/roborev:address <job_id>`"

Extract the job ID from the review output to include in the suggestion. Look for it in the `Enqueued job <id> for ...` line or in the review header.

If the review passed, confirm the result and do not offer `/roborev:address`.

## Examples

**Default branch review:**

User: `/roborev:review-branch`

Agent:
1. Launches background task: `roborev review --branch --wait`
2. Tells user: "Branch review submitted. I'll present the results when it completes."
3. When complete, presents the verdict and findings grouped by severity
4. If findings exist: "Would you like me to address these findings? Run `/roborev:address 1042`"
5. If passed: "Branch review passed with no findings."

**Security review against a specific base:**

User: `/roborev:review-branch --base develop --type security`

Agent:
1. Validates `develop` resolves to a valid ref
2. Launches background task: `roborev review --branch --wait --base develop --type security`
3. Tells user: "Security review submitted for branch (against develop). I'll present the results when it completes."
4. When complete, presents the verdict and findings
5. If findings exist: "Would you like me to address these findings? Run `/roborev:address 1043`"

## See also

- `/roborev:design-review-branch` — shorthand for `/roborev:review-branch --type design`
- `/roborev:address` — fix a review's findings in code
- `/roborev:review` — review a single commit
