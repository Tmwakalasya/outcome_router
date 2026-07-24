from __future__ import annotations

import argparse
import json
import math
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from .io import latest_by_id, read_jsonl, write_json
from .training import _numeric_outcome


def wilson(successes: float, count: int, z: float = 1.96) -> dict[str, Any]:
    if count == 0:
        return {"estimate": 0, "lower": 0, "upper": 0, "n": 0}
    proportion = successes / count
    denominator = 1 + z * z / count
    center = (proportion + z * z / (2 * count)) / denominator
    margin = z * math.sqrt((proportion * (1 - proportion) + z * z / (4 * count)) / count) / denominator
    return {
        "estimate": proportion,
        "lower": max(0, center - margin),
        "upper": min(1, center + margin),
        "n": count,
    }


def evaluate(
    decisions: list[dict[str, Any]],
    outcomes: list[dict[str, Any]],
    *,
    outcome_type: str,
    tolerance: float = 0.01,
) -> dict[str, Any]:
    current = latest_by_id(decisions)
    trace_decisions: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for decision in current.values():
        if decision.get("bucket") == "shadow":
            continue
        trace_decisions[str(decision.get("trace_id", ""))].append(decision)

    trace_outcomes: dict[str, float] = {}
    for outcome in outcomes:
        if outcome.get("type") != outcome_type:
            continue
        value = _numeric_outcome(outcome.get("value"))
        if value is None:
            continue
        trace_id = str(outcome.get("trace_id", ""))
        if not trace_id and outcome.get("decision_id") in current:
            trace_id = str(current[str(outcome["decision_id"])].get("trace_id", ""))
        if trace_id:
            trace_outcomes[trace_id] = value

    groups = {
        "control": {"successes": 0.0, "n": 0, "cost": 0.0, "incumbent_cost": 0.0},
        "treatment": {"successes": 0.0, "n": 0, "cost": 0.0, "incumbent_cost": 0.0},
    }
    model_counts: dict[str, int] = defaultdict(int)
    for trace_id, trace in trace_decisions.items():
        group = (
            "treatment"
            if any(item.get("bucket") in {"treatment", "exploration"} for item in trace)
            else "control"
        )
        for decision in trace:
            cost = float(decision.get("actual_cost_usd") or decision.get("estimated_cost_usd") or 0)
            groups[group]["cost"] += cost
            groups[group]["incumbent_cost"] += float(decision.get("incumbent_estimated_cost_usd") or 0)
            model_counts[str(decision.get("final_model") or decision.get("selected_model"))] += 1
        if trace_id in trace_outcomes:
            groups[group]["successes"] += trace_outcomes[trace_id]
            groups[group]["n"] += 1

    control = wilson(groups["control"]["successes"], int(groups["control"]["n"]))
    treatment = wilson(groups["treatment"]["successes"], int(groups["treatment"]["n"]))
    total_cost = groups["control"]["cost"] + groups["treatment"]["cost"]
    incumbent_cost = groups["control"]["incumbent_cost"] + groups["treatment"]["incumbent_cost"]
    savings = max(0.0, incumbent_cost - total_cost)
    delta = treatment["estimate"] - control["estimate"]
    conservative_delta = treatment["lower"] - control["upper"]
    return {
        "generated_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "outcome_type": outcome_type,
        "quality_tolerance": tolerance,
        "control": control,
        "treatment": treatment,
        "observed_delta": delta,
        "conservative_delta": conservative_delta,
        "non_inferior_observed": delta >= -tolerance,
        "non_inferior_at_95_percent": conservative_delta >= -tolerance,
        "actual_cost_usd": total_cost,
        "incumbent_cost_usd": incumbent_cost,
        "verified_gross_savings_usd": savings,
        "savings_percent": (savings / incumbent_cost * 100) if incumbent_cost else 0,
        "model_distribution": dict(sorted(model_counts.items())),
        "warnings": warnings_for(groups, control, treatment),
    }


def warnings_for(
    groups: dict[str, dict[str, float]],
    control: dict[str, Any],
    treatment: dict[str, Any],
) -> list[str]:
    warnings: list[str] = []
    if control["n"] < 100 or treatment["n"] < 100:
        warnings.append("Fewer than 100 labeled traces in at least one arm; treat confidence intervals as directional.")
    if groups["control"]["incumbent_cost"] == 0:
        warnings.append("Incumbent counterfactual cost is unavailable; savings cannot be verified.")
    return warnings


def markdown_report(report: dict[str, Any]) -> str:
    verdict = "PASS" if report["non_inferior_at_95_percent"] else "NOT YET PROVEN"
    return "\n".join(
        [
            "# Outcome Router Audit",
            "",
            f"- Quality verdict: **{verdict}**",
            f"- Verified gross savings: **${report['verified_gross_savings_usd']:.2f}**",
            f"- Savings rate: **{report['savings_percent']:.1f}%**",
            f"- Treatment outcome: **{report['treatment']['estimate']:.1%}** "
            f"(95% CI {report['treatment']['lower']:.1%}–{report['treatment']['upper']:.1%})",
            f"- Control outcome: **{report['control']['estimate']:.1%}** "
            f"(95% CI {report['control']['lower']:.1%}–{report['control']['upper']:.1%})",
            f"- Conservative quality delta: **{report['conservative_delta']:.1%}**",
            "",
            "## Model distribution",
            "",
            *[f"- {model}: {count:,} decisions" for model, count in report["model_distribution"].items()],
            "",
            "## Warnings",
            "",
            *([f"- {warning}" for warning in report["warnings"]] or ["- None"]),
            "",
        ]
    )


def main() -> None:
    parser = argparse.ArgumentParser(description="Evaluate an Outcome Router randomized pilot.")
    parser.add_argument("--decisions", required=True)
    parser.add_argument("--outcomes", required=True)
    parser.add_argument("--outcome-type", default="resolution")
    parser.add_argument("--tolerance", type=float, default=0.01)
    parser.add_argument("--output", required=True)
    parser.add_argument("--markdown")
    args = parser.parse_args()
    report = evaluate(
        read_jsonl(args.decisions),
        read_jsonl(args.outcomes),
        outcome_type=args.outcome_type,
        tolerance=args.tolerance,
    )
    write_json(args.output, report)
    if args.markdown:
        Path(args.markdown).write_text(markdown_report(report), encoding="utf-8")


if __name__ == "__main__":
    main()
