package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type Mode string

const (
	ModeObserve Mode = "observe"
	ModeShadow  Mode = "shadow"
	ModeCanary  Mode = "canary"
	ModeActive  Mode = "active"
)

type Privacy string

const (
	PrivacyStandard Privacy = "standard"
	PrivacyZDR      Privacy = "zdr"
	PrivacyLocal    Privacy = "local"
)

type RiskClass string

const (
	RiskLow       RiskClass = "low"
	RiskNormal    RiskClass = "normal"
	RiskHigh      RiskClass = "high"
	RiskRegulated RiskClass = "regulated"
)

type Model struct {
	ID                   string  `json:"id"`
	Provider             string  `json:"provider"`
	UpstreamModel        string  `json:"upstream_model"`
	Region               string  `json:"region"`
	Privacy              Privacy `json:"privacy"`
	ContextWindow        int     `json:"context_window"`
	SupportsTools        bool    `json:"supports_tools"`
	SupportsStructured   bool    `json:"supports_structured"`
	SupportsVision       bool    `json:"supports_vision"`
	InputCostPerMTokens  float64 `json:"input_cost_per_million_tokens"`
	OutputCostPerMTokens float64 `json:"output_cost_per_million_tokens"`
	P95LatencyMS         int     `json:"p95_latency_ms"`
	QualityPrior         float64 `json:"quality_prior"`
	Enabled              bool    `json:"enabled"`
	Version              string  `json:"version"`
}

type PolicyConstraints struct {
	AllowedRegions      []string `json:"allowed_regions,omitempty"`
	RequiredPrivacy     Privacy  `json:"required_privacy,omitempty"`
	MaxEstimatedCostUSD float64  `json:"max_estimated_cost_usd,omitempty"`
	MaxP95LatencyMS     int      `json:"max_p95_latency_ms,omitempty"`
}

type Policy struct {
	ID                    string            `json:"id"`
	TenantID              string            `json:"tenant_id"`
	Version               string            `json:"version"`
	Mode                  Mode              `json:"mode"`
	IncumbentModel        string            `json:"incumbent_model"`
	CandidateModels       []string          `json:"candidate_models"`
	FallbackModels        []string          `json:"fallback_models,omitempty"`
	QualityTolerance      float64           `json:"quality_tolerance"`
	ConfidenceZ           float64           `json:"confidence_z"`
	MinimumSamples        int               `json:"minimum_samples"`
	ControlPercent        float64           `json:"control_percent"`
	CanaryPercent         float64           `json:"canary_percent"`
	ExplorationPercent    float64           `json:"exploration_percent"`
	ShadowBudgetUSDPerDay float64           `json:"shadow_budget_usd_per_day"`
	PrimaryOutcome        string            `json:"primary_outcome"`
	Constraints           PolicyConstraints `json:"constraints"`
	Snapshot              Snapshot          `json:"snapshot"`
	Drifted               bool              `json:"drifted"`
	CreatedAt             time.Time         `json:"created_at"`
}

type ModelScore struct {
	SuccessRate float64            `json:"success_rate"`
	Uncertainty float64            `json:"uncertainty"`
	Samples     int                `json:"samples"`
	Weights     map[string]float64 `json:"weights,omitempty"`
	Bias        float64            `json:"bias,omitempty"`
}

type Snapshot struct {
	Version        string                           `json:"version"`
	CatalogVersion string                           `json:"catalog_version"`
	TrainedAt      time.Time                        `json:"trained_at"`
	Global         map[string]ModelScore            `json:"global"`
	ByWorkflow     map[string]map[string]ModelScore `json:"by_workflow,omitempty"`
}

