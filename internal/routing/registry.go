package routing

import (
	"errors"
	"sort"
	"sync"

	"github.com/outcome-router/outcome-router/internal/domain"
)

type PolicySaver interface {
	SavePolicy(policy domain.Policy) error
}

type Registry struct {
	mu       sync.RWMutex
	policies map[string]domain.Policy
	catalog  map[string]domain.Model
	saver    PolicySaver
}

func NewRegistry(catalog map[string]domain.Model, initial []domain.Policy, saver PolicySaver) *Registry {
	registry := &Registry{
		policies: make(map[string]domain.Policy, len(initial)),
		catalog:  catalog,
		saver:    saver,
	}
	for _, policy := range initial {
		registry.policies[policy.ID] = policy
	}
	return registry
}

func (r *Registry) Get(id, tenantID string) (domain.Policy, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	policy, ok := r.policies[id]
	return policy, ok && policy.TenantID == tenantID
}

func (r *Registry) List(tenantID string) []domain.Policy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]domain.Policy, 0)
	for _, policy := range r.policies {
		if tenantID == "" || policy.TenantID == tenantID {
			result = append(result, policy)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID == result[j].ID {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func (r *Registry) Put(policy domain.Policy) error {
	if err := policy.Validate(r.catalog); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.policies[policy.ID]; ok && current.TenantID != policy.TenantID {
		return errors.New("cannot move a policy between tenants")
	}
	if r.saver != nil {
		if err := r.saver.SavePolicy(policy); err != nil {
			return err
		}
	}
	r.policies[policy.ID] = policy
	return nil
}
