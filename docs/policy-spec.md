# Policy Specification

Policies are immutable, versioned routing contracts. Publishing a new version
changes behavior; editing historical audit records does not.

## Required fields

- `id`, `tenant_id`, and `version`
- `mode`: `observe`, `shadow`, `canary`, or `active`
- `incumbent_model`
- `candidate_models`
- `quality_tolerance`
- `confidence_z`
- `minimum_samples`
- `primary_outcome`
- a trained `snapshot`

## Rollout fields

- `control_percent`: deterministic randomized incumbent holdout
- `canary_percent`: percent eligible for live optimization in canary mode
- `exploration_percent`: safe exploration; validation rejects values above 3
- `shadow_budget_usd_per_day`: maximum estimated daily duplicate inference

## Hard constraints

Policy constraints and per-request constraints are intersected. Request
requirements cannot weaken a policy.

- allowed regions
- required privacy level
- maximum estimated request cost
- maximum p95 latency
- model capabilities and context window
- runtime provider health

## Snapshot score

Each model score contains:

```json
{
  "success_rate": 0.91,
  "uncertainty": 0.005,
  "samples": 4100,
  "bias": 0.0,
  "weights": {
    "tool_count": -0.001,
    "risk": -0.003
  }
}
```

Workflow-specific scores override global scores when present. A candidate must
have at least `minimum_samples`; otherwise it can be observed or shadowed but
cannot receive production traffic.
