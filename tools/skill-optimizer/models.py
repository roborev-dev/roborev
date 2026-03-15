from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path


@dataclass
class RepoState:
    description: str
    language: str


@dataclass
class Scenario:
    name: str
    user_message: str
    repo_state: RepoState
    mock_outputs: dict[str, str]
    expected_commands: list[str]
    min_tool_calls: int

    def __post_init__(self) -> None:
        if isinstance(self.repo_state, dict):
            self.repo_state = RepoState(**self.repo_state)


def load_scenarios(path: Path) -> list[Scenario]:
    """Load scenarios from a JSON file."""
    data = json.loads(path.read_text())
    return [Scenario(**s) for s in data]
