from __future__ import annotations

import json
import re

import anthropic

JUDGE_SYSTEM_PROMPT = """\
You are evaluating an AI agent's behavior when handling a code review fix task. \
Rate the quality of the agent's approach on a scale of 1-10.

Consider:
- Did the agent prioritize findings by severity (high before low)?
- Did the agent group fixes by file to minimize context switches?
- Were the comment messages informative and concise?
- Did the agent handle edge cases (passing reviews, errors) appropriately?
- Did the agent follow a logical, efficient workflow?

Output ONLY a JSON object: {"score": <1-10>, "reasoning": "<brief explanation>"}"""


def build_judge_prompt(trace: list[dict], scenario_description: str) -> str:
    """Build the user prompt for the quality judge."""
    trace_text = json.dumps(trace, indent=2)
    return f"""\
## Scenario

{scenario_description}

## Agent Trace

The agent performed these actions:

{trace_text}

## Task

Rate this agent's approach on a scale of 1 to 10 (1 = terrible, 10 = perfect). \
Consider severity prioritization, file grouping, comment quality, and edge case \
handling. Output ONLY: {{"score": <1-10>, "reasoning": "..."}}"""


def score_quality(
    trace: list[dict],
    scenario_description: str,
    model: str = "claude-haiku-4-5-20251001",
) -> float:
    """Score the quality of an agent trace using LLM-as-judge.

    Returns a float 0.0-1.0 (the 1-10 score normalized).
    """
    client = anthropic.Anthropic()
    prompt = build_judge_prompt(trace, scenario_description)
    response = client.messages.create(
        model=model,
        max_tokens=256,
        system=JUDGE_SYSTEM_PROMPT,
        messages=[{"role": "user", "content": prompt}],
    )
    text = response.content[0].text.strip()
    # Strip markdown code fences if present
    if text.startswith("```"):
        lines = text.split("\n")
        text = "\n".join(lines[1:-1]).strip()
    try:
        data = json.loads(text)
        raw_score = float(data["score"])
    except (json.JSONDecodeError, KeyError, ValueError):
        # Fallback: try to extract a number
        match = re.search(r"\b(\d+)\b", text)
        raw_score = float(match.group(1)) if match else 5.0
    # Clamp to 1-10, normalize to 0-1
    raw_score = max(1.0, min(10.0, raw_score))
    return (raw_score - 1.0) / 9.0
