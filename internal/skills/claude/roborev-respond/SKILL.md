---
name: roborev:respond
description: Add a comment to a roborev code review and close it
---

# roborev:respond

Record a comment on a roborev code review and close it.

## Usage

```
/roborev:respond <job_id> [message]
```

## IMPORTANT

This skill requires you to **execute bash commands** to record the comment and close the review. The task is not complete until you run both commands and see confirmation output.

## Instructions

When the user invokes `/roborev:respond <job_id> [message]`:

### 1. Validate input

If no job_id is provided, inform the user that a job ID is required. Suggest `roborev status` or `roborev fix --open --list` to find job IDs.

### 2. Record the comment and close the review

**If a message is provided**, immediately execute:
```bash
roborev comment --job <job_id> "<message>" && roborev close <job_id>
```

If the message contains quotes or special characters, escape them properly in the bash command.

**If no message is provided**, ask the user what they'd like to say, then execute the commands with their comment.

### 3. Verify success

Both commands will output confirmation. If either fails, report the error to the user. Common causes:
- The daemon is not running
- The job ID does not exist
- The repo is not initialized (suggest `roborev init`)
- The review is already closed (not an error, but worth noting to the user)

The comment is recorded in roborev's database and the review is closed. View results with `roborev show`.

## Examples

**With message provided:**

User: `/roborev:respond 1019 Fixed all issues`

Agent action:
```bash
roborev comment --job 1019 "Fixed all issues" && roborev close 1019
```
Then confirm: "Comment recorded and review #1019 closed."

---

**Without message:**

User: `/roborev:respond 1019`

Agent: "What would you like to say about review #1019?"

User: "The null check was a false positive"

Agent action:
```bash
roborev comment --job 1019 "The null check was a false positive" && roborev close 1019
```
Then confirm: "Comment recorded and review #1019 closed."

## See also

- `/roborev:fix` — fix a review's findings in code, then comment and close it
