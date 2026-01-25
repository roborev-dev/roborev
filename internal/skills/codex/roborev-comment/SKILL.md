---
name: roborev:comment
description: Add a comment or note to a roborev code review to document how findings were addressed
---

# roborev:comment

Record a comment on a roborev code review.

## Usage

```
$roborev:comment <job_id> [message]
```

## IMPORTANT

This skill requires you to **execute a bash command** to record the comment in roborev. The task is not complete until you run the `roborev comment` command and see confirmation output.

## Instructions

When the user invokes `$roborev:comment <job_id> [message]`:

1. **If a message is provided**, immediately execute:
   ```bash
   roborev comment --job <job_id> "<message>"
   ```

2. **If no message is provided**, ask the user what they'd like to say, then execute the command with their comment.

3. **Verify success** - the command will output confirmation. If it fails, report the error.

The comment is recorded in roborev's database and will appear when viewing the review with `roborev show`.

## Examples

**With message provided:**

User: `$roborev:comment 1019 Fixed all issues`

Agent action:
```bash
roborev comment --job 1019 "Fixed all issues"
```
Then confirm: "Comment recorded for review #1019."

---

**Without message:**

User: `$roborev:comment 1019`

Agent: "What would you like to say about review #1019?"

User: "The null check was a false positive"

Agent action:
```bash
roborev comment --job 1019 "The null check was a false positive"
```
Then confirm: "Comment recorded for review #1019."
