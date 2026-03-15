from __future__ import annotations


def _command_matches(trace_cmd: str, expected_cmd: str) -> bool:
    """Check if a trace command matches an expected command.

    Uses prefix matching: trace command must start with the expected command.
    This allows for additional arguments (e.g., comment text).
    """
    return trace_cmd.strip().startswith(expected_cmd.strip())


def score_completion(
    trace_commands: list[str], expected_commands: list[str]
) -> float:
    """Score task completion based on ordered subsequence matching.

    Returns fraction of expected commands that appear (in order) in the trace.
    """
    if not expected_commands:
        return 1.0
    if not trace_commands:
        return 0.0

    matched = 0
    trace_idx = 0
    for expected in expected_commands:
        search_idx = trace_idx
        while search_idx < len(trace_commands):
            if _command_matches(trace_commands[search_idx], expected):
                matched += 1
                trace_idx = search_idx + 1
                break
            search_idx += 1

    return matched / len(expected_commands)


def score_efficiency(
    actual_calls: int, min_calls: int, max_penalty: int = 10
) -> float:
    """Score efficiency based on tool call count.

    Returns 1.0 if actual <= min, scales down linearly, clamped at 0.0.
    """
    if actual_calls <= min_calls:
        return 1.0
    penalty = (actual_calls - min_calls) / max_penalty
    return max(0.0, 1.0 - penalty)


def composite_score(
    completion: float,
    quality: float,
    efficiency: float,
    weights: tuple[float, float, float] = (0.5, 0.3, 0.2),
) -> float:
    """Compute weighted composite score from sub-scores."""
    w_comp, w_qual, w_eff = weights
    return w_comp * completion + w_qual * quality + w_eff * efficiency
