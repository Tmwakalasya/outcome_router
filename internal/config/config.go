package config

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/outcome-router/outcome-router/internal/domain"
)

type Provider struct {
	ID              string            `json:"id"`
	BaseURL         string            `json:"base_url"`
	BaseURLEnv      string            `json:"base_url_env,omitempty"`
	APIKeyEnv       string            `json:"api_key_env,omitempty"`
	APIKey          string            `json:"-"`
	Headers         map[string]string `json:"headers,omitempty"`
	TimeoutMS       int               `json:"timeout_ms"`
	MaxRetries      int               `json:"max_retries"`
	CircuitFailures int               `json:"circuit_failures"`
	CircuitOpenMS   int               `json:"circuit_open_ms"`
}

type Tenant struct {
	ID              string              `json:"id"`
	RouterAPIKeyEnv string              `json:"router_api_key_env,omitempty"`
	RouterAPIKey    string              `json:"router_api_key,omitempty"`
	DefaultPolicyID string              `json:"default_policy_id"`
	Providers       map[string]Provider `json:"providers"`
}

type Config struct {
	ListenAddress string                  `json:"listen_address"`
	DataDirectory string                  `json:"data_directory"`
	AdminKeyEnv   string                  `json:"admin_key_env,omitempty"`
	AdminKey      string                  `json:"admin_key,omitempty"`
	Catalog       map[string]domain.Model `json:"catalog"`
	Policies      []domain.Policy         `json:"policies"`
	Tenants       []Tenant                `json:"tenants"`
}

func Load(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":8080"
	}
	if cfg.DataDirectory == "" {
		cfg.DataDirectory = "./data"
	}
	if cfg.AdminKeyEnv != "" {
		cfg.AdminKey = os.Getenv(cfg.AdminKeyEnv)
	}
	for tenantIndex := range cfg.Tenants {
		tenant := &cfg.Tenants[tenantIndex]
		if tenant.RouterAPIKeyEnv != "" {
			tenant.RouterAPIKey = os.Getenv(tenant.RouterAPIKeyEnv)
		}
		for id, provider := range tenant.Providers {
			if provider.ID == "" {
				provider.ID = id
			}
			if provider.APIKeyEnv != "" {
				provider.APIKey = os.Getenv(provider.APIKeyEnv)
			}
			if provider.BaseURLEnv != "" && os.Getenv(provider.BaseURLEnv) != "" {
				provider.BaseURL = os.Getenv(provider.BaseURLEnv)
			}
			if provider.TimeoutMS == 0 {
				provider.TimeoutMS = 120000
			}
			if provider.CircuitFailures == 0 {
				provider.CircuitFailures = 3
			}
			if provider.CircuitOpenMS == 0 {
				provider.CircuitOpenMS = 30000
			}
			tenant.Providers[id] = provider
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if len(c.Tenants) == 0 {
		return errors.New("at least one tenant is required")
	}
	if len(c.Catalog) == 0 {
		return errors.New("model catalog cannot be empty")
	}
	tenantIDs := map[string]bool{}
	for _, tenant := range c.Tenants {
		if tenant.ID == "" || tenant.RouterAPIKey == "" {
			return fmt.Errorf("tenant id and resolved router API key are required")
		}
		if tenantIDs[tenant.ID] {
			return fmt.Errorf("duplicate tenant %q", tenant.ID)
		}
		tenantIDs[tenant.ID] = true
		for id, provider := range tenant.Providers {
			if strings.TrimSpace(provider.BaseURL) == "" {
				return fmt.Errorf("tenant %s provider %s has no base_url", tenant.ID, id)
			}
		}
	}
	for id, model := range c.Catalog {
		if model.ID != id {
			return fmt.Errorf("catalog key %q does not match model id %q", id, model.ID)
		}
	}
	for _, policy := range c.Policies {
		if !tenantIDs[policy.TenantID] {
			return fmt.Errorf("policy %s refers to unknown tenant %s", policy.ID, policy.TenantID)
		}
		if err := policy.Validate(c.Catalog); err != nil {
			return fmt.Errorf("policy %s: %w", policy.ID, err)
		}
	}
	return nil
}

func (c *Config) Authenticate(rawAuthorization string) (*Tenant, bool) {
	token := strings.TrimSpace(strings.TrimPrefix(rawAuthorization, "Bearer "))
	for index := range c.Tenants {
		tenant := &c.Tenants[index]
		if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(tenant.RouterAPIKey)) == 1 {
			return tenant, true
		}
	}
	return nil, false
}

func (c *Config) AuthenticateAdmin(rawAuthorization string) bool {
	if c.AdminKey == "" {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(rawAuthorization, "Bearer "))
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(c.AdminKey)) == 1
}
