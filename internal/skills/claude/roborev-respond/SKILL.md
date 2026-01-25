---
name: roborev:respond
description: Add a comment to a roborev code review and mark it as addressed
---

# roborev:respond

Record a comment on a roborev code review and mark it as addressed.

## Usage

```
/roborev:respond <job_id> [message]
```

## IMPORTANT

This skill requires you to **execute bash commands** to record the comment and mark the review addressed. The task is not complete until you run both commands and see confirmation output.

## Instructions

When the user invokes `/roborev:respond <job_id> [message]`:

1. **If a message is provided**, immediately execute:
   ```bash
   roborev comment --job <job_id> "<message>" && roborev address <job_id>
   ```

2. **If no message is provided**, ask the user what they'd like to say, then execute the commands with their comment.

3. **Verify success** - both commands will output confirmation. If either fails, report the error.

The comment is recorded in roborev's database and the review is marked as addressed. View results with `roborev show`.

## Examples

**With message provided:**

User: `/roborev:respond 1019 Fixed all issues`

Agent action:
```bash
roborev comment --job 1019 "Fixed all issues" && roborev address 1019
```
Then confirm: "Comment recorded and review #1019 marked as addressed."

---

**Without message:**

User: `/roborev:respond 1019`

Agent: "What would you like to say about review #1019?"

User: "The null check was a false positive"

Agent action:
```bash
roborev comment --job 1019 "The null check was a false positive" && roborev address 1019
```
Then confirm: "Comment recorded and review #1019 marked as addressed."
