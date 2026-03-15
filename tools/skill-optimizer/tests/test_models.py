import json

from models import Scenario, load_scenarios


def test_scenario_from_dict():
    data = {
        "name": "happy_path",
        "user_message": "/roborev-fix",
        "repo_state": {
            "description": "Go project with one open review",
            "language": "go",
        },
        "mock_outputs": {
            "roborev fix --open --list": "Job 42: commit abc123 (3 findings, open)",
        },
        "expected_commands": [
            "roborev fix --open --list",
            "roborev show --job 42 --json",
            "go test ./...",
            "roborev comment --job 42",
            "roborev close 42",
        ],
        "min_tool_calls": 6,
    }
    s = Scenario(**data)
    assert s.name == "happy_path"
    assert s.user_message == "/roborev-fix"
    assert len(s.expected_commands) == 5
    assert s.min_tool_calls == 6


def test_load_scenarios(tmp_path):
    scenario_data = [
        {
            "name": "test_scenario",
            "user_message": "/roborev-fix",
            "repo_state": {"description": "test", "language": "go"},
            "mock_outputs": {},
            "expected_commands": ["roborev fix --open --list"],
            "min_tool_calls": 2,
        }
    ]
    p = tmp_path / "scenarios.json"
    p.write_text(json.dumps(scenario_data))
    scenarios = load_scenarios(p)
    assert len(scenarios) == 1
    assert scenarios[0].name == "test_scenario"
    assert scenarios[0].repo_state.language == "go"
