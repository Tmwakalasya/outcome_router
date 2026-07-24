# Outcome Router

Outcome Router is a customer-specific, outcome-aware LLM router for production
support and operations agents. It minimizes model cost subject to explicit
quality, privacy, region, capability, latency, and risk constraints.

It is not a model marketplace. Customers keep their own provider keys and
contracts. The router records each decision, joins delayed business outcomes,
and maintains a randomized incumbent control so savings and quality can be
measured rather than guessed.

## What is implemented

- OpenAI-compatible `POST /v1/chat/completions` proxy
- Streaming SSE, tool, structured-output, and multimodal pass-through
- Versioned routing policies and model snapshots
- Hard feasibility filtering before cost optimization
- Confidence-bound quality floor with incumbent fallback
- Observe, shadow, canary, control, exploration, and active routing modes
- Bounded shadow budget and no high-risk or regulated exploration
- Provider fallback and circuit breaking
- `POST /v1/outcomes` for decision- or trace-level delayed outcomes
- Reproducible decision ledger and statistically aware audit summary
- Append-only local development store and production PostgreSQL store
- Dependency-free Python instrumentation SDK
- Offline policy training, drift detection, and randomized-pilot evaluation
- Production audit dashboard with live API connection and JSON export
- Docker Compose environment with PostgreSQL and a deterministic mock provider

## Quick start

The fastest complete demo uses Docker:

```bash
docker compose up --build
```

The router is available at `http://localhost:8080`. Send a routed request:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer demo-router-key' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "auto",
    "messages": [
      {"role": "user", "content": "Draft a reply for a delayed replacement order."}
    ],
    "routing": {
      "trace_id": "ticket-1042",
      "workflow": "ticket-resolution",
      "step": "draft-reply",
      "risk_class": "normal",
      "deadline_ms": 2000,
      "required_region": "us-east"
    }
  }'
```

The response includes:

```text
X-Outcome-Decision-ID: dec_...
X-Outcome-Model: balanced
X-Outcome-Policy-Version: 1.0.0
```

Record the eventual workflow result:

```bash
curl http://localhost:8080/v1/outcomes \
  -H 'Authorization: Bearer demo-router-key' \
  -H 'Content-Type: application/json' \
  -d '{
    "trace_id": "ticket-1042",
    "type": "resolution",
    "value": true
  }'
```

Read the audit:

```bash
curl 'http://localhost:8080/v1/audit/summary?days=30' \
  -H 'Authorization: Bearer demo-router-key'
```

For local processes instead of Docker, run these in separate terminals:

```bash
make mock
make run
cd dashboard && npm run dev
```

Then open `http://localhost:3000` and connect it to
`http://localhost:8080` with `demo-router-key`.

## Decision rule

The request path never asks another LLM which model to use. For every request:

1. Eliminate models that violate hard requirements.
2. Predict success, uncertainty, cost, and p95 latency for feasible models.
3. Compute the incumbent success estimate.
4. Select the cheapest candidate whose lower confidence bound is above:

   ```text
   incumbent predicted success - configured quality tolerance
   ```

5. Use the incumbent when data is insufficient, the policy drifted, the
   provider is unhealthy, or no candidate clears the floor.

Cost cannot compensate for privacy, capability, region, risk, or latency
violations.

## Public interfaces

The complete wire contract is in [api/openapi.yaml](api/openapi.yaml).

| Endpoint | Purpose |
| --- | --- |
| `POST /v1/chat/completions` | Route and proxy an OpenAI-compatible request |
| `POST /v1/outcomes` | Attach delayed business or step outcomes |
| `GET /v1/decisions/{id}` | Reproduce a decision |
| `GET /v1/audit/summary` | Cost, quality, confidence, and allocation audit |
| `GET /v1/policies` | List tenant policies |
| `POST /v1/policies` | Create or replace a versioned policy using the admin key |

`routing` is an Outcome Router extension removed before the upstream request:

```json
{
  "trace_id": "workflow-instance-id",
  "workflow": "ticket-resolution",
  "step": "tool-selection",
  "risk_class": "low",
  "policy_id": "support-production",
  "deadline_ms": 1500,
  "required_region": "us-east",
  "required_privacy": "zdr",
  "max_cost_usd": 0.05
}
```

Unknown OpenAI-compatible request fields are preserved.

## Training and evaluation

Install the local Python package:

```bash
python3 -m venv python/.venv
python/.venv/bin/pip install -e python
```

Train a customer snapshot from labeled decisions:

```bash
outcome-router-train \
  --decisions data/decisions.jsonl \
  --outcomes data/outcomes.jsonl \
  --outcome-type resolution \
  --catalog-version catalog-2026-07-24 \
  --output audit-output/snapshot.json
```

Evaluate a controlled pilot:

```bash
outcome-router-evaluate \
  --decisions data/decisions.jsonl \
  --outcomes data/outcomes.jsonl \
  --outcome-type resolution \
  --tolerance 0.01 \
  --output audit-output/report.json \
  --markdown audit-output/report.md
```

The evaluator deduplicates final outcomes by trace, reports Wilson confidence
intervals, and only calls the result proven when the conservative
non-inferiority bound clears the configured tolerance.

## Production configuration

1. Copy `config/production.example.json`.
2. Replace model capabilities, pinned versions, prices, and quality priors.
3. Put provider and router credentials in environment variables.
4. Apply `db/migrations/001_initial.sql`.
5. Set `DATABASE_URL`; without it the router uses local append-only JSONL.
6. Add trained snapshots through the admin policy API.

Provider secrets are resolved from environment variable names in configuration.
They are never returned through the API or written to decision logs.

## Safety properties

- Exploration is capped at 3% by policy validation.
- Exploration runs only on explicitly low-risk requests.
- Shadow execution is disabled for high-risk and regulated traffic.
- Fallback models must pass the same hard feasibility constraints.
- A drifted policy always selects the incumbent.
- The gateway removes routing metadata before upstream transmission.
- Incoming tenant and administrator tokens use constant-time comparison.
- Prompt bodies are not persisted by the router.
- Every completed decision stores policy, snapshot, and catalog versions.

## Repository layout

```text
cmd/router/             Go proxy service
cmd/mock-provider/      Local deterministic OpenAI-compatible provider
internal/routing/       Features, feasibility, prediction, and policy engine
internal/api/           HTTP APIs, streaming, fallback, and shadow execution
internal/store/         Local and PostgreSQL audit repositories
python/                 SDK, trainer, drift detection, and evaluator
dashboard/              Production audit dashboard
db/migrations/          PostgreSQL schema
docs/                   Architecture, policy, outcome, and commercial playbooks
```

## Verification

```bash
make test
```

The Go suite covers routing constraints, quality floors, drift fallback,
request preservation, response streaming, outcome ingestion, audit accounting,
and persistence reload. The routing benchmark runs at roughly microsecond
latency on a local Apple silicon machine; upstream and network time are
reported separately.

## Deliberate MVP boundaries

- Native Anthropic Messages request/response translation is not included; use
  an OpenAI-compatible provider endpoint or add an adapter.
- Streaming requests use conservative estimated token cost unless the upstream
  includes usage in the stream.
- PostgreSQL is the implemented production audit store. Object-store Parquet
  export and distributed Redis policy caches are the next scale step, not
  required for the first design partners.
- The included learner is intentionally interpretable. A larger router model
  should only replace it after controlled production evidence shows a material
  gain.
