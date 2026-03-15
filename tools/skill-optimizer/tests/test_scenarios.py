from pathlib import Path

from models import load_scenarios

SCENARIOS_DIR = Path(__file__).parent.parent / "scenarios"


def test_roborev_fix_scenarios_load():
    scenarios = load_scenarios(SCENARIOS_DIR / "roborev-fix.json")
    assert len(scenarios) == 5
    names = {s.name for s in scenarios}
    assert names == {
        "happy_path_single_review",
        "multiple_reviews",
        "no_open_reviews",
        "test_failure_after_fix",
        "explicit_job_ids",
    }


def test_all_scenarios_have_required_fields():
    scenarios = load_scenarios(SCENARIOS_DIR / "roborev-fix.json")
    for s in scenarios:
        assert s.name, f"Scenario missing name"
        assert s.user_message, f"{s.name}: missing user_message"
        assert s.repo_state.description, f"{s.name}: missing repo_state.description"
        assert s.expected_commands, f"{s.name}: missing expected_commands"
        assert s.min_tool_calls > 0, f"{s.name}: min_tool_calls must be > 0"
