"""Skill evaluator — the fixed harness for the optimization loop.

Usage:
    uv run evaluate.py --skill path/to/SKILL.md --scenarios scenarios/roborev-fix.json
    uv run evaluate.py --skill path/to/SKILL.md --scenarios scenarios/roborev-fix.json --model claude-haiku-4-5-20251001
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

from models import load_scenarios
from simulate import simulate_trace, extract_commands
from judge import score_quality
from scoring import score_completion, score_efficiency, composite_score


def load_skill(path: Path) -> str:
    """Load a skill file and return its content."""
    return path.read_text()


def evaluate_scenario(
    skill_content: str,
    scenario,
    model: str = "claude-haiku-4-5-20251001",
) -> dict:
    """Evaluate a single scenario. Returns dict with sub-scores."""
    # Simulate the agent trace
    trace = simulate_trace(skill_content, scenario, model=model)
    commands = extract_commands(trace)

    # Score task completion
    completion = score_completion(commands, scenario.expected_commands)

    # Score quality via LLM judge
    quality = score_quality(
        trace, scenario.repo_state.description, model=model
    )

    # Score efficiency
    efficiency = score_efficiency(
        actual_calls=len(trace),
        min_calls=scenario.min_tool_calls,
    )

    return {
        "scenario": scenario.name,
        "completion": completion,
        "quality": quality,
        "efficiency": efficiency,
        "composite": composite_score(completion, quality, efficiency),
        "trace_length": len(trace),
        "commands_found": len(commands),
    }


def run_evaluation(
    skill_path: Path,
    scenarios_path: Path,
    model: str = "claude-haiku-4-5-20251001",
) -> dict:
    """Run full evaluation across all scenarios. Returns aggregate results."""
    skill_content = load_skill(skill_path)
    scenarios = load_scenarios(scenarios_path)

    results = []
    for scenario in scenarios:
        try:
            result = evaluate_scenario(skill_content, scenario, model=model)
            results.append(result)
        except Exception as e:
            print(f"ERROR in scenario {scenario.name}: {e}", file=sys.stderr)
            results.append({
                "scenario": scenario.name,
                "completion": 0.0,
                "quality": 0.0,
                "efficiency": 0.0,
                "composite": 0.0,
                "error": str(e),
            })

    # Aggregate scores
    n = len(results)
    avg_fn = lambda key: sum(r[key] for r in results) / n if n else 0.0

    aggregate = {
        "score": avg_fn("composite"),
        "completion": avg_fn("completion"),
        "quality": avg_fn("quality"),
        "efficiency": avg_fn("efficiency"),
        "scenarios": n,
        "details": results,
    }
    return aggregate


def print_results(results: dict) -> None:
    """Print results in the autoresearch-style output format."""
    print("---")
    print(f"score:      {results['score']:.4f}")
    print(f"completion: {results['completion']:.4f}")
    print(f"quality:    {results['quality']:.4f}")
    print(f"efficiency: {results['efficiency']:.4f}")
    print(f"scenarios:  {results['scenarios']}")
    print("---")
    print()
    for detail in results["details"]:
        status = "ERROR" if "error" in detail else "OK"
        print(
            f"  {detail['scenario']}: "
            f"comp={detail['completion']:.2f} "
            f"qual={detail['quality']:.2f} "
            f"eff={detail['efficiency']:.2f} "
            f"-> {detail['composite']:.4f} [{status}]"
        )


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Evaluate a skill file against test scenarios"
    )
    parser.add_argument(
        "--skill", required=True, type=Path, help="Path to SKILL.md file"
    )
    parser.add_argument(
        "--scenarios", required=True, type=Path, help="Path to scenarios JSON"
    )
    parser.add_argument(
        "--model",
        default="claude-haiku-4-5-20251001",
        help="Anthropic model for simulation and judging",
    )
    args = parser.parse_args()

    if not args.skill.exists():
        print(f"Error: skill file not found: {args.skill}", file=sys.stderr)
        sys.exit(1)
    if not args.scenarios.exists():
        print(f"Error: scenarios file not found: {args.scenarios}", file=sys.stderr)
        sys.exit(1)

    results = run_evaluation(args.skill, args.scenarios, model=args.model)
    print_results(results)


if __name__ == "__main__":
    main()
