package routing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/outcome-router/outcome-router/internal/domain"
)

type ProviderHealth interface {
	Available(providerID string) bool
}

type Engine struct {
	Catalog map[string]domain.Model
	Health  ProviderHealth
	Now     func() time.Time
}

func (e *Engine) Route(tenantID string, policy domain.Policy, request *Request) domain.RouteDecision {
	started := time.Now()
	now := started.UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	traceID := request.Metadata.TraceID
	if traceID == "" {
		traceID = newID("trace")
	}
	decision := domain.RouteDecision{
		ID:              newID("dec"),
		TenantID:        tenantID,
		TraceID:         traceID,
		Workflow:        request.Metadata.Workflow,
		Step:            request.Metadata.Step,
		RiskClass:       request.Metadata.RiskClass,
		PolicyID:        policy.ID,
		PolicyVersion:   policy.Version,
		SnapshotVersion: policy.Snapshot.Version,
		CatalogVersion:  policy.Snapshot.CatalogVersion,
		IncumbentModel:  policy.IncumbentModel,
		SelectedModel:   policy.IncumbentModel,
		Bucket:          "incumbent",
		Predictions:     map[string]domain.Prediction{},
		Features:        request.Features,
		CreatedAt:       now,
	}

	modelIDs := unique(append(append([]string{policy.IncumbentModel}, policy.CandidateModels...), policy.FallbackModels...))
	for _, modelID := range modelIDs {
		model, ok := e.Catalog[modelID]
		if !ok {
			continue
		}
		prediction := e.predict(policy, model, request)
		decision.Predictions[modelID] = prediction
		if prediction.Eligible {
			decision.FeasibleModels = append(decision.FeasibleModels, modelID)
		}
	}
	incumbent := decision.Predictions[policy.IncumbentModel]
	decision.EstimatedCostUSD = incumbent.EstimatedCostUSD
	decision.IncumbentEstimatedCostUSD = incumbent.EstimatedCostUSD

	if policy.Drifted {
		decision.FallbackReason = "policy_drifted"
		return finishRoutingLatency(decision, started)
	}
	if policy.Mode == domain.ModeObserve {
		decision.FallbackReason = "observe_only"
		return finishRoutingLatency(decision, started)
	}

	decision.ShadowModels = e.shadowCandidates(policy, decision.Predictions)
	if policy.Mode == domain.ModeShadow {
		decision.FallbackReason = "shadow_only"
		return finishRoutingLatency(decision, started)
	}

	bucket := hashPercent(traceID + "|" + policy.Version)
	if bucket < policy.ControlPercent {
		decision.Bucket = "control"
		decision.FallbackReason = "randomized_control"
		return finishRoutingLatency(decision, started)
	}
	if policy.Mode == domain.ModeCanary && bucket >= policy.ControlPercent+policy.CanaryPercent {
		decision.FallbackReason = "outside_canary"
		return finishRoutingLatency(decision, started)
	}

	qualityFloor := incumbent.Success - policy.QualityTolerance
	candidates := make([]domain.Prediction, 0, len(policy.CandidateModels))
	for _, modelID := range policy.CandidateModels {
		prediction := decision.Predictions[modelID]
		if !prediction.Eligible || prediction.Samples < policy.MinimumSamples {
			continue
		}
		if prediction.LowerBound+1e-12 < qualityFloor {
			continue
		}
		candidates = append(candidates, prediction)
	}
	if len(candidates) == 0 {
		decision.FallbackReason = "no_candidate_meets_quality_floor"
		return finishRoutingLatency(decision, started)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if math.Abs(candidates[i].EstimatedCostUSD-candidates[j].EstimatedCostUSD) < 1e-12 {
			return candidates[i].LowerBound > candidates[j].LowerBound
		}
		return candidates[i].EstimatedCostUSD < candidates[j].EstimatedCostUSD
	})

	selected := candidates[0]
	if request.Metadata.RiskClass == domain.RiskLow && policy.ExplorationPercent > 0 {
		exploreBucket := hashPercent(traceID + "|explore|" + policy.Version)
		if exploreBucket < policy.ExplorationPercent {
			index := int(hashPercent(traceID+"|arm")/100*float64(len(candidates))) % len(candidates)
			selected = candidates[index]
			decision.Bucket = "exploration"
		} else {
			decision.Bucket = "treatment"
		}
	} else {
		decision.Bucket = "treatment"
	}
	decision.SelectedModel = selected.ModelID
	decision.EstimatedCostUSD = selected.EstimatedCostUSD
	if selected.ModelID == policy.IncumbentModel {
		decision.FallbackReason = "incumbent_is_optimal"
	}
	return finishRoutingLatency(decision, started)
}

