from judge import build_judge_prompt


def test_build_judge_prompt():
    trace = [
        {"tool": "Bash", "command": "roborev fix --open --list", "reasoning": "discover"},
        {"tool": "Bash", "command": "go test ./...", "reasoning": "verify"},
    ]
    scenario_desc = "Go project with one open review containing 3 findings"
    prompt = build_judge_prompt(trace, scenario_desc)
    assert "1" in prompt and "10" in prompt
    assert "roborev fix --open --list" in prompt
    assert "3 findings" in prompt