type RoutingMetadata struct {
	TraceID         string    `json:"trace_id"`
	Workflow        string    `json:"workflow"`
	Step            string    `json:"step"`
	RiskClass       RiskClass `json:"risk_class"`
	DeadlineMS      int       `json:"deadline_ms,omitempty"`
	PolicyID        string    `json:"policy_id,omitempty"`
	RequiredRegion  string    `json:"required_region,omitempty"`
	RequiredPrivacy Privacy   `json:"required_privacy,omitempty"`
	MaxCostUSD      float64   `json:"max_cost_usd,omitempty"`
}

type Features struct {
	InputTokens          int     `json:"input_tokens"`
	ExpectedOutputTokens int     `json:"expected_output_tokens"`
	MessageCount         int     `json:"message_count"`
	ToolCount            int     `json:"tool_count"`
	HasVision            bool    `json:"has_vision"`
	HasStructuredOutput  bool    `json:"has_structured_output"`
	Risk                 float64 `json:"risk"`
}

func (f Features) Vector() map[string]float64 {
	boolFloat := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	return map[string]float64{
		"input_tokens_k":           float64(f.InputTokens) / 1000,
		"expected_output_tokens_k": float64(f.ExpectedOutputTokens) / 1000,
		"message_count":            float64(f.MessageCount),
		"tool_count":               float64(f.ToolCount),
		"has_vision":               boolFloat(f.HasVision),
		"has_structured_output":    boolFloat(f.HasStructuredOutput),
		"risk":                     f.Risk,
	}
}

