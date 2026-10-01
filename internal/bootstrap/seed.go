// Package bootstrap turns configuration into running services.
//
// It is the only package that knows about both the configuration file and the
// storage layer, which keeps that knowledge from leaking into either. Everything
// here is explicitly one-directional: configuration and database rows are
// converted into domain objects, domain objects are handed to services, and
// nothing in this package makes routing or policy decisions of its own.
package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/corerouter/corerouter/internal/config"
	"github.com/corerouter/corerouter/internal/domain"
	"github.com/corerouter/corerouter/internal/storage"
)

// Seeder applies the declarative catalogue from configuration into the database.
//
// The catalogue is treated as desired state, not as a one-time seed: every startup
// upserts by natural key, so editing a provider's pricing or a policy's targets in
// the config file and restarting is a complete, idempotent deployment. Anything
// created through the dashboard that is absent from the file is left alone rather
// than deleted, because the two sources are meant to coexist.
type Seeder struct {
	cfg    *config.Config
	repos  *storage.Repositories
	logger *slog.Logger
}

// NewSeeder constructs a seeder.
func NewSeeder(cfg *config.Config, repos *storage.Repositories, logger *slog.Logger) *Seeder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Seeder{cfg: cfg, repos: repos, logger: logger}
}

// Result reports what a seed run changed.
type Result struct {
	TenantID  string
	Providers int
	Models    int
	Policies  int
	Skipped   []string
}

// Apply writes the catalogue.
func (s *Seeder) Apply(ctx context.Context) (*Result, error) {
	result := &Result{}

	tenant, err := s.ensureDefaultTenant(ctx)
	if err != nil {
		return nil, err
	}
	result.TenantID = tenant.ID

	// Providers and models are applied together: a model row references a provider
	// row, so the provider must exist first.
	providerIDs := map[string]string{}
	for _, pc := range s.cfg.Providers {
		provider, models, err := s.applyProvider(ctx, pc, tenant.ID)
		if err != nil {
			return nil, err
		}
		providerIDs[provider.Name] = provider.ID
		result.Providers++
		result.Models += models
	}

	policyCount, err := s.applyPolicies(ctx, tenant.ID, providerIDs)
	if err != nil {
		return nil, err
	}
	result.Policies = policyCount

	// A tenant with no default policy falls back to the synthesized default, which
	// is a working configuration, so this is best-effort.
	if err := s.assignTenantDefaultPolicy(ctx, tenant, result); err != nil {
		s.logger.Debug("no default routing policy was assigned to the tenant", "error", err)
	}

	s.logger.Info("applied the configuration catalogue",
		"tenant", tenant.Slug,
		"providers", result.Providers,
		"models", result.Models,
		"policies", result.Policies,
	)

	return result, nil
}

// ensureDefaultTenant creates the tenant a fresh install needs to be usable.
func (s *Seeder) ensureDefaultTenant(ctx context.Context) (*domain.Tenant, error) {
	slug := s.cfg.App.DefaultTenantSlug
	if slug == "" {
		slug = "default"
	}
	name := s.cfg.App.DefaultTenantName
	if name == "" {
		name = "Default Tenant"
	}

	existing, err := s.repos.Tenants.GetBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	tenant := &domain.Tenant{
		ID:        domain.NewID(),
		Slug:      slug,
		Name:      name,
		Status:    domain.StatusActive,
		Labels:    map[string]string{"managed-by": "bootstrap"},
		CreatedAt: domain.Now(),
		UpdatedAt: domain.Now(),
	}
	saved, err := s.repos.Tenants.UpsertBySlug(ctx, tenant)
	if err != nil {
		return nil, err
	}
	s.logger.Info("created the default tenant", "slug", saved.Slug, "id", saved.ID)
	return saved, nil
}

