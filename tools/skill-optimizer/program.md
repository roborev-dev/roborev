# Skill Optimizer — Agent Instructions

You are an autonomous skill optimizer. Your job is to iteratively improve
a SKILL.md file by making targeted modifications and evaluating the results.

## Setup

1. Agree on a **run tag** with the human (e.g., `mar14`).
2. Create branch: `git checkout -b autoresearch/<tag>`
3. Read these files:
   - The skill file to optimize (path will be provided)
   - `tools/skill-optimizer/evaluate.py` (the fixed evaluator — do NOT modify)
   - The scenario file (e.g., `tools/skill-optimizer/scenarios/roborev-fix.json` — do NOT modify)
4. Run the baseline evaluation:
   ```bash
   cd tools/skill-optimizer && uv run evaluate.py --skill <skill_path> --scenarios scenarios/roborev-fix.json
   ```
5. Initialize `tools/skill-optimizer/results.tsv` with header if it doesn't exist:
   ```
   commit	score	completion	quality	efficiency	status	description
   ```
6. Record baseline score in results.tsv.
7. Confirm with human, then begin the loop.

## Experiment Loop

NEVER STOP. Do NOT pause to ask the human if you should continue.
Run experiments continuously until interrupted.

```
LOOP FOREVER:
  1. Read results.tsv to see what's been tried and what scores achieved
  2. Form a hypothesis about what change might improve the score
     Examples:
     - "Add explicit error handling guidance to step 1"
     - "Reword the command example to be more specific"
     - "Add a constraint about checking verdict before fixing"
     - "Simplify step 3 — it's overlong and may confuse the agent"
     - "Add an example for the edge case of no open reviews"
  3. Make ONE targeted modification to the skill file
  4. git add <skill_file> && git commit -m "experiment: <description>"
  5. Run evaluation:
     cd tools/skill-optimizer && uv run evaluate.py --skill <skill_path> --scenarios scenarios/roborev-fix.json
  6. Extract the score from the output (the "score:" line)
  7. If score > best_score:
       - KEEP the commit (it's already committed)
       - Update best_score
       - Log: commit | score | completion | quality | efficiency | keep | description
  8. If score <= best_score:
       - DISCARD: git reset --hard HEAD~1
       - Log: commit | score | completion | quality | efficiency | discard | description
  9. Append to results.tsv (do NOT commit results.tsv — it is gitignored)
  10. REPEAT
```

## Constraints

- **NEVER modify** `evaluate.py` or any file in `scenarios/`
- **ONE change per experiment** — isolate variables so you know what worked
- **Preserve frontmatter** — the `---` name/description block must not change
- **Preserve structure** — keep the Usage, Instructions, Examples, See also sections
- **Readability matters** — a skill that scores 0.01 higher but is unreadable is worse
- **Removing words can help** — shorter, clearer instructions often outperform verbose ones

## What You Can Change

Everything in the skill file's content:
- Wording and phrasing of instructions
- Order of steps within a section
- Level of detail in each step
- Examples (add, remove, modify)
- Constraints and guardrails ("do NOT...", "IMPORTANT:...")
- Error handling instructions
- Command syntax examples

## Crash Handling

If the evaluation crashes:
- Check stderr: `tail -n 20 run.log`
- If it's a transient API error, retry once
- If the skill file has syntax issues (broken markdown), fix and retry
- Log as `crash` status in results.tsv and move on

## Simplicity Criterion

Prefer simpler changes. If removing words maintains or improves the score,
that's a win. The best skill is the shortest one that scores highest.
