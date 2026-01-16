---
name: roborev:respond
description: Add a response or note to a roborev code review to document how findings were addressed
---

# roborev:respond

Add a response to a roborev code review.

## Usage

```
$roborev:respond <job_id> [message]
```

## Description

Adds a response or note to a code review. Common use cases:
- Documenting how findings were addressed
- Explaining why a finding is a false positive
- Noting that a suggestion won't be implemented (and why)

## Instructions

When the user invokes `$roborev:respond <job_id> [message]`:

1. **If a message is provided**, run:
   ```bash
   roborev respond --job <job_id> "<message>"
   ```

2. **If no message is provided**, ask the user what they'd like to say, then run the command.

3. **Confirm the response was added** by checking the command output.

## Examples

User: `$roborev:respond 1019 Fixed all issues`

Agent: Runs `roborev respond --job 1019 "Fixed all issues"` and confirms the response was added.

---

User: `$roborev:respond 1019`

Agent: "What would you like to say in response to review #1019?"

User: "The null check issue was a false positive"

Agent: Runs `roborev respond --job 1019 "The null check issue was a false positive"` and confirms the response was added.
