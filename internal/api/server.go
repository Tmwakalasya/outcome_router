package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/outcome-router/outcome-router/internal/config"
	"github.com/outcome-router/outcome-router/internal/domain"
	"github.com/outcome-router/outcome-router/internal/provider"
	"github.com/outcome-router/outcome-router/internal/routing"
	"github.com/outcome-router/outcome-router/internal/store"
)

type Server struct {
	Config         *config.Config
	Registry       *routing.Registry
	Engine         *routing.Engine
	Providers      *provider.Client
	Store          store.Repository
	Logger         *slog.Logger
	shadowMu       sync.Mutex
	shadowSpendUSD map[string]float64
}

func NewServer(
	cfg *config.Config,
	registry *routing.Registry,
	engine *routing.Engine,
	providers *provider.Client,
	repository store.Repository,
	logger *slog.Logger,
) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		Config: cfg, Registry: registry, Engine: engine, Providers: providers,
		Store: repository, Logger: logger, shadowSpendUSD: map[string]float64{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.health)
	mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
	mux.HandleFunc("POST /v1/outcomes", s.outcomes)
	mux.HandleFunc("GET /v1/decisions/{decisionID}", s.decision)
	mux.HandleFunc("GET /v1/audit/summary", s.auditSummary)
	mux.HandleFunc("GET /v1/policies", s.policies)
	mux.HandleFunc("POST /v1/policies", s.putPolicy)
	return s.recover(s.cors(mux))
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "ok", "time": time.Now().UTC(),
	})
}

func (s *Server) chatCompletions(writer http.ResponseWriter, request *http.Request) {
	tenant, ok := s.Config.Authenticate(request.Header.Get("Authorization"))
	if !ok {
		writeError(writer, http.StatusUnauthorized, "invalid_api_key", "invalid router API key")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 32<<20+1))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "unable to read request")
		return
	}
	parsed, err := routing.ParseRequest(body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	policyID := parsed.Metadata.PolicyID
	if policyID == "" {
		policyID = tenant.DefaultPolicyID
	}
	policy, ok := s.Registry.Get(policyID, tenant.ID)
	if !ok {
		writeError(writer, http.StatusBadRequest, "unknown_policy", "routing policy not found")
		return
	}
	decision := s.Engine.Route(tenant.ID, policy, parsed)
	if err := s.Store.SaveDecision(decision); err != nil {
		s.Logger.Error("save initial decision", "error", err, "decision_id", decision.ID)
		writeError(writer, http.StatusInternalServerError, "audit_unavailable", "unable to persist routing decision")
		return
	}

	attemptOrder := buildAttemptOrder(decision.SelectedModel, policy.FallbackModels, policy.IncumbentModel)
	var response *http.Response
	var finalModel domain.Model
	var lastError error
	providerStarted := time.Now()
	for _, modelID := range attemptOrder {
		prediction, predicted := decision.Predictions[modelID]
		if predicted && !prediction.Eligible {
			continue
		}
		model, exists := s.Config.Catalog[modelID]
		if !exists {
			continue
		}
		providerConfig, exists := tenant.Providers[model.Provider]
		if !exists {
			lastError = fmt.Errorf("provider %s is not configured for tenant", model.Provider)
			continue
		}
		upstreamBody, marshalErr := parsed.BodyForModel(model.UpstreamModel, nil)
		if marshalErr != nil {
			lastError = marshalErr
			break
		}
		decision.AttemptedModels = append(decision.AttemptedModels, modelID)
		response, lastError = s.Providers.Do(request.Context(), providerConfig, model, upstreamBody, request.Header)
		if lastError == nil {
			finalModel = model
			break
		}
		if !provider.Retryable(lastError) {
			break
		}
	}
	if response == nil {
		decision.Error = safeError(lastError)
		decision.ProviderLatencyMS = time.Since(providerStarted).Milliseconds()
		decision.CompletedAt = timePointer(time.Now().UTC())
		if upstreamStatus(lastError) != 0 {
			decision.UpstreamStatus = upstreamStatus(lastError)
		}
		_ = s.Store.SaveDecision(decision)
		s.writeUpstreamFailure(writer, lastError, decision)
		return
	}
	decision.FinalModel = finalModel.ID
	decision.ProviderLatencyMS = time.Since(providerStarted).Milliseconds()
	decision.UpstreamStatus = response.StatusCode
	if finalModel.ID != decision.SelectedModel {
		decision.FallbackReason = "selected_model_failed"
	}
	setDecisionHeaders(writer.Header(), decision)
	copyResponseHeaders(writer.Header(), response.Header)
	s.launchShadows(tenant, policy, parsed, decision, request.Header)

	if parsed.Stream || strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		writer.WriteHeader(response.StatusCode)
		_, copyErr := io.Copy(writer, response.Body)
		_ = response.Body.Close()
		if copyErr != nil {
			decision.Error = "stream interrupted"
		}
		decision.ActualCostUSD = decision.EstimatedCostUSD
		decision.ProviderLatencyMS = time.Since(providerStarted).Milliseconds()
		decision.CompletedAt = timePointer(time.Now().UTC())
		if err := s.Store.SaveDecision(decision); err != nil {
			s.Logger.Error("save streamed decision", "error", err, "decision_id", decision.ID)
		}
		return
	}

	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	_ = response.Body.Close()
	if readErr != nil {
		decision.Error = "unable to read upstream response"
		decision.CompletedAt = timePointer(time.Now().UTC())
		_ = s.Store.SaveDecision(decision)
		writeError(writer, http.StatusBadGateway, "upstream_read_error", decision.Error)
		return
	}
	inputTokens, outputTokens := parseUsage(responseBody)
	decision.InputTokens = inputTokens
	decision.OutputTokens = outputTokens
	if inputTokens > 0 || outputTokens > 0 {
		decision.ActualCostUSD = routing.ActualCost(finalModel, inputTokens, outputTokens)
		if incumbent, exists := s.Config.Catalog[decision.IncumbentModel]; exists {
			decision.IncumbentEstimatedCostUSD = routing.ActualCost(incumbent, inputTokens, outputTokens)
		}
	} else {
		decision.ActualCostUSD = decision.EstimatedCostUSD
	}
	decision.ProviderLatencyMS = time.Since(providerStarted).Milliseconds()
	decision.CompletedAt = timePointer(time.Now().UTC())
	if err := s.Store.SaveDecision(decision); err != nil {
		s.Logger.Error("save completed decision", "error", err, "decision_id", decision.ID)
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(responseBody)
}

