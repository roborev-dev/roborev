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

## Instructions

When the user invokes `/roborev:design-review [commit]`:

### 1. Validate inputs

If a commit ref is provided, verify it resolves to a valid commit:

```bash
git rev-parse --verify <commit>^{commit}
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

When the background task completes, read the output and present it to the user. The output contains the full review including verdict and findings.

### 5. Offer next steps

If the review has findings (verdict is not Pass), offer to address them:

- "Would you like me to address these findings? You can run `/roborev:address <job_id>`"

Extract the job ID from the `Enqueued job <id> for ...` line in the command output to include in the suggestion.

## Example

User: `/roborev:design-review`

Agent:
1. Launches background task: `roborev review --wait --type design`
2. Tells user: "Design review submitted for HEAD. I'll present the results when it completes."
3. When complete, presents the review output
4. If findings exist: "Would you like me to address these findings? Run `/roborev:address 1042`"

User: `/roborev:design-review abc123`

Agent:
1. Launches background task: `roborev review abc123 --wait --type design`
2. Tells user: "Design review submitted for abc123. I'll present the results when it completes."
3. When complete, presents the review output
