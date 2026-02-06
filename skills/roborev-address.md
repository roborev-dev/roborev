# /roborev:address

Fetch a single review and fix its findings.

## Usage

```
/roborev:address <job_id>
```

## Description

Fetches a single code review by job ID and fixes its findings. The job ID is shown in review notifications (e.g., "Review #1019").

## Instructions

When the user invokes `/roborev:address <job_id>`:

1. **Fetch the review** using the roborev CLI:
   ```bash
   roborev show --job <job_id> --json
   ```

2. **Check the verdict** at the top of the review output:
   - If **Pass**: Inform the user no action is needed
   - If **Fail**: Continue to address the findings

3. **Parse the findings** from the review output. Look for:
   - Severity levels (high, medium, low)
   - File paths and line numbers
   - Specific issues to address

4. **Read the relevant files** mentioned in the findings.

5. **Address findings by priority** (high severity first):
   - Fix bugs and issues
   - Add missing error handling
   - Improve code quality
   - Add tests if requested

6. **Handle false positives**: If a finding is invalid or already handled:
   - Explain why to the user
   - Suggest documenting this with `/roborev:respond`

7. **Run tests** if the project has them to verify the fixes work.

8. **Summarize what was done** and ask the user if they want to:
   - Commit the changes
   - Respond to the review with a summary using `/roborev:respond <job_id> <message>`

## Example

User: `/roborev:address 1019`

Agent:
1. Runs `roborev show --job 1019 --json` to fetch the review
2. Sees verdict is Fail with 3 findings (1 high, 2 low)
3. Reads the files mentioned in the findings
4. Addresses the high severity finding first, then the low ones
5. Runs tests to verify the fixes
6. Reports: "I've addressed the 3 findings: fixed the null check in foo.go:42 (high), added error handling in bar.go:15 (low), and updated the test (low). All tests pass. Would you like me to commit these changes?"
