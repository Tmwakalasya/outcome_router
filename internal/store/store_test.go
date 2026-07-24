package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/outcome-router/outcome-router/internal/domain"
)

func TestSummaryUsesUniqueTraceOutcomes(t *testing.T) {
	repository, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, decision := range []domain.RouteDecision{
		{ID: "d1", TenantID: "t", TraceID: "trace-treatment", PolicyID: "p", Bucket: "treatment", IncumbentModel: "premium", SelectedModel: "balanced", EstimatedCostUSD: 1, IncumbentEstimatedCostUSD: 4, CreatedAt: now},
		{ID: "d2", TenantID: "t", TraceID: "trace-treatment", PolicyID: "p", Bucket: "treatment", IncumbentModel: "premium", SelectedModel: "balanced", EstimatedCostUSD: 1, IncumbentEstimatedCostUSD: 4, CreatedAt: now},
		{ID: "d3", TenantID: "t", TraceID: "trace-control", PolicyID: "p", Bucket: "control", IncumbentModel: "premium", SelectedModel: "premium", EstimatedCostUSD: 4, IncumbentEstimatedCostUSD: 4, CreatedAt: now},
	} {
		if err := repository.SaveDecision(decision); err != nil {
			t.Fatal(err)
		}
	}
	for index, outcome := range []domain.Outcome{
		{ID: "o1", TenantID: "t", TraceID: "trace-treatment", Type: "resolution", Value: json.RawMessage("true"), CreatedAt: now},
		{ID: "o2", TenantID: "t", TraceID: "trace-control", Type: "resolution", Value: json.RawMessage("false"), CreatedAt: now},
	} {
		outcome.OccurredAt = now.Add(time.Duration(index) * time.Second)
		if err := repository.SaveOutcome(outcome); err != nil {
			t.Fatal(err)
		}
	}
	summary := repository.Summary("t", "p", "resolution", now.Add(-time.Hour))
	if summary.Requests != 3 || summary.RoutedRequests != 2 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if summary.TreatmentOutcome.N != 1 || summary.TreatmentOutcome.Estimate != 1 {
		t.Fatalf("treatment outcome = %+v, want one unique successful trace", summary.TreatmentOutcome)
	}
	if summary.ControlOutcome.N != 1 || summary.ControlOutcome.Estimate != 0 {
		t.Fatalf("control outcome = %+v", summary.ControlOutcome)
	}
	if summary.VerifiedGrossSavingsUSD != 6 {
		t.Fatalf("savings = %f, want 6", summary.VerifiedGrossSavingsUSD)
	}
}

func TestFileStoreReloadsLatestDecisionVersion(t *testing.T) {
	directory := t.TempDir()
	repository, err := NewFileStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	decision := domain.RouteDecision{ID: "d", TenantID: "t", SelectedModel: "a", CreatedAt: time.Now()}
	if err := repository.SaveDecision(decision); err != nil {
		t.Fatal(err)
	}
	decision.FinalModel = "b"
	if err := repository.SaveDecision(decision); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFileStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.GetDecision("t", "d")
	if !ok || got.FinalModel != "b" {
		t.Fatalf("reloaded = %+v, ok=%v", got, ok)
	}
}