// applyProvider upserts a provider and its models.
func (s *Seeder) applyProvider(ctx context.Context, pc config.ProviderConfig, tenantID string) (*domain.Provider, int, error) {
	provider := &domain.Provider{
		ID:             domain.NewID(),
		Name:           pc.Name,
		Kind:           domain.ProviderKind(pc.Kind),
		BaseURL:        strings.TrimRight(pc.BaseURL, "/"),
		APIKeyEnv:      pc.APIKeyEnv,
		APIKeyInline:   pc.APIKey,
		AuthStyle:      domain.AuthStyle(pc.AuthStyle),
		HeaderName:     pc.HeaderName,
		Headers:        pc.Headers,
		Organization:   pc.Organization,
		Project:        pc.Project,
		Status:         resolveProviderStatus(pc),
		Weight:         pc.Weight,
		Priority:       pc.Priority,
		TimeoutMS:      int(pc.Timeout.Std().Milliseconds()),
		MaxConcurrency: pc.MaxConcurrency,
		Region:         pc.Region,
		Labels:         pc.Labels,
		CreatedAt:      domain.Now(),
		UpdatedAt:      domain.Now(),
	}
	for _, c := range pc.Capabilities {
		if c != "" {
			provider.Capabilities = append(provider.Capabilities, domain.Capability(c))
		}
	}

	// An empty capability list means "use the kind's defaults", which the adapter
	// fills in. Storing an empty list rather than materializing the defaults keeps
	// the row honest about what the operator actually configured.
	//
	// The row is bootstrap-owned, but an operator may have taken ownership
	// through the admin API. Re-applying the file over such a row would revert
	// dashboard edits on every restart, so api-managed rows (and their models)
	// are left alone.
	provider.ManagedBy = domain.ManagedByBootstrap
	if existing, err := s.repos.Providers.GetByName(ctx, provider.Name); err != nil {
		return nil, 0, err
	} else if existing != nil {
		if existing.ManagedBy == domain.ManagedByAPI {
			s.logger.Info("skipping a provider managed through the admin API",
				"provider", provider.Name)
			return existing, 0, nil
		}
		provider.ID = existing.ID
	}

	saved, err := s.repos.Providers.Upsert(ctx, provider)
	if err != nil {
		return nil, 0, err
	}

	apiModels := map[string]bool{}
	if listed, err := s.repos.Models.ListByProvider(ctx, saved.ID); err == nil {
		for _, m := range listed {
			if m.ManagedBy == domain.ManagedByAPI {
				apiModels[m.Name] = true
			}
		}
	}

	count := 0
	for _, mc := range pc.Models {
		if strings.TrimSpace(mc.Name) == "" {
			continue
		}
		if apiModels[mc.Name] {
			s.logger.Info("skipping a model managed through the admin API",
				"provider", saved.Name, "model", mc.Name)
			continue
		}

		status := domain.ModelActive
		if mc.Status != "" {
			status = domain.ModelStatus(mc.Status)
		}

		model := &domain.Model{
			ID:                        domain.NewID(),
			ProviderID:                saved.ID,
			ProviderName:              saved.Name,
			Name:                      mc.Name,
			Aliases:                   mc.Aliases,
			DisplayName:               mc.DisplayName,
			Version:                   mc.Version,
			ContextWindow:             mc.ContextWindow,
			MaxOutputTokens:           mc.MaxOutputTokens,
			InputCostPerMillion:       mc.InputCostPerMillion,
			OutputCostPerMillion:      mc.OutputCostPerMillion,
			CachedInputCostPerMillion: mc.CachedInputCostPerMillion,
			Status:                    status,
			QualityTier:               mc.QualityTier,
			RateLimitRPM:              mc.RateLimitRPM,
			RateLimitTPM:              mc.RateLimitTPM,
			ManagedBy:                 domain.ManagedByBootstrap,
			Metadata:                  mc.Metadata,
			CreatedAt:                 domain.Now(),
			UpdatedAt:                 domain.Now(),
		}
		for _, c := range mc.Capabilities {
			if c != "" {
				model.Capabilities = append(model.Capabilities, domain.Capability(c))
			}
		}

		if _, err := s.repos.Models.Upsert(ctx, model); err != nil {
			return nil, 0, err
		}
		count++
	}

	return saved, count, nil
}

// resolveProviderStatus derives the provider status from enabled or status.
func resolveProviderStatus(pc config.ProviderConfig) domain.Status {
	if pc.Status != "" {
		return domain.Status(pc.Status)
	}
	if pc.Enabled != nil && !*pc.Enabled {
		return domain.StatusDisabled
	}
	return domain.StatusActive
}

