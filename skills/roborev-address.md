# /roborev:address

Address findings from a roborev code review.

## Usage

```
/roborev:address <job_id>
```

## Description

Fetches a code review by job ID and addresses the findings. The job ID is shown in review notifications (e.g., "Review #1019").

## Instructions

When the user invokes `/roborev:address <job_id>`:

1. **Fetch the review** using the roborev CLI:
   ```bash
   roborev show <job_id>
   ```

2. **Parse the findings** from the review output. Look for:
   - Severity levels (high, medium, low)
   - File paths and line numbers
   - Specific issues to address

3. **Read the relevant files** mentioned in the findings.

4. **Address each finding** by making the necessary code changes:
   - Fix bugs and issues
   - Add missing error handling
   - Improve code quality
   - Add tests if requested

5. **Summarize what was done** and ask the user if they want to:
   - Commit the changes
   - Respond to the review with a summary using `roborev respond <job_id> "message"`

## Example

User: `/roborev:address 1019`

Agent:
1. Runs `roborev show 1019` to fetch the review
2. Reads the files mentioned in the findings
3. Makes the necessary fixes
4. Reports: "I've addressed the 3 findings: fixed the null check in foo.go:42, added error handling in bar.go:15, and updated the test. Would you like me to commit these changes?"
