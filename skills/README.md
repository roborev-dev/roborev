# roborev Skills

Install slash commands that let AI agents fix review findings directly.

## Installation

```bash
roborev skills install
```

Skills are updated automatically when you run `roborev update`.

## Available Skills

| Skill | Description |
|-------|-------------|
| `/roborev:fix [job_id...]` | Discover and fix all unaddressed review findings in one pass |
| `/roborev:address <job_id>` | Fetch a single review and fix its findings |
| `/roborev:respond <job_id> [message]` | Add a response to document changes |
| `/roborev:design <feature>` | Design a feature with a PRD and implementation task list |

## Usage

### Fix all unaddressed reviews at once

The most powerful skill is `/roborev:fix`. With no arguments it discovers all unaddressed failed reviews on recent commits and fixes them in a single pass:

```
/roborev:fix
```

You can also target specific jobs:

```
/roborev:fix 1019 1021
```

### Fix a single review

When you want to address one specific review:

```
/roborev:address 1019
```

The agent fetches the review, fixes issues by priority, runs tests, and offers to commit.

### Document changes

After fixing, record what was done:

```
/roborev:respond 1019 Fixed null check and improved error handling
```

### Design a new feature

Collaboratively design a feature with a PRD and detailed task list:

```
/roborev:design webhook notifications for review completion
```

## Checking Skill Status

See which skills are installed and whether any need updating:

```bash
roborev skills
```

## Agent-Specific Syntax

| Agent | Syntax |
|-------|--------|
| Claude Code | `/roborev:fix`, `/roborev:address`, `/roborev:respond`, `/roborev:design` |
| Codex | `$roborev:fix`, `$roborev:address`, `$roborev:respond`, `$roborev:design` |

## Interactive vs Automated

Skills provide an **interactive** workflow — your agent addresses findings and you review the changes before committing. `/roborev:fix` is the fastest interactive option since it batches all outstanding findings into one pass.

For **fully automated** fixing, use `roborev fix --batch` (headless, no agent interaction) or `roborev refine` (iterative loop until all reviews pass).