func (s *Server) outcomes(writer http.ResponseWriter, request *http.Request) {
	tenant, ok := s.Config.Authenticate(request.Header.Get("Authorization"))
	if !ok {
		writeError(writer, http.StatusUnauthorized, "invalid_api_key", "invalid router API key")
		return
	}
	var outcome domain.Outcome
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	if err := decoder.Decode(&outcome); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_outcome", "invalid outcome payload")
		return
	}
	if outcome.DecisionID == "" && outcome.TraceID == "" {
		writeError(writer, http.StatusBadRequest, "invalid_outcome", "decision_id or trace_id is required")
		return
	}
	if strings.TrimSpace(outcome.Type) == "" || len(outcome.Value) == 0 {
		writeError(writer, http.StatusBadRequest, "invalid_outcome", "type and value are required")
		return
	}
	if outcome.DecisionID != "" {
		if _, exists := s.Store.GetDecision(tenant.ID, outcome.DecisionID); !exists {
			writeError(writer, http.StatusNotFound, "unknown_decision", "decision not found")
			return
		}
	}
	outcome.ID = newRecordID("out")
	outcome.TenantID = tenant.ID
	if outcome.OccurredAt.IsZero() {
		outcome.OccurredAt = time.Now().UTC()
	}
	outcome.CreatedAt = time.Now().UTC()
	if err := s.Store.SaveOutcome(outcome); err != nil {
		s.Logger.Error("save outcome", "error", err)
		writeError(writer, http.StatusInternalServerError, "outcome_unavailable", "unable to persist outcome")
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"id": outcome.ID, "accepted": true})
}

func (s *Server) decision(writer http.ResponseWriter, request *http.Request) {
	tenant, ok := s.Config.Authenticate(request.Header.Get("Authorization"))
	if !ok {
		writeError(writer, http.StatusUnauthorized, "invalid_api_key", "invalid router API key")
		return
	}
	decision, exists := s.Store.GetDecision(tenant.ID, request.PathValue("decisionID"))
	if !exists {
		writeError(writer, http.StatusNotFound, "unknown_decision", "decision not found")
		return
	}
	writeJSON(writer, http.StatusOK, decision)
}

