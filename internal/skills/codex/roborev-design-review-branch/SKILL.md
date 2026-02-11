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

Present the output to the user. The output contains the full review including verdict and findings.

### 4. Offer next steps

If the review has findings (verdict is not Pass), offer to address them:

- "Would you like me to address these findings? You can run `$roborev:address <job_id>`"

Extract the job ID from the `Enqueued job <id> for ...` line in the command output to include in the suggestion.

## Example

User: `$roborev:design-review-branch`

Agent:
1. Executes `roborev review --branch --wait --type design`
2. Presents the review output
3. If findings exist: "Would you like me to address these findings? Run `$roborev:address 1042`"

User: `$roborev:design-review-branch --base develop`

Agent:
1. Validates: `git rev-parse --verify -- develop`
2. Executes `roborev review --branch --wait --type design --base develop`
3. Presents the review output
