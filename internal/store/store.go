package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/outcome-router/outcome-router/internal/domain"
)

type Repository interface {
	SaveDecision(decision domain.RouteDecision) error
	SaveOutcome(outcome domain.Outcome) error
	GetDecision(tenantID, decisionID string) (domain.RouteDecision, bool)
	Summary(tenantID, policyID, primaryOutcome string, since time.Time) domain.AuditSummary
	SavePolicy(policy domain.Policy) error
	LoadPolicies() ([]domain.Policy, error)
}

type FileStore struct {
	mu              sync.RWMutex
	directory       string
	decisions       map[string]domain.RouteDecision
	outcomes        []domain.Outcome
	policies        map[string]domain.Policy
	decisionLogPath string
	outcomeLogPath  string
	policyLogPath   string
}

func NewFileStore(directory string) (*FileStore, error) {
	if directory == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	result := &FileStore{
		directory:       directory,
		decisions:       map[string]domain.RouteDecision{},
		outcomes:        []domain.Outcome{},
		policies:        map[string]domain.Policy{},
		decisionLogPath: filepath.Join(directory, "decisions.jsonl"),
		outcomeLogPath:  filepath.Join(directory, "outcomes.jsonl"),
		policyLogPath:   filepath.Join(directory, "policies.jsonl"),
	}
	if err := loadJSONLines(result.decisionLogPath, func(line []byte) error {
		var decision domain.RouteDecision
		if err := json.Unmarshal(line, &decision); err != nil {
			return err
		}
		result.decisions[decision.ID] = decision
		return nil
	}); err != nil {
		return nil, fmt.Errorf("load decisions: %w", err)
	}
	if err := loadJSONLines(result.outcomeLogPath, func(line []byte) error {
		var outcome domain.Outcome
		if err := json.Unmarshal(line, &outcome); err != nil {
			return err
		}
		result.outcomes = append(result.outcomes, outcome)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("load outcomes: %w", err)
	}
	if err := loadJSONLines(result.policyLogPath, func(line []byte) error {
		var policy domain.Policy
		if err := json.Unmarshal(line, &policy); err != nil {
			return err
		}
		result.policies[policy.ID] = policy
		return nil
	}); err != nil {
		return nil, fmt.Errorf("load policies: %w", err)
	}
	return result, nil
}

func (s *FileStore) SaveDecision(decision domain.RouteDecision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := appendJSONLine(s.decisionLogPath, decision); err != nil {
		return err
	}
	s.decisions[decision.ID] = decision
	return nil
}

func (s *FileStore) SaveOutcome(outcome domain.Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := appendJSONLine(s.outcomeLogPath, outcome); err != nil {
		return err
	}
	s.outcomes = append(s.outcomes, outcome)
	return nil
}

func (s *FileStore) GetDecision(tenantID, decisionID string) (domain.RouteDecision, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	decision, ok := s.decisions[decisionID]
	return decision, ok && decision.TenantID == tenantID
}

func (s *FileStore) SavePolicy(policy domain.Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := appendJSONLine(s.policyLogPath, policy); err != nil {
		return err
	}
	s.policies[policy.ID] = policy
	return nil
}

func (s *FileStore) LoadPolicies() ([]domain.Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Policy, 0, len(s.policies))
	for _, policy := range s.policies {
		result = append(result, policy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (s *FileStore) Summary(tenantID, policyID, primaryOutcome string, since time.Time) domain.AuditSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	decisions := make([]domain.RouteDecision, 0, len(s.decisions))
	for _, decision := range s.decisions {
		decisions = append(decisions, decision)
	}
	outcomes := append([]domain.Outcome(nil), s.outcomes...)
	return summarize(decisions, outcomes, tenantID, policyID, primaryOutcome, since)
}

func summarize(
	allDecisions []domain.RouteDecision,
	outcomes []domain.Outcome,
	tenantID, policyID, primaryOutcome string,
	since time.Time,
) domain.AuditSummary {
	summary := domain.AuditSummary{
		TenantID:       tenantID,
		PolicyID:       policyID,
		Since:          since.UTC(),
		PrimaryOutcome: primaryOutcome,
		ByModel:        map[string]domain.ModelSummary{},
		ByBucket:       map[string]int{},
		GeneratedAt:    time.Now().UTC(),
	}

	decisions := make(map[string]domain.RouteDecision)
	traceBuckets := map[string]string{}
	for _, decision := range allDecisions {
		if decision.TenantID != tenantID || decision.CreatedAt.Before(since) {
			continue
		}
		if policyID != "" && decision.PolicyID != policyID {
			continue
		}
		decisions[decision.ID] = decision
		summary.ByBucket[decision.Bucket]++
		if decision.Bucket == "shadow" {
			summary.ShadowRequests++
			continue
		}
		summary.Requests++
		if decision.Bucket == "control" {
			summary.ControlRequests++
		}
		if decision.SelectedModel != decision.IncumbentModel {
			summary.RoutedRequests++
		}
		cost := decision.ActualCostUSD
		if cost <= 0 {
			cost = decision.EstimatedCostUSD
		}
		summary.ActualCostUSD += cost
		summary.EstimatedCostUSD += decision.EstimatedCostUSD
		summary.IncumbentCostUSD += decision.IncumbentEstimatedCostUSD
		modelID := decision.FinalModel
		if modelID == "" {
			modelID = decision.SelectedModel
		}
		model := summary.ByModel[modelID]
		model.Requests++
		model.CostUSD += cost
		summary.ByModel[modelID] = model
		if decision.Bucket == "treatment" || decision.Bucket == "exploration" {
			traceBuckets[decision.TraceID] = "treatment"
		} else if _, exists := traceBuckets[decision.TraceID]; !exists {
			traceBuckets[decision.TraceID] = "control"
		}
	}

	type outcomePoint struct {
		value float64
		trace string
	}
	primaryByTrace := map[string]outcomePoint{}
	for _, outcome := range outcomes {
		if outcome.TenantID != tenantID || outcome.CreatedAt.Before(since) || outcome.Type != primaryOutcome {
			continue
		}
		value, ok := outcome.NumericValue()
		if !ok {
			continue
		}
		traceID := outcome.TraceID
		if outcome.DecisionID != "" {
			if decision, exists := decisions[outcome.DecisionID]; exists {
				if traceID == "" {
					traceID = decision.TraceID
				}
				modelID := decision.FinalModel
				if modelID == "" {
					modelID = decision.SelectedModel
				}
				model := summary.ByModel[modelID]
				model.Successes += value
				model.Outcomes++
				summary.ByModel[modelID] = model
			}
		}
		if traceID != "" {
			primaryByTrace[traceID] = outcomePoint{value: value, trace: traceID}
		}
	}
	var treatmentSuccess, controlSuccess float64
	var treatmentN, controlN int
	for traceID, point := range primaryByTrace {
		switch traceBuckets[traceID] {
		case "treatment":
			treatmentSuccess += point.value
			treatmentN++
		case "control":
			controlSuccess += point.value
			controlN++
		}
	}
	summary.TreatmentOutcome = wilson(treatmentSuccess, treatmentN, 1.96)
	summary.ControlOutcome = wilson(controlSuccess, controlN, 1.96)
	summary.NonInferiorityDelta = summary.TreatmentOutcome.Estimate - summary.ControlOutcome.Estimate
	summary.VerifiedGrossSavingsUSD = math.Max(0, summary.IncumbentCostUSD-summary.ActualCostUSD)
	if summary.IncumbentCostUSD > 0 {
		summary.SavingsPercent = summary.VerifiedGrossSavingsUSD / summary.IncumbentCostUSD * 100
	}
	for modelID, model := range summary.ByModel {
		if model.Outcomes > 0 {
			model.SuccessRate = model.Successes / float64(model.Outcomes)
			summary.ByModel[modelID] = model
		}
	}
	return summary
}

func wilson(successes float64, n int, z float64) domain.Interval {
	if n == 0 {
		return domain.Interval{}
	}
	proportion := successes / float64(n)
	denominator := 1 + z*z/float64(n)
	center := (proportion + z*z/(2*float64(n))) / denominator
	margin := z * math.Sqrt((proportion*(1-proportion)+z*z/(4*float64(n)))/float64(n)) / denominator
	return domain.Interval{
		Estimate: proportion,
		Lower:    math.Max(0, center-margin),
		Upper:    math.Min(1, center+margin),
		N:        n,
	}
}

func appendJSONLine(path string, value any) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func loadJSONLines(path string, consume func([]byte) error) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32<<20)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := consume(line); err != nil {
			return fmt.Errorf("line %d: %w", lineNumber, err)
		}
	}
	return scanner.Err()
}
