# roborev Skills

Let AI agents automatically fix issues found in code reviews.

## Installation

```bash
roborev skills install
```

Skills are updated automatically when you run `roborev update`.

## Skills

| Skill | Description |
|-------|-------------|
| `/roborev:address <job_id>` | Fetch a review and fix its findings |
| `/roborev:design-review <path-or-job-id>` | Review a design proposal for completeness and feasibility |
| `/roborev:respond <job_id> [message]` | Add a response to a review |

## Example Workflow

When you receive a review notification:

```
Review #1019: Fail
- high: Missing null check in foo.go:42
- low: Consider adding error context in bar.go:15
```

Ask your agent to fix it:

```
/roborev:address 1019
```

The agent will:
1. Fetch the review
2. Read the relevant files
3. Fix issues by priority (high severity first)
4. Run tests to verify
5. Offer to commit the changes

After fixing, document what was done:

```
/roborev:respond 1019 Fixed null check and improved error handling
```

## Supported Agents

| Agent | Invocation |
|-------|------------|
| Claude Code | `/roborev:address`, `/roborev:design-review`, `/roborev:respond` |
| Codex | `$roborev:address`, `$roborev:design-review`, `$roborev:respond` |