func (s *Server) auditSummary(writer http.ResponseWriter, request *http.Request) {
	tenant, ok := s.Config.Authenticate(request.Header.Get("Authorization"))
	if !ok {
		writeError(writer, http.StatusUnauthorized, "invalid_api_key", "invalid router API key")
		return
	}
	days := 30
	if raw := request.URL.Query().Get("days"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}
	policyID := request.URL.Query().Get("policy_id")
	primaryOutcome := request.URL.Query().Get("primary_outcome")
	if primaryOutcome == "" {
		if policy, exists := s.Registry.Get(policyID, tenant.ID); exists {
			primaryOutcome = policy.PrimaryOutcome
		} else if policy, exists := s.Registry.Get(tenant.DefaultPolicyID, tenant.ID); exists {
			primaryOutcome = policy.PrimaryOutcome
		}
	}
	summary := s.Store.Summary(tenant.ID, policyID, primaryOutcome, time.Now().UTC().Add(-time.Duration(days)*24*time.Hour))
	writeJSON(writer, http.StatusOK, summary)
}

func (s *Server) policies(writer http.ResponseWriter, request *http.Request) {
	tenant, ok := s.Config.Authenticate(request.Header.Get("Authorization"))
	if !ok {
		writeError(writer, http.StatusUnauthorized, "invalid_api_key", "invalid router API key")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": s.Registry.List(tenant.ID)})
}

func (s *Server) putPolicy(writer http.ResponseWriter, request *http.Request) {
	if !s.Config.AuthenticateAdmin(request.Header.Get("Authorization")) {
		writeError(writer, http.StatusUnauthorized, "invalid_admin_key", "invalid administrator API key")
		return
	}
	var policy domain.Policy
	if err := json.NewDecoder(io.LimitReader(request.Body, 4<<20)).Decode(&policy); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_policy", "invalid policy payload")
		return
	}
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = time.Now().UTC()
	}
	if err := s.Registry.Put(policy); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_policy", err.Error())
		return
	}
	writeJSON(writer, http.StatusCreated, policy)
}

func (s *Server) launchShadows(
	tenant *config.Tenant,
	policy domain.Policy,
	request *routing.Request,
	parent domain.RouteDecision,
	headers http.Header,
) {
	if len(parent.ShadowModels) == 0 || parent.RiskClass == domain.RiskRegulated || parent.RiskClass == domain.RiskHigh {
		return
	}
	for _, modelID := range parent.ShadowModels {
		if modelID == parent.FinalModel {
			continue
		}
		prediction := parent.Predictions[modelID]
		if !s.reserveShadowBudget(tenant.ID, policy, prediction.EstimatedCostUSD) {
			continue
		}
		model := s.Config.Catalog[modelID]
		providerConfig, exists := tenant.Providers[model.Provider]
		if !exists {
			continue
		}
		go func(model domain.Model, providerConfig config.Provider, prediction domain.Prediction) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			stream := false
			body, err := request.BodyForModel(model.UpstreamModel, &stream)
			shadow := parent
			shadow.ID = newRecordID("dec")
			shadow.ParentDecisionID = parent.ID
			shadow.SelectedModel = model.ID
			shadow.FinalModel = ""
			shadow.Bucket = "shadow"
			shadow.ShadowModels = nil
			shadow.AttemptedModels = []string{model.ID}
			shadow.EstimatedCostUSD = prediction.EstimatedCostUSD
			shadow.ActualCostUSD = 0
			shadow.CreatedAt = time.Now().UTC()
			shadow.CompletedAt = nil
			shadow.Error = ""
			if err != nil {
				shadow.Error = "unable to encode shadow request"
				shadow.CompletedAt = timePointer(time.Now().UTC())
				_ = s.Store.SaveDecision(shadow)
				return
			}
			started := time.Now()
			response, err := s.Providers.Do(ctx, providerConfig, model, body, headers)
			shadow.ProviderLatencyMS = time.Since(started).Milliseconds()
			if err != nil {
				shadow.Error = safeError(err)
				shadow.UpstreamStatus = upstreamStatus(err)
			} else {
				responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 64<<20))
				_ = response.Body.Close()
				inputTokens, outputTokens := parseUsage(responseBody)
				shadow.InputTokens = inputTokens
				shadow.OutputTokens = outputTokens
				shadow.UpstreamStatus = response.StatusCode
				shadow.FinalModel = model.ID
				if inputTokens > 0 || outputTokens > 0 {
					shadow.ActualCostUSD = routing.ActualCost(model, inputTokens, outputTokens)
				} else {
					shadow.ActualCostUSD = prediction.EstimatedCostUSD
				}
			}
			shadow.CompletedAt = timePointer(time.Now().UTC())
			if saveErr := s.Store.SaveDecision(shadow); saveErr != nil {
				s.Logger.Error("save shadow decision", "error", saveErr, "decision_id", shadow.ID)
			}
		}(model, providerConfig, prediction)
	}
}

