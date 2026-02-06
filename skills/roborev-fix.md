# /roborev:fix

Discover and fix all unaddressed review findings in one pass.

## Usage

```
/roborev:fix [job_id...]
```

## Description

Discovers unaddressed code reviews and fixes all their findings in a single pass. Unlike `/roborev:address` which handles one review at a time, this skill batches all outstanding findings together, groups them by file, and fixes them by severity priority. This is the most powerful interactive skill — the agent sees all findings at once and can make coordinated fixes across related issues.

If job IDs are provided, only those reviews are addressed. Otherwise, the skill checks recent commits (HEAD, HEAD~1) for failed reviews that have not been commented on.

## Instructions

When the user invokes `/roborev:fix [job_id...]`:

1. **Discover reviews** to address:
   - If job IDs given, use those
   - Otherwise, run `roborev show HEAD` and `roborev show HEAD~1` to find failed, unaddressed reviews
   - If no failed reviews found, inform the user

2. **Fetch all reviews** using `roborev show --job <id> --json` for each job.

3. **Parse and prioritize findings** from all reviews:
   - Collect severity, file paths, and line numbers
   - Group by file to minimize context switches
   - Order by severity (high first)

4. **Fix all findings** across all reviews.

5. **Run tests** to verify the fixes work.

6. **Record comments** for each addressed job:
   ```bash
   roborev comment --job <job_id> "<summary of changes>"
   ```

7. **Ask to commit** all changes together.

## Example

User: `/roborev:fix`

Agent:
1. Runs `roborev show HEAD` and `roborev show HEAD~1`
2. Finds 2 failed reviews: job 1019 (2 findings) and job 1021 (1 finding)
3. Fetches both reviews with `roborev show --job 1019 --json` and `roborev show --job 1021 --json`
4. Fixes all 3 findings across both reviews, prioritizing by severity
5. Runs tests to verify
6. Records comments on both jobs
7. Asks: "I've addressed 3 findings across 2 reviews. Tests pass. Would you like me to commit these changes?"
