---
name: roborev:design-review
description: Request a design review for a commit and present the results
---

# roborev:design-review

Request a design review for a commit and present the results.

## Usage

```
/roborev:design-review [commit]
```

## IMPORTANT

This skill requires you to **execute bash commands** to validate the commit and launch the review. The task is not complete until the background review finishes and you present the results to the user.

## Instructions

When the user invokes `/roborev:design-review [commit]`:

### 1. Validate inputs

If a commit ref is provided, verify it resolves to a valid commit:

```bash
git rev-parse --verify -- <commit>^{commit}
```

If validation fails, inform the user the ref is invalid. Do not proceed.

### 2. Build the command

Construct the review command:

```
roborev review [commit] --wait --type design
```

- If no commit is specified, omit it (defaults to HEAD)

### 3. Run the review in the background

Launch a background task that runs the command. This lets the user continue working while the review runs.

Use the `Task` tool with `run_in_background: true` and `subagent_type: "Bash"`:

```
roborev review [commit] --wait --type design
```

Tell the user that the design review has been submitted and they can continue working. You will present the results when the review completes.

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

**Default design review of HEAD:**

User: `/roborev:design-review`

Agent:
1. Launches background task: `roborev review --wait --type design`
2. Tells user: "Design review submitted for HEAD. I'll present the results when it completes."
3. When complete, presents the verdict and findings grouped by severity
4. If findings exist: "Would you like me to address these findings? Run `/roborev:address 1042`"
5. If passed: "Design review passed with no findings."

**Design review of a specific commit:**

User: `/roborev:design-review abc123`

Agent:
1. Validates `abc123` resolves to a valid commit
2. Launches background task: `roborev review abc123 --wait --type design`
3. Tells user: "Design review submitted for abc123. I'll present the results when it completes."
4. When complete, presents the verdict and findings
5. If findings exist: "Would you like me to address these findings? Run `/roborev:address 1043`"

## See also

- `/roborev:review --type design` — equivalent, with additional `--type` flexibility
- `/roborev:design-review-branch` — design review all commits on the current branch
- `/roborev:address` — fix a review's findings in code
