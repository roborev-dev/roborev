"""End-to-end test with mocked LLM calls."""
from pathlib import Path
from unittest.mock import patch

from evaluate import run_evaluation


def test_full_evaluation_mocked(tmp_path):
    """Run full evaluation pipeline with mocked API calls."""
    # Create a minimal skill file
    skill = tmp_path / "SKILL.md"
    skill.write_text(
        "---\nname: test-skill\ndescription: test\n---\n"
        "# test-skill\n\n## Instructions\n\n"
        "1. Run `roborev fix --open --list`\n"
        "2. Fix findings\n"
        "3. Run tests\n"
        "4. Close reviews\n"
    )

    # Create a minimal scenario file
    scenarios = tmp_path / "scenarios.json"
    scenarios.write_text(
        '[{"name": "basic", "user_message": "/test-skill", '
        '"repo_state": {"description": "test project", "language": "go"}, '
        '"mock_outputs": {"roborev fix --open --list": "Job 1: (1 finding)"}, '
        '"expected_commands": ["roborev fix --open --list", "go test ./..."], '
        '"min_tool_calls": 3}]'
    )

    mock_trace = [
        {"tool": "Bash", "command": "roborev fix --open --list", "reasoning": "discover"},
        {"tool": "Edit", "command": "fix code", "reasoning": "fix"},
        {"tool": "Bash", "command": "go test ./...", "reasoning": "test"},
    ]

    with patch("evaluate.simulate_trace", return_value=mock_trace), \
         patch("evaluate.score_quality", return_value=0.7):
        results = run_evaluation(skill, scenarios)

    assert results["scenarios"] == 1
    assert results["score"] > 0.0
    assert results["completion"] == 1.0  # both expected commands found
    assert results["quality"] == 0.7
    assert results["efficiency"] == 1.0  # 3 calls == min
    assert len(results["details"]) == 1
    assert results["details"][0]["scenario"] == "basic"