func (e *Engine) predict(policy domain.Policy, model domain.Model, request *Request) domain.Prediction {
	prediction := domain.Prediction{
		ModelID:          model.ID,
		P95LatencyMS:     model.P95LatencyMS,
		EstimatedCostUSD: estimateCost(model, request.Features),
		Eligible:         true,
	}
	if reason := e.infeasibleReason(policy, model, request, prediction.EstimatedCostUSD); reason != "" {
		prediction.Eligible = false
		prediction.Reason = reason
	}

	score, ok := workflowScore(policy.Snapshot, request.Metadata.Workflow, model.ID)
	if !ok {
		score = domain.ModelScore{SuccessRate: model.QualityPrior, Uncertainty: 0.25}
	}
	success := score.SuccessRate + score.Bias
	for name, value := range request.Features.Vector() {
		success += score.Weights[name] * value
	}
	prediction.Success = clamp(success, 0, 1)
	prediction.Uncertainty = clamp(score.Uncertainty, 0.005, 0.5)
	prediction.Samples = score.Samples
	prediction.LowerBound = clamp(prediction.Success-policy.ConfidenceZ*prediction.Uncertainty, 0, 1)
	return prediction
}

func (e *Engine) infeasibleReason(policy domain.Policy, model domain.Model, request *Request, cost float64) string {
	if !model.Enabled {
		return "model_disabled"
	}
	if e.Health != nil && !e.Health.Available(model.Provider) {
		return "provider_circuit_open"
	}
	neededTokens := request.Features.InputTokens + request.Features.ExpectedOutputTokens
	if model.ContextWindow > 0 && neededTokens > model.ContextWindow {
		return "context_window"
	}
	if request.Features.ToolCount > 0 && !model.SupportsTools {
		return "tools_unsupported"
	}
	if request.Features.HasStructuredOutput && !model.SupportsStructured {
		return "structured_output_unsupported"
	}
	if request.Features.HasVision && !model.SupportsVision {
		return "vision_unsupported"
	}
	requiredRegion := request.Metadata.RequiredRegion
	if requiredRegion != "" && !strings.EqualFold(model.Region, requiredRegion) {
		return "region"
	}
	if len(policy.Constraints.AllowedRegions) > 0 && !containsFold(policy.Constraints.AllowedRegions, model.Region) {
		return "policy_region"
	}
	requiredPrivacy := request.Metadata.RequiredPrivacy
	if requiredPrivacy == "" {
		requiredPrivacy = policy.Constraints.RequiredPrivacy
	}
	if !privacySatisfies(model.Privacy, requiredPrivacy) {
		return "privacy"
	}
	deadline := request.Metadata.DeadlineMS
	if deadline == 0 {
		deadline = policy.Constraints.MaxP95LatencyMS
	}
	if deadline > 0 && model.P95LatencyMS > deadline {
		return "latency"
	}
	maxCost := request.Metadata.MaxCostUSD
	if maxCost == 0 {
		maxCost = policy.Constraints.MaxEstimatedCostUSD
	}
	if maxCost > 0 && cost > maxCost {
		return "cost"
	}
	return ""
}

func (e *Engine) shadowCandidates(policy domain.Policy, predictions map[string]domain.Prediction) []string {
	if policy.ShadowBudgetUSDPerDay <= 0 {
		return nil
	}
	result := make([]string, 0)
	for _, modelID := range policy.CandidateModels {
		if prediction := predictions[modelID]; prediction.Eligible {
			result = append(result, modelID)
		}
	}
	return result
}

func workflowScore(snapshot domain.Snapshot, workflow, modelID string) (domain.ModelScore, bool) {
	if workflowScores, ok := snapshot.ByWorkflow[workflow]; ok {
		if score, found := workflowScores[modelID]; found {
			return score, true
		}
	}
	score, ok := snapshot.Global[modelID]
	return score, ok
}

func estimateCost(model domain.Model, features domain.Features) float64 {
	return (float64(features.InputTokens)*model.InputCostPerMTokens +
		float64(features.ExpectedOutputTokens)*model.OutputCostPerMTokens) / 1_000_000
}

func ActualCost(model domain.Model, inputTokens, outputTokens int) float64 {
	return (float64(inputTokens)*model.InputCostPerMTokens +
		float64(outputTokens)*model.OutputCostPerMTokens) / 1_000_000
}

func finishRoutingLatency(decision domain.RouteDecision, started time.Time) domain.RouteDecision {
	decision.RoutingLatencyMicros = time.Since(started).Microseconds()
	return decision
}

func privacySatisfies(actual, required domain.Privacy) bool {
	if required == "" || required == domain.PrivacyStandard {
		return true
	}
	if required == domain.PrivacyZDR {
		return actual == domain.PrivacyZDR || actual == domain.PrivacyLocal
	}
	return actual == domain.PrivacyLocal
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func unique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func clamp(value, minimum, maximum float64) float64 {
	return math.Max(minimum, math.Min(maximum, value))
}

func hashPercent(value string) float64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(value))
	return float64(hash.Sum64()%1_000_000) / 10_000
}

func newID(prefix string) string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buffer)
}
