from unittest.mock import patch

from evaluate import load_skill, evaluate_scenario
from models import Scenario, RepoState


def test_load_skill(tmp_path):
    skill_file = tmp_path / "SKILL.md"
    skill_file.write_text("---\nname: test\n---\n# Test skill\nDo things.")
    content = load_skill(skill_file)
    assert "# Test skill" in content
    assert "Do things." in content


def test_evaluate_scenario_mocked():
    """Integration test with mocked LLM calls."""
    scenario = Scenario(
        name="test",
        user_message="/roborev-fix",
        repo_state=RepoState("Go project with one open review", "go"),
        mock_outputs={
            "roborev fix --open --list": "Job 42: commit abc123 (1 finding, open)",
        },
        expected_commands=[
            "roborev fix --open --list",
            "roborev show --job 42 --json",
            "go test ./...",
            "roborev close 42",
        ],
        min_tool_calls=5,
    )
    skill_content = "# roborev-fix\n\nFix review findings."

    mock_trace = [
        {"tool": "Bash", "command": "roborev fix --open --list", "reasoning": "discover"},
        {"tool": "Bash", "command": "roborev show --job 42 --json", "reasoning": "fetch"},
        {"tool": "Edit", "command": "fix main.go", "reasoning": "fix"},
        {"tool": "Bash", "command": "go test ./...", "reasoning": "test"},
        {"tool": "Bash", "command": "roborev close 42", "reasoning": "close"},
    ]

    with patch("evaluate.simulate_trace", return_value=mock_trace), \
         patch("evaluate.score_quality", return_value=0.8):
        result = evaluate_scenario(skill_content, scenario)

    assert result["completion"] == 1.0  # all 4 commands matched
    assert result["quality"] == 0.8
    assert result["efficiency"] == 1.0  # 5 calls == min_tool_calls
    assert 0.0 <= result["composite"] <= 1.0
