from models import Scenario, RepoState
from simulate import build_simulation_prompt, extract_commands


def test_build_simulation_prompt():
    scenario = Scenario(
        name="test",
        user_message="/roborev-fix",
        repo_state=RepoState(
            description="Go project with one open review",
            language="go",
        ),
        mock_outputs={
            "roborev fix --open --list": "Job 42: commit abc123 (3 findings, open)",
        },
        expected_commands=["roborev fix --open --list"],
        min_tool_calls=2,
    )
    skill_content = "# roborev-fix\n\nFix all open review findings."
    system, user = build_simulation_prompt(skill_content, scenario)
    assert "simulat" in system.lower()
    assert "roborev-fix" in user
    assert "/roborev-fix" in user
    assert "Job 42" in user
    assert "go" in user.lower()


def test_extract_commands():
    trace = [
        {"tool": "Bash", "command": "roborev fix --open --list", "reasoning": "discover"},
        {"tool": "Read", "command": "read main.go", "reasoning": "check code"},
        {"tool": "Bash", "command": "go test ./...", "reasoning": "run tests"},
        {"tool": "Edit", "command": "fix main.go", "reasoning": "apply fix"},
        {"tool": "Bash", "command": "roborev close 42", "reasoning": "close review"},
    ]
    cmds = extract_commands(trace)
    assert cmds == [
        "roborev fix --open --list",
        "go test ./...",
        "roborev close 42",
    ]


def test_extract_commands_empty():
    assert extract_commands([]) == []
