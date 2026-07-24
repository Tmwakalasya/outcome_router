# Outcome Event Specification

Outcome events are the product’s core data contract. They describe observable
business results, not an LLM judge’s general preference.

## Event

```json
{
  "decision_id": "dec_01...",
  "trace_id": "ticket-1042",
  "type": "resolution",
  "value": true,
  "occurred_at": "2026-07-24T14:00:00Z",
  "metadata": {
    "channel": "email",
    "queue": "returns"
  }
}
```

At least one of `decision_id` or `trace_id` is required.

- Use `decision_id` for immediate step outcomes such as valid tool arguments.
- Use `trace_id` for final workflow outcomes such as ticket resolution.
- `value` must be a Boolean or a number between zero and one for training.
- Events are append-only. Corrections should be a later event with a new
  timestamp; the latest event for the trace and type wins in evaluation.

## Recommended support and operations outcomes

| Type | Value | Level | Suggested priority |
| --- | --- | --- | --- |
| `resolution` | Boolean | Trace | Primary |
| `escalation` | Boolean | Trace | Guardrail; invert for success |
| `reopen` | Boolean | Trace | Guardrail; invert for success |
| `csat_normalized` | 0–1 | Trace | Secondary |
| `human_edit_accepted` | Boolean | Decision | Secondary |
| `tool_call_valid` | Boolean | Decision | Immediate guardrail |
| `policy_violation` | Boolean | Decision | Hard failure |

Primary outcomes should be difficult for the agent to manipulate and available
for both control and treatment traffic.

## Attribution rules

- Never assign a production trace outcome to a shadow response that did not
  affect the workflow.
- Shadow decisions need explicit evaluator or human labels before training.
- Final quality is reported once per workflow trace, even when the agent made
  many routed LLM calls.
- Cost is summed across every production step.
