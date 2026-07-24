package routing

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/outcome-router/outcome-router/internal/domain"
)

func TestRouteSelectsCheapestModelAboveQualityFloor(t *testing.T) {
	engine, policy := testEngineAndPolicy()
	request := mustRequest(t, `{
		"model":"auto",
		"messages":[{"role":"user","content":"Resolve this customer ticket"}],
		"routing":{"trace_id":"trace-treatment","workflow":"support","step":"draft","risk_class":"normal"}
	}`)
	decision := engine.Route("tenant", policy, request)
	if decision.SelectedModel != "balanced" {
		t.Fatalf("selected model = %q, want balanced; fallback=%s predictions=%+v", decision.SelectedModel, decision.FallbackReason, decision.Predictions)
	}
	if decision.EstimatedCostUSD >= decision.IncumbentEstimatedCostUSD {
		t.Fatalf("selected cost %.8f should be below incumbent %.8f", decision.EstimatedCostUSD, decision.IncumbentEstimatedCostUSD)
	}
}

func TestRouteHardConstraintsCannotBeOverriddenByScore(t *testing.T) {
	engine, policy := testEngineAndPolicy()
	policy.CandidateModels = []string{"economy"}
	policy.Snapshot.Global["economy"] = domain.ModelScore{SuccessRate: 0.99, Uncertainty: 0.001, Samples: 10000}
	request := mustRequest(t, `{
		"model":"auto",
		"messages":[{"role":"user","content":[{"type":"text","text":"Describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],
		"routing":{"trace_id":"vision","workflow":"support","risk_class":"normal","required_privacy":"zdr"}
	}`)
	decision := engine.Route("tenant", policy, request)
	if decision.SelectedModel != policy.IncumbentModel {
		t.Fatalf("selected model = %q, want incumbent", decision.SelectedModel)
	}
	if prediction := decision.Predictions["economy"]; prediction.Eligible || prediction.Reason != "vision_unsupported" {
		t.Fatalf("economy prediction = %+v, want vision constraint failure", prediction)
	}
}

func TestRouteDriftFailsClosedToIncumbent(t *testing.T) {
	engine, policy := testEngineAndPolicy()
	policy.Drifted = true
	request := mustRequest(t, `{"messages":[{"role":"user","content":"hello"}],"routing":{"trace_id":"drift"}}`)
	decision := engine.Route("tenant", policy, request)
	if decision.SelectedModel != "premium" || decision.FallbackReason != "policy_drifted" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestParseRequestPreservesUnknownFieldsAndRemovesRouting(t *testing.T) {
	request := mustRequest(t, `{
		"model":"auto",
		"messages":[{"role":"user","content":"hello"}],
		"routing":{"trace_id":"abc"},
		"vendor_extension":{"enabled":true}
	}`)
	body, err := request.BodyForModel("upstream-name", nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["routing"]; exists {
		t.Fatal("routing metadata leaked upstream")
	}
	if _, exists := decoded["vendor_extension"]; !exists {
		t.Fatal("unknown provider field was not preserved")
	}
	var model string
	_ = json.Unmarshal(decoded["model"], &model)
	if model != "upstream-name" {
		t.Fatalf("model = %q", model)
	}
}

func BenchmarkRoute(b *testing.B) {
	engine, policy := testEngineAndPolicy()
	request := mustRequest(b, `{"messages":[{"role":"user","content":"hello"}],"routing":{"trace_id":"benchmark"}}`)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		engine.Route("tenant", policy, request)
	}
}

type testingT interface {
	Helper()
	Fatal(...any)
}

func mustRequest(t testingT, body string) *Request {
	t.Helper()
	request, err := ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func testEngineAndPolicy() (*Engine, domain.Policy) {
	catalog := map[string]domain.Model{
		"premium": {
			ID: "premium", Provider: "p", ContextWindow: 128000, SupportsTools: true,
			SupportsStructured: true, SupportsVision: true, Privacy: domain.PrivacyZDR,
			InputCostPerMTokens: 5, OutputCostPerMTokens: 20, P95LatencyMS: 1000,
			QualityPrior: .92, Enabled: true,
		},
		"balanced": {
			ID: "balanced", Provider: "p", ContextWindow: 128000, SupportsTools: true,
			SupportsStructured: true, SupportsVision: true, Privacy: domain.PrivacyZDR,
			InputCostPerMTokens: 1, OutputCostPerMTokens: 4, P95LatencyMS: 700,
			QualityPrior: .91, Enabled: true,
		},
		"economy": {
			ID: "economy", Provider: "p", ContextWindow: 16000, SupportsStructured: true,
			Privacy: domain.PrivacyStandard, InputCostPerMTokens: .2, OutputCostPerMTokens: .8,
			P95LatencyMS: 400, QualityPrior: .85, Enabled: true,
		},
	}
	policy := domain.Policy{
		ID: "policy", TenantID: "tenant", Version: "1", Mode: domain.ModeActive,
		IncumbentModel: "premium", CandidateModels: []string{"balanced", "economy"},
		QualityTolerance: .03, ConfidenceZ: 1.645, MinimumSamples: 100,
		Constraints: domain.PolicyConstraints{MaxP95LatencyMS: 2000},
		Snapshot: domain.Snapshot{
			Version: "snapshot", CatalogVersion: "catalog", TrainedAt: time.Now(),
			Global: map[string]domain.ModelScore{
				"premium":  {SuccessRate: .92, Uncertainty: .004, Samples: 1000},
				"balanced": {SuccessRate: .91, Uncertainty: .005, Samples: 1000},
				"economy":  {SuccessRate: .85, Uncertainty: .01, Samples: 1000},
			},
		},
	}
	return &Engine{Catalog: catalog}, policy
}
