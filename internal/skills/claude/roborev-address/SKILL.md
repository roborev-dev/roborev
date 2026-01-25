---
name: roborev:address
description: Address findings from a roborev code review by fetching the review and making necessary code changes
---

# roborev:address

Fetch a code review and fix its findings.

## Usage

```
/roborev:address <job_id>
```

## Instructions

When the user invokes `/roborev:address <job_id>`:

### 1. Fetch the review

Execute:
```bash
roborev show --job <job_id>
```

### 2. Check the verdict

- If **Pass**: Inform the user no action is needed
- If **Fail**: Continue to address the findings

### 3. Fix the findings

Parse the findings from the output (severity, file paths, line numbers), then:

1. Read the relevant files
2. Fix issues by priority (high severity first)
3. Run tests if the project has them

### 4. Complete the workflow

After fixing, **record what was done** by executing:
```bash
roborev comment --job <job_id> "<summary of changes>"
```

This records your comment in roborev so the review shows it was addressed.

Then ask the user if they want to commit the changes.

## Example

User: `/roborev:address 1019`

Agent:
1. Executes `roborev show --job 1019`
2. Sees verdict is Fail with 2 findings
3. Reads files, fixes the issues, runs tests
4. Executes `roborev comment --job 1019 "Fixed null check in foo.go and added error handling in bar.go"`
5. Asks: "I've addressed both findings and recorded a comment. Tests pass. Would you like me to commit these changes?"
