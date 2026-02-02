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
roborev show HEAD
roborev show HEAD~1
```

Look for jobs with verdict **Fail** that have no comments (unaddressed). Collect their job IDs.

If no failed reviews are found, inform the user there is nothing to fix.

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

### 5. Record comments

For each job that was addressed, record a summary comment:

```bash
roborev comment --job <job_id> "<summary of changes>"
```

### 6. Ask to commit

Ask the user if they want to commit all the changes together.

## Example

User: `$roborev:fix`

Agent:
1. Runs `roborev show HEAD` and `roborev show HEAD~1`
2. Finds 2 failed reviews: job 1019 (2 findings) and job 1021 (1 finding)
3. Fetches both reviews with `roborev show --job 1019` and `roborev show --job 1021`
4. Fixes all 3 findings across both reviews, prioritizing by severity
5. Runs tests to verify
6. Executes `roborev comment --job 1019 "Fixed null check and added error handling"` and `roborev comment --job 1021 "Fixed missing validation"`
7. Asks: "I've addressed 3 findings across 2 reviews. Tests pass. Would you like me to commit these changes?"
