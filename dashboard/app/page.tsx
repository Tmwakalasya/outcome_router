"use client";

import { FormEvent, useMemo, useState } from "react";

type Interval = {
  estimate: number;
  lower: number;
  upper: number;
  n: number;
};

type ModelSummary = {
  requests: number;
  cost_usd: number;
  success_rate?: number;
};

type Summary = {
  requests: number;
  routed_requests: number;
  control_requests: number;
  shadow_requests: number;
  actual_cost_usd: number;
  incumbent_cost_usd: number;
  verified_gross_savings_usd: number;
  savings_percent: number;
  treatment_outcome: Interval;
  control_outcome: Interval;
  non_inferiority_delta: number;
  by_model: Record<string, ModelSummary>;
  by_bucket: Record<string, number>;
  generated_at: string;
};

const demoSummary: Summary = {
  requests: 128_442,
  routed_requests: 93_110,
  control_requests: 12_851,
  shadow_requests: 8_420,
  actual_cost_usd: 8_941,
  incumbent_cost_usd: 13_897,
  verified_gross_savings_usd: 4_956,
  savings_percent: 35.66,
  treatment_outcome: { estimate: 0.921, lower: 0.916, upper: 0.926, n: 9_432 },
  control_outcome: { estimate: 0.924, lower: 0.917, upper: 0.931, n: 2_011 },
  non_inferiority_delta: -0.003,
  by_model: {
    balanced: { requests: 81_008, cost_usd: 3_118, success_rate: 0.919 },
    premium: { requests: 35_332, cost_usd: 5_607, success_rate: 0.926 },
    economy: { requests: 12_102, cost_usd: 216, success_rate: 0.901 },
  },
  by_bucket: { treatment: 92_110, control: 12_851, incumbent: 15_061, shadow: 8_420 },
  generated_at: "2026-07-24T13:42:00Z",
};

const recentDecisions = [
  { trace: "tr_8c4f…19a", workflow: "Ticket resolution", step: "Draft reply", model: "balanced", reason: "Quality floor met", saved: "$0.061", latency: "6ms", status: "routed" },
  { trace: "tr_12ba…e7c", workflow: "Refund operations", step: "Policy check", model: "premium", reason: "High-risk workflow", saved: "—", latency: "4ms", status: "guarded" },
  { trace: "tr_92de…41f", workflow: "Account recovery", step: "Tool selection", model: "premium", reason: "Randomized control", saved: "—", latency: "5ms", status: "control" },
  { trace: "tr_1ff3…89d", workflow: "Ticket resolution", step: "Classify intent", model: "economy", reason: "Quality floor met", saved: "$0.018", latency: "3ms", status: "routed" },
  { trace: "tr_a273…cd0", workflow: "Order exception", step: "Draft reply", model: "balanced", reason: "Quality floor met", saved: "$0.044", latency: "7ms", status: "routed" },
];

function money(value: number) {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    maximumFractionDigits: 0,
  }).format(value);
}

