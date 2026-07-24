package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/outcome-router/outcome-router/internal/domain"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) SaveDecision(decision domain.RouteDecision) error {
	data, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO routing_decisions (
			id, tenant_id, trace_id, policy_id, policy_version, selected_model,
			final_model, bucket, estimated_cost_usd, actual_cost_usd, created_at, data
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			final_model = EXCLUDED.final_model,
			bucket = EXCLUDED.bucket,
			estimated_cost_usd = EXCLUDED.estimated_cost_usd,
			actual_cost_usd = EXCLUDED.actual_cost_usd,
			data = EXCLUDED.data`,
		decision.ID, decision.TenantID, decision.TraceID, decision.PolicyID,
		decision.PolicyVersion, decision.SelectedModel, decision.FinalModel,
		decision.Bucket, decision.EstimatedCostUSD, decision.ActualCostUSD,
		decision.CreatedAt, data,
	)
	return err
}

func (s *PostgresStore) SaveOutcome(outcome domain.Outcome) error {
	data, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO routing_outcomes (
			id, tenant_id, decision_id, trace_id, outcome_type, occurred_at, created_at, data
		) VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8)
		ON CONFLICT (id) DO NOTHING`,
		outcome.ID, outcome.TenantID, outcome.DecisionID, outcome.TraceID,
		outcome.Type, outcome.OccurredAt, outcome.CreatedAt, data,
	)
	return err
}

func (s *PostgresStore) GetDecision(tenantID, decisionID string) (domain.RouteDecision, bool) {
	var data []byte
	err := s.db.QueryRow(
		`SELECT data FROM routing_decisions WHERE tenant_id=$1 AND id=$2`,
		tenantID, decisionID,
	).Scan(&data)
	if err != nil {
		return domain.RouteDecision{}, false
	}
	var decision domain.RouteDecision
	if json.Unmarshal(data, &decision) != nil {
		return domain.RouteDecision{}, false
	}
	return decision, true
}

func (s *PostgresStore) Summary(tenantID, policyID, primaryOutcome string, since time.Time) domain.AuditSummary {
	arguments := []any{tenantID, since}
	policyFilter := ""
	if policyID != "" {
		policyFilter = " AND policy_id=$3"
		arguments = append(arguments, policyID)
	}
	rows, err := s.db.Query(
		`SELECT data FROM routing_decisions WHERE tenant_id=$1 AND created_at >= $2`+policyFilter,
		arguments...,
	)
	if err != nil {
		return domain.AuditSummary{TenantID: tenantID, PolicyID: policyID, Since: since, GeneratedAt: time.Now().UTC()}
	}
	var decisions []domain.RouteDecision
	for rows.Next() {
		var data []byte
		var decision domain.RouteDecision
		if rows.Scan(&data) == nil && json.Unmarshal(data, &decision) == nil {
			decisions = append(decisions, decision)
		}
	}
	_ = rows.Close()

	outcomeRows, err := s.db.Query(
		`SELECT data FROM routing_outcomes WHERE tenant_id=$1 AND created_at >= $2 AND outcome_type=$3`,
		tenantID, since, primaryOutcome,
	)
	if err != nil {
		return summarize(decisions, nil, tenantID, policyID, primaryOutcome, since)
	}
	var outcomes []domain.Outcome
	for outcomeRows.Next() {
		var data []byte
		var outcome domain.Outcome
		if outcomeRows.Scan(&data) == nil && json.Unmarshal(data, &outcome) == nil {
			outcomes = append(outcomes, outcome)
		}
	}
	_ = outcomeRows.Close()
	return summarize(decisions, outcomes, tenantID, policyID, primaryOutcome, since)
}

func (s *PostgresStore) SavePolicy(policy domain.Policy) error {
	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO routing_policies (id, tenant_id, version, created_at, data)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET
			version=EXCLUDED.version, created_at=EXCLUDED.created_at, data=EXCLUDED.data`,
		policy.ID, policy.TenantID, policy.Version, policy.CreatedAt, data,
	)
	return err
}

func (s *PostgresStore) LoadPolicies() ([]domain.Policy, error) {
	rows, err := s.db.Query(`SELECT data FROM routing_policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Policy
	for rows.Next() {
		var data []byte
		var policy domain.Policy
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &policy); err != nil {
			return nil, err
		}
		result = append(result, policy)
	}
	return result, rows.Err()
}
