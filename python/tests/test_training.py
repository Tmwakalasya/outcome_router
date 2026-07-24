import json
import unittest

from outcome_router.evaluation import evaluate
from outcome_router.training import detect_drift, train_snapshot


def decision(identifier, trace, model, bucket, outcome_feature=0.0, cost=1.0, incumbent_cost=2.0):
    return {
        "id": identifier,
        "trace_id": trace,
        "workflow": "support",
        "selected_model": model,
        "final_model": model,
        "bucket": bucket,
        "estimated_cost_usd": cost,
        "incumbent_estimated_cost_usd": incumbent_cost,
        "features": {
            "input_tokens": 100,
            "expected_output_tokens": 50,
            "message_count": 2,
            "tool_count": outcome_feature,
            "has_vision": False,
            "has_structured_output": True,
            "risk": 0.35,
        },
    }


class TrainingTests(unittest.TestCase):
    def test_snapshot_contains_customer_model_scores(self):
        decisions = []
        outcomes = []
        for index in range(120):
            model = "balanced" if index % 2 else "premium"
            decisions.append(decision(f"d{index}", f"t{index}", model, "treatment"))
            outcomes.append(
                {
                    "id": f"o{index}",
                    "decision_id": f"d{index}",
                    "type": "resolution",
                    "value": index % 5 != 0,
                }
            )
        snapshot = train_snapshot(
            decisions,
            outcomes,
            outcome_type="resolution",
            catalog_version="catalog-1",
            minimum_workflow_samples=20,
        )
        self.assertEqual(snapshot["training"]["labeled_examples"], 120)
        self.assertEqual(set(snapshot["global"]), {"premium", "balanced"})
        self.assertIn("support", snapshot["by_workflow"])
        for score in snapshot["global"].values():
            self.assertGreater(score["samples"], 0)
            self.assertEqual(set(score["weights"]), {
                "input_tokens_k",
                "expected_output_tokens_k",
                "message_count",
                "tool_count",
                "has_vision",
                "has_structured_output",
                "risk",
            })

    def test_evaluation_deduplicates_trace_outcome(self):
        decisions = [
            decision("d1", "treatment", "balanced", "treatment", cost=1, incumbent_cost=4),
            decision("d2", "treatment", "balanced", "treatment", cost=1, incumbent_cost=4),
            decision("d3", "control", "premium", "control", cost=4, incumbent_cost=4),
        ]
        outcomes = [
            {"trace_id": "treatment", "type": "resolution", "value": True},
            {"trace_id": "control", "type": "resolution", "value": False},
        ]
        report = evaluate(decisions, outcomes, outcome_type="resolution")
        self.assertEqual(report["treatment"]["n"], 1)
        self.assertEqual(report["control"]["n"], 1)
        self.assertEqual(report["verified_gross_savings_usd"], 6)

    def test_detects_quality_regression_and_catalog_change(self):
        previous = {
            "catalog_version": "a",
            "global": {"balanced": {"success_rate": 0.92}},
        }
        current = {
            "catalog_version": "b",
            "global": {"balanced": {"success_rate": 0.85}},
        }
        alerts = detect_drift(previous, current)
        self.assertEqual({alert["type"] for alert in alerts}, {"catalog_version_changed", "quality_regression"})


if __name__ == "__main__":
    unittest.main()