function compact(value: number) {
  return new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function percent(value: number, digits = 1) {
  return `${(value * 100).toFixed(digits)}%`;
}

export default function Home() {
  const [summary, setSummary] = useState<Summary>(demoSummary);
  const [source, setSource] = useState<"demo" | "live">("demo");
  const [connectionOpen, setConnectionOpen] = useState(false);
  const [apiUrl, setApiUrl] = useState("http://localhost:8080");
  const [apiKey, setApiKey] = useState("");
  const [connectionState, setConnectionState] = useState<"idle" | "loading" | "error">("idle");
  const [connectionError, setConnectionError] = useState("");

  const modelRows = useMemo(() => {
    const total = Object.values(summary.by_model).reduce((sum, model) => sum + model.requests, 0) || 1;
    return Object.entries(summary.by_model)
      .map(([name, model]) => ({ name, ...model, share: model.requests / total }))
      .sort((a, b) => b.requests - a.requests);
  }, [summary]);

  async function connect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setConnectionState("loading");
    setConnectionError("");
    try {
      const response = await fetch(`${apiUrl.replace(/\/$/, "")}/v1/audit/summary?days=30`, {
        headers: { Authorization: `Bearer ${apiKey}` },
      });
      if (!response.ok) {
        throw new Error(response.status === 401 ? "The router key was not accepted." : `The router returned HTTP ${response.status}.`);
      }
      const liveSummary = (await response.json()) as Summary;
      setSummary(liveSummary);
      setSource("live");
      setConnectionOpen(false);
      setConnectionState("idle");
    } catch (error) {
      setConnectionState("error");
      setConnectionError(error instanceof Error ? error.message : "Could not reach the router.");
    }
  }

  function useDemo() {
    setSummary(demoSummary);
    setSource("demo");
    setConnectionOpen(false);
    setConnectionState("idle");
    setConnectionError("");
  }

  function exportAudit() {
    const blob = new Blob([JSON.stringify(summary, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `outcome-router-audit-${new Date().toISOString().slice(0, 10)}.json`;
    anchor.click();
    URL.revokeObjectURL(url);
  }

  const qualityDelta = summary.non_inferiority_delta;
  const qualityHolding = qualityDelta >= -0.01;
  const routingRate = summary.requests ? summary.routed_requests / summary.requests : 0;

  return (
    <main className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true"><i /><i /><i /></span>
          <span>Outcome<span>Router</span></span>
        </div>
        <nav aria-label="Primary navigation">
          <p>Workspace</p>
          <a className="nav-item active" href="#overview"><span aria-hidden="true">⌁</span>Overview</a>
          <a className="nav-item" href="#decisions"><span aria-hidden="true">↳</span>Decisions</a>
          <a className="nav-item" href="#models"><span aria-hidden="true">◫</span>Models</a>
          <a className="nav-item" href="#experiment"><span aria-hidden="true">⎇</span>Experiment</a>
          <p>Control</p>
          <a className="nav-item" href="#policy"><span aria-hidden="true">◇</span>Policies</a>
          <a className="nav-item" href="#audit"><span aria-hidden="true">✓</span>Audit trail</a>
        </nav>
        <div className="sidebar-foot">
          <div className="tenant-avatar">AC</div>
          <div><strong>Acme Support</strong><span>Production</span></div>
          <button aria-label="Workspace menu">•••</button>
        </div>
      </aside>

      <section className="main-column">
        <header className="topbar">
          <div>
            <span className="crumb-muted">Production</span>
            <span className="crumb-divider">/</span>
            <span>Support agent</span>
          </div>
          <div className="top-actions">
            <span className={`source-pill ${source}`}><i />{source === "live" ? "Live router" : "Demo data"}</span>
            <button className="button secondary" onClick={() => setConnectionOpen(true)}>Connect router</button>
            <button className="button primary" onClick={exportAudit}>Export audit</button>
          </div>
        </header>

        <div className="content" id="overview">
          <div className="page-heading">
            <div>
              <p className="eyebrow">Last 30 days · policy support-production</p>
              <h1>Quality is holding.<br /><span>Cost is not.</span></h1>
            </div>
            <div className={`verdict ${qualityHolding ? "passing" : "failing"}`}>
              <span className="verdict-icon">{qualityHolding ? "✓" : "!"}</span>
              <div>
                <strong>{qualityHolding ? "Non-inferiority passing" : "Quality guardrail tripped"}</strong>
                <span>{percent(qualityDelta)} outcome delta · 95% confidence tracked</span>
              </div>
            </div>
          </div>

          <section className="metric-grid" aria-label="Key routing metrics">
            <article className="metric featured">
              <p>Verified gross savings</p>
              <strong>{money(summary.verified_gross_savings_usd)}</strong>
              <span className="metric-change positive">↓ {summary.savings_percent.toFixed(1)}% vs. incumbent</span>
              <div className="metric-spark savings-spark" aria-hidden="true">
                {[22, 30, 28, 44, 41, 55, 64, 60, 77, 82, 91, 96].map((height, index) => <i key={index} style={{ height: `${height}%` }} />)}
              </div>
            </article>
            <article className="metric">
              <p>Resolution quality</p>
              <strong>{percent(summary.treatment_outcome.estimate)}</strong>
              <span className={qualityHolding ? "metric-change neutral" : "metric-change negative"}>
                {qualityDelta >= 0 ? "+" : ""}{percent(qualityDelta)} vs. control
              </span>
              <div className="confidence-line"><i style={{ left: "37%", width: "48%" }} /><b style={{ left: "59%" }} /></div>
              <small>95% CI {percent(summary.treatment_outcome.lower)}–{percent(summary.treatment_outcome.upper)}</small>
            </article>
            <article className="metric">
              <p>Routed decisions</p>
              <strong>{compact(summary.routed_requests)}</strong>
              <span className="metric-change positive">{percent(routingRate)} of eligible calls</span>
              <div className="mini-stack" aria-hidden="true"><i style={{ width: `${routingRate * 100}%` }} /><b /></div>
              <small>{compact(summary.control_requests)} held out for control</small>
            </article>
            <article className="metric">
              <p>Policy overhead</p>
              <strong>6.2<span>ms</span></strong>
              <span className="metric-change positive">p95 · below 15ms target</span>
              <div className="latency-scale"><i /><b /></div>
              <small>1.1µs local decision · network excluded</small>
            </article>
          </section>

          <section className="dashboard-grid">
            <article className="panel frontier-panel" id="experiment">
              <div className="panel-heading">
                <div>
                  <p className="eyebrow">Cost-quality frontier</p>
                  <h2>Every cheaper route must clear the floor</h2>
                </div>
                <span className="guardrail-badge"><i /> Quality floor {percent(summary.control_outcome.estimate - 0.01)}</span>
              </div>
              <div className="frontier-chart" aria-label="Cost and outcome quality plot">
                <span className="axis-label y">Resolution quality</span>
                <span className="axis-label x">Cost per 1K decisions →</span>
                <div className="chart-grid">
                  <span className="floor-line" style={{ bottom: "48%" }}><i>Quality floor</i></span>
                  <span className="frontier-path" />
                  <button className="plot-point premium-point" aria-label="Premium model: 92.6 percent quality, highest cost">
                    <i /><span><strong>Premium</strong>$108 / 1K</span>
                  </button>
                  <button className="plot-point balanced-point" aria-label="Balanced model: 91.9 percent quality, medium cost">
                    <i /><span><strong>Balanced</strong>$38 / 1K</span>
                  </button>
                  <button className="plot-point economy-point" aria-label="Economy model: 90.1 percent quality, lowest cost">
                    <i /><span><strong>Economy</strong>$12 / 1K</span>
                  </button>
                </div>
              </div>
              <div className="chart-note">
                <span className="note-mark">↗</span>
                <p><strong>The router selected a cheaper model on 72.5% of calls</strong> while retaining the premium model for risk, uncertainty, and complex tool use.</p>
              </div>
            </article>

            <article className="panel model-panel" id="models">
              <div className="panel-heading">
                <div>
                  <p className="eyebrow">Model allocation</p>
                  <h2>Traffic by selected model</h2>
                </div>
                <button className="plain-button">30 days⌄</button>
              </div>
              <div className="allocation-bar" aria-hidden="true">
                {modelRows.map((model) => <i key={model.name} className={`model-${model.name}`} style={{ width: `${model.share * 100}%` }} />)}
              </div>
              <div className="model-list">
                {modelRows.map((model) => (
                  <div className="model-row" key={model.name}>
                    <span className={`model-dot model-${model.name}`} />
                    <div><strong>{model.name[0].toUpperCase() + model.name.slice(1)}</strong><small>{compact(model.requests)} decisions</small></div>
                    <div className="model-stat"><strong>{percent(model.share)}</strong><small>{money(model.cost_usd)}</small></div>
                  </div>
                ))}
              </div>
              <div className="policy-card" id="policy">
                <div className="policy-top"><span>ACTIVE POLICY</span><b>v1.0.0</b></div>
                <strong>Minimize cost subject to quality</strong>
                <p>Quality tolerance 1.0pp · Control 10% · Exploration 1%</p>
              </div>
            </article>
          </section>

          <section className="panel decisions-panel" id="decisions">
            <div className="panel-heading">
              <div>
                <p className="eyebrow">Decision ledger</p>
                <h2>Recent production routes</h2>
              </div>
              <div className="ledger-actions">
                <span><i /> All decisions reproducible</span>
                <button className="plain-button">View full audit →</button>
              </div>
            </div>
            <div className="decision-table" role="table" aria-label="Recent routing decisions">
              <div className="table-row table-head" role="row">
                <span>Trace</span><span>Workflow / step</span><span>Selected model</span><span>Decision basis</span><span>Savings</span><span>Overhead</span>
              </div>
              {recentDecisions.map((decision) => (
                <div className="table-row" role="row" key={decision.trace}>
                  <span className="mono">{decision.trace}</span>
                  <span><strong>{decision.workflow}</strong><small>{decision.step}</small></span>
                  <span><i className={`status-dot ${decision.status}`} />{decision.model}</span>
                  <span>{decision.reason}</span>
                  <span className={decision.saved === "—" ? "muted" : "saved"}>{decision.saved}</span>
                  <span className="mono">{decision.latency}</span>
                </div>
              ))}
            </div>
          </section>

          <footer id="audit">
            <span>Outcome Router · Evidence over intuition</span>
            <span>
              Updated{" "}
              {new Date(summary.generated_at).toLocaleString("en-US", {
                month: "short",
                day: "numeric",
                hour: "numeric",
                minute: "2-digit",
                timeZone: "UTC",
                timeZoneName: "short",
              })}
            </span>
          </footer>
        </div>
      </section>

      {connectionOpen && (
        <div className="modal-backdrop" role="presentation" onMouseDown={(event) => {
          if (event.target === event.currentTarget) setConnectionOpen(false);
        }}>
          <section className="connection-modal" role="dialog" aria-modal="true" aria-labelledby="connect-title">
            <button className="modal-close" aria-label="Close connection dialog" onClick={() => setConnectionOpen(false)}>×</button>
            <p className="eyebrow">Live audit API</p>
            <h2 id="connect-title">Connect your router</h2>
            <p>Credentials are used for this request only and are never saved in the browser.</p>
            <form onSubmit={connect}>
              <label>Router URL<input type="url" value={apiUrl} onChange={(event) => setApiUrl(event.target.value)} required /></label>
              <label>Router API key<input type="password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder="router_••••••••" required /></label>
              {connectionState === "error" && <div className="form-error">{connectionError}</div>}
              <button className="button primary full" disabled={connectionState === "loading"}>
                {connectionState === "loading" ? "Connecting…" : "Load live audit"}
              </button>
              <button className="button ghost full" type="button" onClick={useDemo}>Continue with demo data</button>
            </form>
          </section>
        </div>
      )}
    </main>
  );
}
