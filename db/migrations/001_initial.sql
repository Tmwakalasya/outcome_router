CREATE TABLE IF NOT EXISTS routing_policies (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    version         TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    data            JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS routing_policies_tenant_idx
    ON routing_policies (tenant_id);

CREATE TABLE IF NOT EXISTS routing_decisions (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL,
    trace_id            TEXT NOT NULL,
    policy_id           TEXT NOT NULL,
    policy_version      TEXT NOT NULL,
    selected_model      TEXT NOT NULL,
    final_model         TEXT NOT NULL DEFAULT '',
    bucket              TEXT NOT NULL,
    estimated_cost_usd  DOUBLE PRECISION NOT NULL DEFAULT 0,
    actual_cost_usd     DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL,
    data                JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS routing_decisions_tenant_created_idx
    ON routing_decisions (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS routing_decisions_trace_idx
    ON routing_decisions (tenant_id, trace_id);

CREATE INDEX IF NOT EXISTS routing_decisions_policy_bucket_idx
    ON routing_decisions (tenant_id, policy_id, bucket, created_at DESC);

CREATE TABLE IF NOT EXISTS routing_outcomes (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    decision_id     TEXT,
    trace_id        TEXT,
    outcome_type    TEXT NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    data            JSONB NOT NULL,
    CONSTRAINT routing_outcome_target CHECK (decision_id IS NOT NULL OR trace_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS routing_outcomes_tenant_created_idx
    ON routing_outcomes (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS routing_outcomes_decision_idx
    ON routing_outcomes (tenant_id, decision_id)
    WHERE decision_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS routing_outcomes_trace_idx
    ON routing_outcomes (tenant_id, trace_id)
    WHERE trace_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS routing_outcomes_type_idx
    ON routing_outcomes (tenant_id, outcome_type, created_at DESC);
