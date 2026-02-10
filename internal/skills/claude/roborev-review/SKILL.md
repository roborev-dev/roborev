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

## Instructions

When the user invokes `/roborev:review [commit] [--type security|design]`:

### 1. Build the command

Construct the review command:

```
roborev review [commit] --wait [--type <type>]
```

- If no commit is specified, omit it (defaults to HEAD)
- If `--type` is specified, include it

### 2. Run the review in the background

Launch a background task that runs the command. This lets the user continue working while the review runs.

Use the `Task` tool with `run_in_background: true` and `subagent_type: "Bash"`:

```
roborev review [commit] --wait [--type <type>]
```

Tell the user that the review has been submitted and they can continue working. You will present the results when the review completes.

### 3. Present the results

When the background task completes, read the output and present it to the user. The output contains the full review including verdict and findings.

### 4. Offer next steps

If the review has findings (verdict is not Pass), offer to address them:

- "Would you like me to address these findings? You can run `/roborev:address <job_id>`"

Extract the job ID from the review output to include in the suggestion.

## Example

User: `/roborev:review`

Agent:
1. Launches background task: `roborev review --wait`
2. Tells user: "Review submitted for HEAD. I'll present the results when it completes."
3. When complete, presents the review output
4. If findings exist: "Would you like me to address these findings? Run `/roborev:address 1042`"

User: `/roborev:review abc123 --type security`

Agent:
1. Launches background task: `roborev review abc123 --wait --type security`
2. Tells user: "Security review submitted for abc123. I'll present the results when it completes."
3. When complete, presents the review output
