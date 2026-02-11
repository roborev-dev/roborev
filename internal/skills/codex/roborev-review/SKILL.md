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

Present the output to the user. The output contains the full review including verdict and findings.

### 4. Offer next steps

If the review has findings (verdict is not Pass), offer to address them:

- "Would you like me to address these findings? You can run `$roborev:address <job_id>`"

Extract the job ID from the `Enqueued job <id> for ...` line in the command output to include in the suggestion.

## Example

User: `$roborev:review`

Agent:
1. Executes `roborev review --wait`
2. Presents the review output
3. If findings exist: "Would you like me to address these findings? Run `$roborev:address 1042`"

User: `$roborev:review abc123 --type security`

Agent:
1. Validates: `git rev-parse --verify -- abc123^{commit}`
2. Executes `roborev review abc123 --wait --type security`
3. Presents the review output
