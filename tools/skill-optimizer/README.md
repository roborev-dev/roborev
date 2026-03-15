# Skill Optimizer

Autoresearch-style autonomous optimization loop for SKILL.md files.

## Quick Start

```bash
# Install dependencies
cd tools/skill-optimizer
uv sync

# Run evaluation on a skill
uv run evaluate.py \
  --skill ../../internal/skills/claude/roborev-fix/SKILL.md \
  --scenarios scenarios/roborev-fix.json

# Run with a different model
uv run evaluate.py \
  --skill ../../internal/skills/claude/roborev-fix/SKILL.md \
  --scenarios scenarios/roborev-fix.json \
  --model claude-sonnet-4-6-20250514
```

## How It Works

This tool mirrors [Karpathy's autoresearch](https://github.com/karpathy/autoresearch):

| autoresearch | skill-optimizer |
|---|---|
| `prepare.py` | `evaluate.py` |
| `train.py` | SKILL.md |
| `program.md` | `program.md` |
| val_bpb | composite score |

## Running the Optimizer

1. Start a Claude Code session
2. Give it `program.md` as instructions
3. Point it at the skill to optimize
4. Let it run autonomously

## Scoring

Composite score (0.0 - 1.0, higher is better):

- **Task completion** (50%): Did the agent produce the right commands in order?
- **Output quality** (30%): LLM-as-judge rates the approach quality
- **Efficiency** (20%): Fewer tool calls = better

## Adding Scenarios for Other Skills

Create a new JSON file in `scenarios/`:

```json
[
  {
    "name": "scenario_name",
    "user_message": "/skill-name",
    "repo_state": {"description": "...", "language": "go"},
    "mock_outputs": {"command": "output"},
    "expected_commands": ["cmd1", "cmd2"],
    "min_tool_calls": 3
  }
]
```

## Tests

```bash
cd tools/skill-optimizer
uv run python -m pytest tests/ -v
```
