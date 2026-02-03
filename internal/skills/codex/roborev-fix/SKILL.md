---
name: roborev:fix
description: Fix multiple review findings in one pass by discovering unaddressed reviews and addressing them all
---

# roborev:fix

Fix all unaddressed review findings in one pass.

## Usage

```
$roborev:fix [job_id...]
```

## Instructions

When the user invokes `$roborev:fix [job_id...]`:

### 1. Discover reviews

If job IDs are provided, use those. Otherwise, discover unaddressed reviews:

```bash
roborev fix --unaddressed --list
```

This prints one line per unaddressed job with its ID, commit SHA, agent, and summary. Collect the job IDs from the output.

If no unaddressed reviews are found, inform the user there is nothing to fix.

### 2. Fetch all reviews

For each job ID, fetch the full review:

```bash
roborev show --job <job_id>
```

### 3. Fix all findings

Parse findings from all reviews. Collect every finding with its severity, file path, and line number. Then:

1. Group findings by file to minimize context switches
2. Fix issues by priority (high severity first)
3. If the same file has findings from multiple reviews, fix them all together

### 4. Run tests

Run the project's test suite to verify all fixes work:

```bash
go test ./...
```

Or whatever test command the project uses.

### 5. Record comments and mark addressed

For each job that was addressed, record a summary comment and mark it as addressed:

```bash
roborev comment --job <job_id> "<summary of changes>"
roborev address <job_id>
```

### 6. Ask to commit

Ask the user if they want to commit all the changes together.

## Example

User: `$roborev:fix`

Agent:
1. Runs `roborev fix --unaddressed --list` and finds 2 unaddressed reviews: job 1019 and job 1021
2. Fetches both reviews with `roborev show --job 1019` and `roborev show --job 1021`
3. Fixes all 3 findings across both reviews, prioritizing by severity
4. Runs tests to verify
5. Records comments and marks addressed:
   - `roborev comment --job 1019 "Fixed null check and added error handling"` then `roborev address 1019`
   - `roborev comment --job 1021 "Fixed missing validation"` then `roborev address 1021`
6. Asks: "I've addressed 3 findings across 2 reviews. Tests pass. Would you like me to commit these changes?"