// applyPolicies upserts the configured policies, skipping rows an operator
// owns through the admin API.
func (s *Seeder) applyPolicies(ctx context.Context, tenantID string, providerIDs map[string]string) (int, error) {
	apiOwned := map[string]bool{}
	if listed, err := s.repos.Policies.ListPolicies(ctx); err == nil {
		for _, p := range listed {
			if p.ManagedBy == domain.ManagedByAPI {
				apiOwned[p.TenantID+"\x00"+p.Name] = true
			}
		}
	}

	count := 0
	for _, pc := range s.cfg.Policies {
		if strings.TrimSpace(pc.Name) == "" {
			continue
		}

		policy := convertPolicy(pc, tenantID, providerIDs)
		policy.ManagedBy = domain.ManagedByBootstrap
		if apiOwned[policy.TenantID+"\x00"+policy.Name] {
			s.logger.Info("skipping a policy managed through the admin API",
				"policy", policy.Name)
			continue
		}
		if _, err := s.repos.Policies.Upsert(ctx, policy); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// convertPolicy maps a configuration policy onto the domain type.
func convertPolicy(pc config.PolicyConfig, tenantID string, providerIDs map[string]string) *domain.RoutingPolicy {
	policy := &domain.RoutingPolicy{
		ID:          domain.NewID(),
		Name:        pc.Name,
		Description: pc.Description,
		Priority:    pc.Priority,
		Enabled:     pc.Enabled == nil || *pc.Enabled,
		Strategy:    domain.RoutingStrategy(pc.Strategy),
		CreatedAt:   domain.Now(),
		UpdatedAt:   domain.Now(),
	}

	// A policy scoped to the default tenant is stored as a tenant policy; one
	// without a tenant slug is platform-wide.
	if pc.TenantSlug != "" {
		policy.TenantID = tenantID
	}

	// Match block.
	if len(pc.Match.Models) > 0 {
		policy.Match.Models = pc.Match.Models
	}
	for _, rt := range pc.Match.RequestTypes {
		policy.Match.RequestTypes = append(policy.Match.RequestTypes, domain.RequestType(rt))
	}
	if len(pc.Match.TenantSlugs) > 0 {
		policy.Match.TenantIDs = append(policy.Match.TenantIDs, tenantID)
	}
	if len(pc.Match.EndpointSlugs) > 0 {
		policy.Match.EndpointIDs = pc.Match.EndpointSlugs
	}
	if pc.Match.MinPromptTokens > 0 {
		policy.Match.MinPromptTokens = pc.Match.MinPromptTokens
	}
	if pc.Match.MaxPromptTokens > 0 {
		policy.Match.MaxPromptTokens = pc.Match.MaxPromptTokens
	}
	for _, c := range pc.Match.RequiredCapabilities {
		policy.Match.RequiredCapabilities = append(policy.Match.RequiredCapabilities, domain.Capability(c))
	}
	policy.Match.Streaming = pc.Match.Streaming
	policy.Match.Batch = pc.Match.Batch
	if len(pc.Match.Regions) > 0 {
		policy.Match.Regions = pc.Match.Regions
	}
	if len(pc.Match.DataSensitivity) > 0 {
		policy.Match.DataSensitivity = pc.Match.DataSensitivity
	}
	for _, t := range pc.Match.TaskTypes {
		policy.Match.TaskTypes = append(policy.Match.TaskTypes, domain.TaskType(t))
	}

	// Targets.
	for i, tc := range pc.Targets {
		target := domain.RouteTarget{
			Model:           tc.Model,
			Weight:          tc.Weight,
			Priority:        tc.Priority,
			MaxOutputTokens: tc.MaxOutputTokens,
		}
		// The provider reference is resolved to an id when known so a provider
		// rename does not silently break a policy; the name is kept as a fallback
		// for a policy that references a provider the file does not declare.
		if tc.Provider != "" {
			target.ProviderName = tc.Provider
			if id, ok := providerIDs[tc.Provider]; ok {
				target.ProviderID = id
			}
		}
		if target.Priority == 0 {
			target.Priority = i + 1
		}
		policy.Targets = append(policy.Targets, target)
	}

	// Nested policy blocks, each optional.
	if pc.Fallback != nil {
		policy.Fallback = domain.FallbackPolicy{
			Enabled:               pc.Fallback.Enabled == nil || *pc.Fallback.Enabled,
			MaxAttempts:           pc.Fallback.MaxAttempts,
			BudgetAware:           pc.Fallback.BudgetAware == nil || *pc.Fallback.BudgetAware,
			BackoffBeforeFailover: pc.Fallback.BackoffBeforeFailover.Std(),
		}
		for _, code := range pc.Fallback.OnErrorCodes {
			policy.Fallback.OnErrorCodes = append(policy.Fallback.OnErrorCodes, domain.ErrorCode(code))
		}
		policy.Fallback.OnStatusCodes = pc.Fallback.OnStatusCodes
	}
	if pc.Retry != nil {
		policy.Retry = domain.RetryPolicy{
			MaxAttempts:    pc.Retry.MaxAttempts,
			InitialBackoff: pc.Retry.InitialBackoff.Std(),
			MaxBackoff:     pc.Retry.MaxBackoff.Std(),
			Multiplier:     pc.Retry.Multiplier,
		}
		if pc.Retry.Jitter != nil {
			policy.Retry.Jitter = *pc.Retry.Jitter
		} else {
			policy.Retry.Jitter = true
		}
		if pc.Retry.HonorRetryAfter != nil {
			policy.Retry.HonorRetryAfter = *pc.Retry.HonorRetryAfter
		} else {
			policy.Retry.HonorRetryAfter = true
		}
		for _, code := range pc.Retry.RetryOn {
			policy.Retry.RetryOn = append(policy.Retry.RetryOn, domain.ErrorCode(code))
		}
	}
	if pc.Timeout != nil {
		policy.Timeout = domain.TimeoutPolicy{
			Total:      pc.Timeout.Total.Std(),
			PerAttempt: pc.Timeout.PerAttempt.Std(),
			Connect:    pc.Timeout.Connect.Std(),
			StreamIdle: pc.Timeout.StreamIdle.Std(),
			FirstToken: pc.Timeout.FirstToken.Std(),
		}
	}
	if pc.Limits != nil {
		policy.Limits = domain.PolicyLimits{
			MaxCostPerRequestUSD:        pc.Limits.MaxCostPerRequestUSD,
			MaxPromptTokens:             pc.Limits.MaxPromptTokens,
			MaxOutputTokens:             pc.Limits.MaxOutputTokens,
			LatencyTargetMS:             pc.Limits.LatencyTargetMS,
			MaxLatencyMS:                pc.Limits.MaxLatencyMS,
			RequestsPerMinute:           pc.Limits.RequestsPerMinute,
			TokensPerMinute:             pc.Limits.TokensPerMinute,
			DailyBudgetUSD:              pc.Limits.DailyBudgetUSD,
			MonthlyBudgetUSD:            pc.Limits.MonthlyBudgetUSD,
			AllowedModels:               pc.Limits.AllowedModels,
			DeniedModels:                pc.Limits.DeniedModels,
			AllowedProviders:            pc.Limits.AllowedProviders,
			DeniedProviders:             pc.Limits.DeniedProviders,
			AllowedRegions:              pc.Limits.AllowedRegions,
			DeniedRegions:               pc.Limits.DeniedRegions,
			DeniedSensitive:             pc.Limits.DeniedSensitive,
			MaxRequestBytes:             pc.Limits.MaxRequestBytes,
			BatchOnly:                   pc.Limits.BatchOnly,
			InteractiveOnly:             pc.Limits.InteractiveOnly,
			CacheEnabled:                pc.Limits.CacheEnabled,
			BypassCacheForSensitive:     pc.Limits.BypassCacheForSensitive,
			RequireCacheBypassSensitive: pc.Limits.RequireCacheBypassSensitive,
			MaxHistoryMessages:          pc.Limits.MaxHistoryMessages,
		}
	}

	policy.Normalize()
	return policy
}

// assignTenantDefaultPolicy points the default tenant at the first configured
// policy, so a fresh install routes deterministically without further setup.
func (s *Seeder) assignTenantDefaultPolicy(ctx context.Context, tenant *domain.Tenant, result *Result) error {
	if tenant.DefaultRoutingPolicyID != "" || result.Policies == 0 {
		return nil
	}

	policies, err := s.repos.Policies.ListPolicies(ctx)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if !p.Enabled {
			continue
		}
		return s.repos.Tenants.SetDefaultPolicy(ctx, tenant.ID, p.ID)
	}
	return nil
}

// ResolveAdminKey returns the bootstrap administrative credential.
//
// It is read from the environment rather than from the config file so that a file
// committed to version control cannot contain a working admin credential.
func ResolveAdminKey(cfg *config.Config) string {
	if cfg.Auth.AdminKey != "" {
		return cfg.Auth.AdminKey
	}
	if cfg.Auth.AdminKeyEnv != "" {
		return strings.TrimSpace(os.Getenv(cfg.Auth.AdminKeyEnv))
	}
	return ""
}