func (s *Server) reserveShadowBudget(tenantID string, policy domain.Policy, estimatedCost float64) bool {
	key := tenantID + "|" + policy.ID + "|" + time.Now().UTC().Format("2006-01-02")
	s.shadowMu.Lock()
	defer s.shadowMu.Unlock()
	if s.shadowSpendUSD[key]+estimatedCost > policy.ShadowBudgetUSDPerDay {
		return false
	}
	s.shadowSpendUSD[key] += estimatedCost
	return true
}

func (s *Server) writeUpstreamFailure(writer http.ResponseWriter, err error, decision domain.RouteDecision) {
	var upstream *provider.UpstreamError
	if errors.As(err, &upstream) && len(upstream.Body) > 0 {
		writer.Header().Set("Content-Type", "application/json")
		setDecisionHeaders(writer.Header(), decision)
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write(upstream.Body)
		return
	}
	setDecisionHeaders(writer.Header(), decision)
	writeError(writer, http.StatusBadGateway, "upstream_unavailable", "all eligible models failed")
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Outcome-Router-Admin")
		writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		writer.Header().Set("Access-Control-Expose-Headers", "X-Outcome-Decision-ID, X-Outcome-Model, X-Outcome-Policy-Version, X-Outcome-Fallback-Reason")
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				s.Logger.Error("request panic", "panic", value, "path", request.URL.Path)
				writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

func buildAttemptOrder(selected string, fallbacks []string, incumbent string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, 2+len(fallbacks))
	for _, modelID := range append(append([]string{selected}, fallbacks...), incumbent) {
		if modelID != "" && !seen[modelID] {
			seen[modelID] = true
			result = append(result, modelID)
		}
	}
	return result
}

func setDecisionHeaders(headers http.Header, decision domain.RouteDecision) {
	headers.Set("X-Outcome-Decision-ID", decision.ID)
	model := decision.FinalModel
	if model == "" {
		model = decision.SelectedModel
	}
	headers.Set("X-Outcome-Model", model)
	headers.Set("X-Outcome-Policy-Version", decision.PolicyVersion)
	if decision.FallbackReason != "" {
		headers.Set("X-Outcome-Fallback-Reason", decision.FallbackReason)
	}
}

func copyResponseHeaders(destination, source http.Header) {
	for _, key := range []string{"Content-Type", "Cache-Control", "OpenAI-Organization", "OpenAI-Processing-Ms", "X-Request-ID"} {
		if value := source.Get(key); value != "" {
			destination.Set(key, value)
		}
	}
}

func parseUsage(body []byte) (int, int) {
	var response struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil {
		return 0, 0
	}
	input := response.Usage.PromptTokens
	if input == 0 {
		input = response.Usage.InputTokens
	}
	output := response.Usage.CompletionTokens
	if output == 0 {
		output = response.Usage.OutputTokens
	}
	return input, output
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]any{"message": message, "type": code, "code": code},
	})
}

func upstreamStatus(err error) int {
	var upstream *provider.UpstreamError
	if errors.As(err, &upstream) {
		return upstream.StatusCode
	}
	return 0
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	var upstream *provider.UpstreamError
	if errors.As(err, &upstream) {
		return upstream.Error()
	}
	return "upstream request failed"
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func newRecordID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
