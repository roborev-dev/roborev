from __future__ import annotations

import json

import anthropic

from models import Scenario

SIMULATION_SYSTEM_PROMPT = """\
You are simulating a Claude Code agent. You have access to Bash, Read, Write, \
Edit, Grep, and Glob tools. Given a skill definition and a user invocation, \
produce the exact sequence of tool calls and actions you would take.

Output a JSON array where each element is:
{"tool": "<tool_name>", "command": "<bash command or action>", "reasoning": "<why>"}

For Bash tool calls, set "command" to the shell command you would run.
For other actions (reading files, editing code, communicating with user), \
set "tool" to the action type and "command" to a description.

Be realistic: follow the skill instructions exactly as written. \
If the skill says to run a command, include it. If it says to check \
something first, include that check.

IMPORTANT: Output ONLY the JSON array, no other text."""


def build_simulation_prompt(
    skill_content: str, scenario: Scenario
) -> tuple[str, str]:
    """Build the system and user prompts for trace simulation."""
    mock_section = "\n".join(
        f"$ {cmd}\n{output}" for cmd, output in scenario.mock_outputs.items()
    )

    user_prompt = f"""\
## Skill Definition

{skill_content}

## User Invocation

The user says: `{scenario.user_message}`

## Repository Context

{scenario.repo_state.description}
Language: {scenario.repo_state.language}

## Available Command Outputs

When you run these commands, here is what they return:

{mock_section}

## Task

Produce the JSON array of tool calls you would make to handle this \
invocation, following the skill instructions exactly. Include every \
bash command, file read, code edit, and user communication."""

    return SIMULATION_SYSTEM_PROMPT, user_prompt


def simulate_trace(
    skill_content: str,
    scenario: Scenario,
    model: str = "claude-haiku-4-5-20251001",
) -> list[dict]:
    """Simulate an agent trace by calling the Anthropic API.

    Returns a list of tool call dicts: [{"tool": ..., "command": ..., "reasoning": ...}]
    """
    system, user = build_simulation_prompt(skill_content, scenario)
    client = anthropic.Anthropic()
    response = client.messages.create(
        model=model,
        max_tokens=4096,
        system=system,
        messages=[{"role": "user", "content": user}],
    )
    text = response.content[0].text.strip()
    # Strip markdown code fences if present
    if text.startswith("```"):
        lines = text.split("\n")
        text = "\n".join(lines[1:-1]).strip()
    return json.loads(text)


def extract_commands(trace: list[dict]) -> list[str]:
    """Extract bash commands from a simulated trace."""
    commands = []
    for step in trace:
        if step.get("tool", "").lower() == "bash" and step.get("command"):
            commands.append(step["command"])
    return commands
