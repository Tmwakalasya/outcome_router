from outcome_router import Client, RoutingMetadata

router = Client("http://localhost:8080", "demo-router-key")

metadata = RoutingMetadata.create(
    "ticket-resolution",
    step="draft-reply",
    risk_class="normal",
    deadline_ms=2_000,
    required_region="us-east",
)

response = router.chat(
    [{"role": "user", "content": "A customer says their replacement order never arrived."}],
    routing=metadata,
    temperature=0.2,
)

print(response.data["choices"][0]["message"]["content"])
print("decision:", response.decision_id, "model:", response.model)

# Attach the final workflow outcome when it is known. A trace-level outcome can
# be delayed and will be joined back to every production step in the workflow.
router.outcome("resolution", True, trace_id=metadata.trace_id)
