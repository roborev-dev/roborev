---
name: roborev:review
description: Request a code review for a commit and present the results
---

# roborev:review

Request a code review for a commit and present the results.

## Usage

```
/roborev:review [commit] [--type security|design]
```

## IMPORTANT

This skill requires you to **execute bash commands** to validate the commit and launch the review. The task is not complete until the background review finishes and you present the results to the user.

## Instructions

When the user invokes `/roborev:review [commit] [--type security|design]`:

### 1. Validate inputs

If a commit ref is provided, verify it resolves to a valid commit:

```bash
git rev-parse --verify -- <commit>^{commit}
```

If validation fails, inform the user the ref is invalid. Do not proceed.

### 2. Build the command

Construct the review command:

```
roborev review [commit] --wait [--type <type>]
```

- If no commit is specified, omit it (defaults to HEAD)
- If `--type` is specified, include it

### 3. Run the review in the background

Launch a background task that runs the command. This lets the user continue working while the review runs.

Use the `Task` tool with `run_in_background: true` and `subagent_type: "Bash"`:

```
roborev review [commit] --wait [--type <type>]
```

Tell the user that the review has been submitted and they can continue working. You will present the results when the review completes.

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

**Default review of HEAD:**

User: `/roborev:review`

Agent:
1. Launches background task: `roborev review --wait`
2. Tells user: "Review submitted for HEAD. I'll present the results when it completes."
3. When complete, presents the verdict and findings grouped by severity
4. If findings exist: "Would you like me to address these findings? Run `/roborev:address 1042`"
5. If passed: "Review passed with no findings."

**Security review of a specific commit:**

User: `/roborev:review abc123 --type security`

Agent:
1. Validates `abc123` resolves to a valid commit
2. Launches background task: `roborev review abc123 --wait --type security`
3. Tells user: "Security review submitted for abc123. I'll present the results when it completes."
4. When complete, presents the verdict and findings
5. If findings exist: "Would you like me to address these findings? Run `/roborev:address 1043`"

## See also

- `/roborev:design-review` — shorthand for `/roborev:review --type design`
- `/roborev:address` — fix a review's findings in code
- `/roborev:review-branch` — review all commits on the current branch
