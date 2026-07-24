package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outcome-router/outcome-router/internal/config"
	"github.com/outcome-router/outcome-router/internal/domain"
	"github.com/outcome-router/outcome-router/internal/provider"
	"github.com/outcome-router/outcome-router/internal/routing"
	"github.com/outcome-router/outcome-router/internal/store"
)

func TestChatCompletionAndOutcomeRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, leaked := body["routing"]; leaked {
			t.Error("routing metadata leaked upstream")
		}
		var model string
		_ = json.Unmarshal(body["model"], &model)
		writeJSON(writer, http.StatusOK, map[string]any{
			"id": "upstream", "model": model,
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer upstream.Close()
	server, repository := testServer(t, upstream.URL)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	body := `{"model":"auto","messages":[{"role":"user","content":"help"}],"routing":{"trace_id":"trace-api","workflow":"support"}}`
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer router-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, responseBody)
	}
	decisionID := response.Header.Get("X-Outcome-Decision-ID")
	if decisionID == "" {
		t.Fatal("missing decision header")
	}
	decision, ok := repository.GetDecision("tenant", decisionID)
	if !ok || decision.FinalModel != "balanced" || decision.ActualCostUSD <= 0 {
		t.Fatalf("decision=%+v ok=%v", decision, ok)
	}

	outcomeBody := `{"decision_id":"` + decisionID + `","type":"resolution","value":true}`
	outcomeRequest, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/outcomes", strings.NewReader(outcomeBody))
	outcomeRequest.Header.Set("Authorization", "Bearer router-key")
	outcomeResponse, err := http.DefaultClient.Do(outcomeRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = outcomeResponse.Body.Close()
	if outcomeResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("outcome status=%d", outcomeResponse.StatusCode)
	}

	summaryRequest, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/audit/summary?policy_id=policy", nil)
	summaryRequest.Header.Set("Authorization", "Bearer router-key")
	summaryResponse, err := http.DefaultClient.Do(summaryRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer summaryResponse.Body.Close()
	var summary domain.AuditSummary
	if err := json.NewDecoder(summaryResponse.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 1 || summary.RoutedRequests != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	if summary.SavingsPercent < 79.9 || summary.SavingsPercent > 80.1 {
		t.Fatalf("savings percent=%f, want token-matched 80%%", summary.SavingsPercent)
	}
	if model := summary.ByModel["balanced"]; model.Outcomes != 1 || model.SuccessRate != 1 {
		t.Fatalf("balanced outcome summary=%+v", model)
	}
}

func TestStreamingIsPassedThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	server, _ := testServer(t, upstream.URL)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"help"}],"routing":{"trace_id":"stream"}}`
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer router-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	foundDone := false
	for scanner.Scan() {
		if scanner.Text() == "data: [DONE]" {
			foundDone = true
		}
	}
	if !foundDone || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream missing done=%v content-type=%q", foundDone, response.Header.Get("Content-Type"))
	}
}

func testServer(t *testing.T, upstreamURL string) (*Server, *store.FileStore) {
	t.Helper()
	catalog := map[string]domain.Model{
		"premium":  {ID: "premium", Provider: "provider", UpstreamModel: "premium-upstream", Enabled: true, ContextWindow: 10000, SupportsTools: true, SupportsStructured: true, SupportsVision: true, QualityPrior: .92, InputCostPerMTokens: 5, OutputCostPerMTokens: 20, P95LatencyMS: 1000},
		"balanced": {ID: "balanced", Provider: "provider", UpstreamModel: "balanced-upstream", Enabled: true, ContextWindow: 10000, SupportsTools: true, SupportsStructured: true, SupportsVision: true, QualityPrior: .91, InputCostPerMTokens: 1, OutputCostPerMTokens: 4, P95LatencyMS: 600},
	}
	policy := domain.Policy{
		ID: "policy", TenantID: "tenant", Version: "1", Mode: domain.ModeActive,
		IncumbentModel: "premium", CandidateModels: []string{"balanced"},
		QualityTolerance: .03, ConfidenceZ: 1.645, MinimumSamples: 10, PrimaryOutcome: "resolution",
		Snapshot: domain.Snapshot{Version: "snapshot", CatalogVersion: "catalog", Global: map[string]domain.ModelScore{
			"premium":  {SuccessRate: .92, Uncertainty: .003, Samples: 100},
			"balanced": {SuccessRate: .91, Uncertainty: .003, Samples: 100},
		}},
	}
	cfg := &config.Config{
		AdminKey: "admin", Catalog: catalog, Policies: []domain.Policy{policy},
		Tenants: []config.Tenant{{
			ID: "tenant", RouterAPIKey: "router-key", DefaultPolicyID: "policy",
			Providers: map[string]config.Provider{"provider": {ID: "provider", BaseURL: upstreamURL, TimeoutMS: 5000}},
		}},
	}
	repository, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	providerClient := provider.NewClient(nil)
	registry := routing.NewRegistry(catalog, []domain.Policy{policy}, repository)
	engine := &routing.Engine{Catalog: catalog, Health: providerClient}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(cfg, registry, engine, providerClient, repository, logger), repository
}
