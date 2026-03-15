from scoring import score_completion, score_efficiency, composite_score


# --- score_completion ---


def test_perfect_completion():
    expected = [
        "roborev fix --open --list",
        "roborev show --job 42 --json",
        "go test ./...",
        "roborev comment --job 42",
        "roborev close 42",
    ]
    trace_commands = [
        "roborev fix --open --list",
        "roborev show --job 42 --json",
        "git show abc1234",
        "go test ./...",
        "roborev comment --job 42 \"Fixed error handling\"",
        "roborev close 42",
    ]
    assert score_completion(trace_commands, expected) == 1.0


def test_missing_commands():
    expected = [
        "roborev fix --open --list",
        "roborev show --job 42 --json",
        "go test ./...",
        "roborev comment --job 42",
        "roborev close 42",
    ]
    trace_commands = [
        "roborev fix --open --list",
        "roborev show --job 42 --json",
        "roborev comment --job 42 \"Fixed it\"",
    ]
    # 2/5: fix and show match, but searching for "go test" scans past
    # the comment command, so comment and close don't match either
    assert score_completion(trace_commands, expected) == 2 / 5


def test_empty_trace():
    expected = ["roborev fix --open --list"]
    assert score_completion([], expected) == 0.0


def test_no_expected_commands():
    assert score_completion(["anything"], []) == 1.0


# --- score_efficiency ---


def test_perfect_efficiency():
    assert score_efficiency(actual_calls=6, min_calls=6) == 1.0


def test_some_waste():
    assert score_efficiency(actual_calls=10, min_calls=6, max_penalty=10) == 0.6


def test_excessive_calls_clamps():
    assert score_efficiency(actual_calls=100, min_calls=6, max_penalty=10) == 0.0


def test_fewer_than_min():
    assert score_efficiency(actual_calls=4, min_calls=6) == 1.0


# --- composite_score ---


def test_composite_score_default_weights():
    result = composite_score(completion=1.0, quality=1.0, efficiency=1.0)
    assert result == 1.0


def test_composite_score_mixed():
    result = composite_score(completion=0.8, quality=0.6, efficiency=1.0)
    expected = 0.5 * 0.8 + 0.3 * 0.6 + 0.2 * 1.0
    assert abs(result - expected) < 1e-9


def test_composite_score_custom_weights():
    result = composite_score(
        completion=1.0,
        quality=0.0,
        efficiency=0.0,
        weights=(1.0, 0.0, 0.0),
    )
    assert result == 1.0
