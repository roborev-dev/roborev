# roborev Skills

Agent skills for addressing and responding to roborev code reviews.

## Available Skills

| Skill | Description |
|-------|-------------|
| `/roborev:address <job_id>` | Fetch a review and address its findings |
| `/roborev:respond <job_id> [message]` | Add a response to a review |

## Installation

### Claude Code Marketplace

Install the `roborev` skill pack from the Claude Code marketplace.

### Manual Installation

Copy the skill files to your project's `.claude/skills/` directory:

```bash
mkdir -p .claude/skills
cp roborev-address.md roborev-respond.md .claude/skills/
```

## Usage

When you receive a review notification like:

```
Review #1019 roborev abc123 (codex)
Verdict: Fail
**Findings**
- high: Missing null check in foo.go:42
```

Instead of copy-pasting, simply run:

```
/roborev:address 1019
```

The agent will fetch the review, read the relevant files, and address the findings.

After addressing, respond to the review:

```
/roborev:respond 1019 Fixed null check and added test
```

## Prerequisites

- `roborev` CLI installed and in PATH
- roborev daemon running (`roborev daemon start`)
