---
name: roborev:respond
description: Add a response or note to a roborev code review to document how findings were addressed
---

# roborev:respond

Record a response to a roborev code review.

## Usage

```
$roborev:respond <job_id> [message]
```

## IMPORTANT

This skill requires you to **execute a bash command** to record the response in roborev. The task is not complete until you run the `roborev respond` command and see confirmation output.

## Instructions

When the user invokes `$roborev:respond <job_id> [message]`:

1. **If a message is provided**, immediately execute:
   ```bash
   roborev respond --job <job_id> "<message>"
   ```

2. **If no message is provided**, ask the user what they'd like to say, then execute the command with their response.

3. **Verify success** - the command will output confirmation. If it fails, report the error.

The response is recorded in roborev's database and will appear when viewing the review with `roborev show`.

## Examples

**With message provided:**

User: `$roborev:respond 1019 Fixed all issues`

Agent action:
```bash
roborev respond --job 1019 "Fixed all issues"
```
Then confirm: "Response recorded for review #1019."

---

**Without message:**

User: `$roborev:respond 1019`

Agent: "What would you like to say in response to review #1019?"

User: "The null check was a false positive"

Agent action:
```bash
roborev respond --job 1019 "The null check was a false positive"
```
Then confirm: "Response recorded for review #1019."
