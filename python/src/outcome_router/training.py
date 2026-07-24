from __future__ import annotations

import argparse
import json
import math
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

from .io import latest_by_id, read_jsonl, write_json

FEATURE_NAMES = (
    "input_tokens_k",
    "expected_output_tokens_k",
    "message_count",
    "tool_count",
    "has_vision",
    "has_structured_output",
    "risk",
)


def _numeric_outcome(value: Any) -> float | None:
    if isinstance(value, bool):
        return 1.0 if value else 0.0
    if isinstance(value, (int, float)) and math.isfinite(float(value)):
        return min(1.0, max(0.0, float(value)))
    return None


def _vector(decision: dict[str, Any]) -> list[float]:
    features = decision.get("features", {})
    return [
        float(features.get("input_tokens", 0)) / 1000,
        float(features.get("expected_output_tokens", 0)) / 1000,
        float(features.get("message_count", 0)),
        float(features.get("tool_count", 0)),
        1.0 if features.get("has_vision") else 0.0,
        1.0 if features.get("has_structured_output") else 0.0,
        float(features.get("risk", 0)),
    ]


def join_examples(
    decisions: Iterable[dict[str, Any]],
    outcomes: Iterable[dict[str, Any]],
    outcome_type: str,
) -> list[tuple[dict[str, Any], float]]:
    latest = latest_by_id(decisions)
    by_trace: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for decision in latest.values():
        by_trace[str(decision.get("trace_id", ""))].append(decision)

    examples: dict[str, tuple[dict[str, Any], float]] = {}
    for outcome in outcomes:
        if outcome.get("type") != outcome_type:
            continue
        value = _numeric_outcome(outcome.get("value"))
        if value is None:
            continue
        decision_id = str(outcome.get("decision_id", ""))
        if decision_id and decision_id in latest:
            examples[decision_id] = (latest[decision_id], value)
            continue
        trace_id = str(outcome.get("trace_id", ""))
        for decision in by_trace.get(trace_id, []):
            if decision.get("bucket") != "shadow":
                examples[str(decision["id"])] = (decision, value)
    return list(examples.values())


def fit_logistic(
    examples: list[tuple[dict[str, Any], float]],
    *,
    epochs: int = 500,
    learning_rate: float = 0.03,
    l2: float = 0.01,
) -> dict[str, Any]:
    if not examples:
        raise ValueError("cannot train without labeled examples")
    positives = sum(label for _, label in examples)
    prior = (positives + 1) / (len(examples) + 2)
    bias = math.log(prior / (1 - prior))
    weights = [0.0] * len(FEATURE_NAMES)

    for _ in range(epochs):
        bias_gradient = 0.0
        gradients = [0.0] * len(weights)
        for decision, label in examples:
            vector = _vector(decision)
            prediction = sigmoid(bias + sum(w * x for w, x in zip(weights, vector)))
            error = prediction - label
            bias_gradient += error
            for index, value in enumerate(vector):
                gradients[index] += error * value
        count = float(len(examples))
        bias -= learning_rate * bias_gradient / count
        for index in range(len(weights)):
            gradient = gradients[index] / count + l2 * weights[index]
            weights[index] -= learning_rate * gradient

    predictions = [
        sigmoid(bias + sum(w * x for w, x in zip(weights, _vector(decision))))
        for decision, _ in examples
    ]
    labels = [label for _, label in examples]
    brier = sum((prediction - label) ** 2 for prediction, label in zip(predictions, labels)) / len(labels)
    success_rate = sum(labels) / len(labels)
    standard_error = math.sqrt(max(1e-9, success_rate * (1 - success_rate)) / len(labels))
    uncertainty = min(0.5, max(0.005, standard_error + math.sqrt(brier) / math.sqrt(len(labels))))
    return {
        "success_rate": success_rate,
        "uncertainty": uncertainty,
        "samples": len(examples),
        "weights": dict(zip(FEATURE_NAMES, weights)),
        "bias": bias,
        "brier_score": brier,
    }


