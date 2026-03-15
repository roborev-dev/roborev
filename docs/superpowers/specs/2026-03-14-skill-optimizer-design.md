# Skill Optimizer Design

Autoresearch-style autonomous optimization loop for roborev skill files.

## Problem

Skill files (SKILL.md) are hand-written natural language instructions that guide AI agents through roborev workflows. Small wording changes can significantly affect agent behavior — but we have no systematic way to test or improve them. We need a feedback loop.

## Approach

Mirror Karpathy's autoresearch pattern exactly: one file to modify, one scalar metric, autonomous keep/discard loop.

| autoresearch | skill-optimizer |
|---|---|
| `prepare.py` (fixed eval) | `evaluate.py` (fixed eval harness) |
| `train.py` (modified by agent) | `SKILL.md` copy (modified by optimizer agent) |
| `program.md` (meta-instructions) | `program.md` (meta-instructions) |
| `results.tsv` (experiment log) | `results.tsv` (experiment log) |
| val_bpb (lower is better) | composite score (higher is better) |

## File Structure

```
tools/skill-optimizer/
├── evaluate.py          # Fixed evaluator — NEVER modified by optimizer
├── program.md           # Meta-instructions for the optimizer agent
├── scenarios/
│   └── roborev-fix.json # Test scenarios for the roborev-fix skill
├── results.tsv          # Experiment log (gitignored)
├── pyproject.toml       # Dependencies (anthropic SDK)
└── README.md            # Usage instructions
```

## Evaluation Design

### Running Evaluation

```bash
uv run evaluate.py --skill path/to/SKILL.md --scenarios scenarios/roborev-fix.json
```

### Simulated Trace Generation

For each scenario, the evaluator sends to an LLM (Haiku for cost):
- **System prompt**: "You are simulating a Claude Code agent. Given this skill definition and user invocation, produce the exact sequence of tool calls you would make. Output as JSON array of {tool, args, reasoning}."
- **User prompt**: The scenario context (user message, repo state, mock tool outputs)

### Composite Score

Three sub-scores, each 0.0–1.0:

| Sub-score | Weight | How scored |
|---|---|---|
| Task completion | 50% | Checklist: required commands appear in correct order |
| Output quality | 30% | LLM-as-judge: rates fix approach, comment quality, severity prioritization (1-10 → normalized) |
| Efficiency | 20% | `1.0 - (actual_calls - min_calls) / max_penalty`, clamped [0, 1] |

Final score = weighted average across all scenarios. Higher is better.

### Output Format

```
---
score:      0.8350
completion: 0.9000
quality:    0.7500
efficiency: 0.8000
scenarios:  5
---
```

### Live Validation (Hybrid)

The optimization loop runs with simulated traces (cheap, fast). Periodically (every ~10 experiments or on score plateau), the human can run live-agent validation to confirm simulated scores track reality.

## Test Scenarios (roborev-fix)

Five scenarios defined in `scenarios/roborev-fix.json`:

1. **happy_path_single_review**: One open review with 3 findings. Agent discovers, fetches, fixes, tests, comments, closes.
2. **multiple_reviews**: Two open reviews. Agent handles both.
3. **no_open_reviews**: No reviews found. Agent reports nothing to fix.
4. **test_failure_after_fix**: Agent fixes code, tests fail, agent iterates.
5. **explicit_job_ids**: User passes `/roborev-fix 42 43`. Agent skips discovery.

Each scenario specifies: user_message, repo_state, mock_outputs, expected_commands, min_tool_calls.

## Experiment Loop (program.md)

### Setup Phase

1. Agree on run tag (e.g., `mar14`)
2. Create branch `autoresearch/<tag>`
3. Read: current skill file, evaluate.py, scenarios
4. Run baseline evaluation, record in results.tsv
5. Confirm and begin

### Optimization Loop (autonomous, never stops)

```
LOOP FOREVER:
  1. Read results.tsv — what's been tried, what worked
  2. Form hypothesis (e.g., "step 3 lacks error handling guidance")
  3. Make ONE targeted modification to the skill file
  4. git commit
  5. Run: uv run evaluate.py --skill <path> --scenarios scenarios/roborev-fix.json
  6. Extract composite score from output
  7. If score > best_score → keep commit, update best_score
  8. If score <= best_score → git reset --hard HEAD~1
  9. Record in results.tsv:
     commit | score | completion | quality | efficiency | status | description
  10. REPEAT — do NOT pause to ask permission
```

### Constraints

- Never modify evaluate.py or scenario files
- One change per experiment (isolate variables)
- Preserve skill frontmatter (name, description)
- Preserve overall structure (Usage, Instructions, Examples sections)
- Keep the skill readable and maintainable — complexity without improvement is a regression

### Results Tracking

```
results.tsv (tab-separated, gitignored):
commit	score	completion	quality	efficiency	status	description
a1b2c3d	0.8350	0.9000	0.7500	0.8000	keep	baseline
b2c3d4e	0.8500	0.9200	0.7500	0.8200	keep	add explicit error check step
c3d4e5f	0.8300	0.8800	0.7800	0.8000	discard	reorder steps 4 and 5
```

## Dependencies

```toml
[project]
name = "skill-optimizer"
requires-python = ">=3.11"
dependencies = [
    "anthropic>=0.40.0",
]
```

## Design Decisions

1. **Higher-is-better scoring** (unlike autoresearch's lower-is-better val_bpb) because it's more intuitive for quality metrics.
2. **Haiku for simulation** to keep per-evaluation cost at ~$0.01/scenario (~$0.05/run for 5 scenarios).
3. **Git-based keep/discard** matches autoresearch exactly — the commit history IS the optimization trajectory.
4. **Scenarios as JSON** rather than code, so the optimizer agent can't accidentally modify them.
5. **Single skill per run** — optimize one skill at a time for isolation.