type Prediction struct {
	ModelID          string  `json:"model_id"`
	Success          float64 `json:"success"`
	LowerBound       float64 `json:"lower_bound"`
	Uncertainty      float64 `json:"uncertainty"`
	Samples          int     `json:"samples"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	P95LatencyMS     int     `json:"p95_latency_ms"`
	Eligible         bool    `json:"eligible"`
	Reason           string  `json:"reason,omitempty"`
}

type RouteDecision struct {
	ID                        string                `json:"id"`
	ParentDecisionID          string                `json:"parent_decision_id,omitempty"`
	TenantID                  string                `json:"tenant_id"`
	TraceID                   string                `json:"trace_id"`
	Workflow                  string                `json:"workflow"`
	Step                      string                `json:"step"`
	RiskClass                 RiskClass             `json:"risk_class"`
	PolicyID                  string                `json:"policy_id"`
	PolicyVersion             string                `json:"policy_version"`
	SnapshotVersion           string                `json:"snapshot_version"`
	CatalogVersion            string                `json:"catalog_version"`
	IncumbentModel            string                `json:"incumbent_model"`
	SelectedModel             string                `json:"selected_model"`
	FinalModel                string                `json:"final_model,omitempty"`
	Bucket                    string                `json:"bucket"`
	FallbackReason            string                `json:"fallback_reason,omitempty"`
	FeasibleModels            []string              `json:"feasible_models"`
	ShadowModels              []string              `json:"shadow_models,omitempty"`
	Predictions               map[string]Prediction `json:"predictions"`
	Features                  Features              `json:"features"`
	EstimatedCostUSD          float64               `json:"estimated_cost_usd"`
	IncumbentEstimatedCostUSD float64               `json:"incumbent_estimated_cost_usd"`
	ActualCostUSD             float64               `json:"actual_cost_usd,omitempty"`
	InputTokens               int                   `json:"input_tokens,omitempty"`
	OutputTokens              int                   `json:"output_tokens,omitempty"`
	RoutingLatencyMicros      int64                 `json:"routing_latency_micros"`
	ProviderLatencyMS         int64                 `json:"provider_latency_ms,omitempty"`
	AttemptedModels           []string              `json:"attempted_models,omitempty"`
	UpstreamStatus            int                   `json:"upstream_status,omitempty"`
	Error                     string                `json:"error,omitempty"`
	CreatedAt                 time.Time             `json:"created_at"`
	CompletedAt               *time.Time            `json:"completed_at,omitempty"`
}

type Outcome struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id"`
	DecisionID string          `json:"decision_id,omitempty"`
	TraceID    string          `json:"trace_id,omitempty"`
	Type       string          `json:"type"`
	Value      json.RawMessage `json:"value"`
	OccurredAt time.Time       `json:"occurred_at"`
	Metadata   map[string]any  `json:"metadata,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (o Outcome) NumericValue() (float64, bool) {
	var number float64
	if err := json.Unmarshal(o.Value, &number); err == nil && !math.IsNaN(number) {
		return number, true
	}
	var boolean bool
	if err := json.Unmarshal(o.Value, &boolean); err == nil {
		if boolean {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

type ModelSummary struct {
	Requests    int     `json:"requests"`
	CostUSD     float64 `json:"cost_usd"`
	Successes   float64 `json:"successes"`
	Outcomes    int     `json:"outcomes"`
	SuccessRate float64 `json:"success_rate,omitempty"`
}

type Interval struct {
	Estimate float64 `json:"estimate"`
	Lower    float64 `json:"lower"`
	Upper    float64 `json:"upper"`
	N        int     `json:"n"`
}

type AuditSummary struct {
	TenantID                string                  `json:"tenant_id"`
	PolicyID                string                  `json:"policy_id,omitempty"`
	Since                   time.Time               `json:"since"`
	Requests                int                     `json:"requests"`
	RoutedRequests          int                     `json:"routed_requests"`
	ControlRequests         int                     `json:"control_requests"`
	ShadowRequests          int                     `json:"shadow_requests"`
	ActualCostUSD           float64                 `json:"actual_cost_usd"`
	EstimatedCostUSD        float64                 `json:"estimated_cost_usd"`
	IncumbentCostUSD        float64                 `json:"incumbent_cost_usd"`
	VerifiedGrossSavingsUSD float64                 `json:"verified_gross_savings_usd"`
	SavingsPercent          float64                 `json:"savings_percent"`
	PrimaryOutcome          string                  `json:"primary_outcome,omitempty"`
	TreatmentOutcome        Interval                `json:"treatment_outcome"`
	ControlOutcome          Interval                `json:"control_outcome"`
	NonInferiorityDelta     float64                 `json:"non_inferiority_delta"`
	ByModel                 map[string]ModelSummary `json:"by_model"`
	ByBucket                map[string]int          `json:"by_bucket"`
	GeneratedAt             time.Time               `json:"generated_at"`
}

func (p Policy) Validate(catalog map[string]Model) error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.TenantID) == "" || strings.TrimSpace(p.Version) == "" {
		return errors.New("policy id, tenant_id, and version are required")
	}
	switch p.Mode {
	case ModeObserve, ModeShadow, ModeCanary, ModeActive:
	default:
		return fmt.Errorf("unsupported policy mode %q", p.Mode)
	}
	if _, ok := catalog[p.IncumbentModel]; !ok {
		return fmt.Errorf("incumbent model %q is not in the catalog", p.IncumbentModel)
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, p.CandidateModels...), p.FallbackModels...) {
		if _, ok := catalog[id]; !ok {
			return fmt.Errorf("model %q is not in the catalog", id)
		}
		if seen[id] {
			return fmt.Errorf("model %q is repeated", id)
		}
		seen[id] = true
	}
	if p.QualityTolerance < 0 || p.QualityTolerance > 1 {
		return errors.New("quality_tolerance must be between 0 and 1")
	}
	for name, value := range map[string]float64{
		"control_percent": p.ControlPercent, "canary_percent": p.CanaryPercent, "exploration_percent": p.ExplorationPercent,
	} {
		if value < 0 || value > 100 {
			return fmt.Errorf("%s must be between 0 and 100", name)
		}
	}
	if p.ExplorationPercent > 3 {
		return errors.New("exploration_percent cannot exceed the 3% safety limit")
	}
	if p.ConfidenceZ <= 0 {
		return errors.New("confidence_z must be positive")
	}
	return nil
}