def train_snapshot(
    decisions: list[dict[str, Any]],
    outcomes: list[dict[str, Any]],
    *,
    outcome_type: str,
    catalog_version: str,
    minimum_workflow_samples: int = 50,
) -> dict[str, Any]:
    examples = join_examples(decisions, outcomes, outcome_type)
    by_model: dict[str, list[tuple[dict[str, Any], float]]] = defaultdict(list)
    by_workflow: dict[str, dict[str, list[tuple[dict[str, Any], float]]]] = defaultdict(
        lambda: defaultdict(list)
    )
    for decision, label in examples:
        model = str(decision.get("final_model") or decision.get("selected_model") or "")
        workflow = str(decision.get("workflow") or "default")
        if not model:
            continue
        by_model[model].append((decision, label))
        by_workflow[workflow][model].append((decision, label))

    global_scores = {model: strip_diagnostics(fit_logistic(values)) for model, values in by_model.items()}
    workflow_scores: dict[str, dict[str, Any]] = {}
    for workflow, model_examples in by_workflow.items():
        scores = {
            model: strip_diagnostics(fit_logistic(values))
            for model, values in model_examples.items()
            if len(values) >= minimum_workflow_samples
        }
        if scores:
            workflow_scores[workflow] = scores

    now = datetime.now(timezone.utc)
    return {
        "version": "snapshot-" + now.strftime("%Y%m%dT%H%M%SZ"),
        "catalog_version": catalog_version,
        "trained_at": now.isoformat().replace("+00:00", "Z"),
        "global": global_scores,
        "by_workflow": workflow_scores,
        "training": {
            "outcome_type": outcome_type,
            "labeled_examples": len(examples),
            "models": {model: len(values) for model, values in by_model.items()},
        },
    }


def detect_drift(
    previous: dict[str, Any],
    current: dict[str, Any],
    *,
    success_rate_threshold: float = 0.03,
) -> list[dict[str, Any]]:
    alerts: list[dict[str, Any]] = []
    if previous.get("catalog_version") != current.get("catalog_version"):
        alerts.append(
            {
                "type": "catalog_version_changed",
                "previous": previous.get("catalog_version"),
                "current": current.get("catalog_version"),
            }
        )
    for model, current_score in current.get("global", {}).items():
        previous_score = previous.get("global", {}).get(model)
        if not previous_score:
            alerts.append({"type": "new_model", "model": model})
            continue
        delta = float(current_score["success_rate"]) - float(previous_score["success_rate"])
        if delta < -success_rate_threshold:
            alerts.append({"type": "quality_regression", "model": model, "delta": delta})
    return alerts


def sigmoid(value: float) -> float:
    if value >= 0:
        exponential = math.exp(-min(value, 700))
        return 1 / (1 + exponential)
    exponential = math.exp(max(value, -700))
    return exponential / (1 + exponential)


def strip_diagnostics(score: dict[str, Any]) -> dict[str, Any]:
    return {key: value for key, value in score.items() if key != "brier_score"}


def main() -> None:
    parser = argparse.ArgumentParser(description="Train an Outcome Router policy snapshot.")
    parser.add_argument("--decisions", required=True, help="Path to decisions.jsonl")
    parser.add_argument("--outcomes", required=True, help="Path to outcomes.jsonl")
    parser.add_argument("--outcome-type", default="resolution")
    parser.add_argument("--catalog-version", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--previous", help="Previous snapshot used for drift detection")
    args = parser.parse_args()

    snapshot = train_snapshot(
        read_jsonl(args.decisions),
        read_jsonl(args.outcomes),
        outcome_type=args.outcome_type,
        catalog_version=args.catalog_version,
    )
    if args.previous:
        with Path(args.previous).open("r", encoding="utf-8") as handle:
            snapshot["drift_alerts"] = detect_drift(json.load(handle), snapshot)
    write_json(args.output, snapshot)


if __name__ == "__main__":
    main()
