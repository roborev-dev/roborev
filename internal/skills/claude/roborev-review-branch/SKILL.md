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

When the background task completes, read the output and present it to the user. The output contains the full review including verdict and findings.

### 5. Offer next steps

If the review has findings (verdict is not Pass), offer to address them:

- "Would you like me to address these findings? You can run `/roborev:address <job_id>`"

Extract the job ID from the `Enqueued job <id> for ...` line in the command output to include in the suggestion.

## Example

User: `/roborev:review-branch`

Agent:
1. Launches background task: `roborev review --branch --wait`
2. Tells user: "Branch review submitted. I'll present the results when it completes."
3. When complete, presents the review output
4. If findings exist: "Would you like me to address these findings? Run `/roborev:address 1042`"

User: `/roborev:review-branch --base develop --type security`

Agent:
1. Launches background task: `roborev review --branch --wait --base develop --type security`
2. Tells user: "Security review submitted for branch (against develop). I'll present the results when it completes."
3. When complete, presents the review output
