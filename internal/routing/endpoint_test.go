package routing

import (
	"context"
	"strings"
	"testing"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

func endpointIndex() *StaticCatalogue {
	return NewStaticCatalogue(
		[]domain.Model{
			testModel(openaiID, "openai", "fast", domain.CapChat),
			testModel(anthropicID, "anthropic", "fast", domain.CapChat),
			testModel(anthropicID, "anthropic", "strong", domain.CapChat),
		},
		[]domain.Provider{
			testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
			testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2),
		},
	)
}

func endpointPolicy() *domain.RoutingPolicy {
	return &domain.RoutingPolicy{
		ID: "p1", Name: "base", Enabled: true, Priority: 100,
		Strategy: domain.StrategyPriority,
		Targets: []domain.RouteTarget{
			{ProviderName: "openai", Model: "fast"},
			{ProviderName: "anthropic", Model: "strong"},
		},
		Fallback: domain.FallbackPolicy{Enabled: true, MaxAttempts: 2},
		Limits:   domain.PolicyLimits{MaxCostPerRequestUSD: 1},
	}
}

func TestApplyEndpointOverrideNilIsIdentity(t *testing.T) {
	policy := endpointPolicy()
	rc := testRequest("fast")
	out, err := applyEndpointOverride(rc, endpointIndex(), policy)
	if err != nil {
		t.Fatalf("nil override must not fail: %v", err)
	}
	if out != policy {
		t.Error("nil override must return the policy untouched")
	}
}

func TestApplyEndpointOverrideForceModel(t *testing.T) {
	rc := testRequest("anything")
	rc.EndpointID = "mobile"
	rc.EndpointOverride = &domain.EndpointRoutingOverride{ForceModel: "fast"}
	out, err := applyEndpointOverride(rc, endpointIndex(), endpointPolicy())
	if err != nil {
		t.Fatalf("force model: %v", err)
	}
	if len(out.Targets) != 2 {
		t.Fatalf("targets = %+v, want both providers serving fast", out.Targets)
	}
	for _, target := range out.Targets {
		if target.Model != "fast" {
			t.Errorf("target model = %q, want fast", target.Model)
		}
	}
	if out.Targets[0].ProviderName != "openai" {
		t.Errorf("priority ordering lost: %+v", out.Targets)
	}
}

func TestApplyEndpointOverrideForceUnknownModel(t *testing.T) {
	rc := testRequest("anything")
	rc.EndpointID = "mobile"
	rc.EndpointOverride = &domain.EndpointRoutingOverride{ForceModel: "nope"}
	_, err := applyEndpointOverride(rc, endpointIndex(), endpointPolicy())
	if domain.AsError(err).Code != domain.ErrCodeNotFound {
		t.Fatalf("code = %v, want not_found", domain.AsError(err))
	}
}

func TestApplyEndpointOverridePreferredFilters(t *testing.T) {
	rc := testRequest("anything")
	rc.EndpointID = "mobile"
	rc.EndpointOverride = &domain.EndpointRoutingOverride{PreferredProviders: []string{"anthropic"}}
	out, err := applyEndpointOverride(rc, endpointIndex(), endpointPolicy())
	if err != nil {
		t.Fatalf("preferred filter: %v", err)
	}
	if len(out.Targets) != 1 || out.Targets[0].ProviderName != "anthropic" {
		t.Fatalf("targets = %+v", out.Targets)
	}

	rc.EndpointOverride = &domain.EndpointRoutingOverride{PreferredProviders: []string{"nobody"}}
	_, err = applyEndpointOverride(rc, endpointIndex(), endpointPolicy())
	if domain.AsError(err).Code != domain.ErrCodeUnavailable {
		t.Fatalf("code = %v, want provider_unavailable", domain.AsError(err))
	}
}

func TestApplyEndpointOverrideFallsBackWhenPolicyTargetsDoNotOverlap(t *testing.T) {
	// A policy that names only hosted providers, and a scope that pins traffic
	// to a provider the policy never mentions. Without the synthesized fallback
	// this can only ever fail: a scope meant to *introduce* a provider must not
	// be limited to subtracting from the policy's list.
	policy := &domain.RoutingPolicy{
		ID: "hosted", Name: "hosted", Enabled: true,
		Strategy: domain.StrategyPriority,
		Targets: []domain.RouteTarget{
			{ProviderID: openaiID, Model: "fast"},
			{ProviderID: anthropicID, Model: "strong"},
		},
	}
	index := NewStaticCatalogue(
		[]domain.Model{
			testModel(anthropicID, "anthropic", "claude", domain.CapChat),
			testModel(localID, "byo", "claude", domain.CapChat),
		},
		[]domain.Provider{
			testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 1),
			testProvider(localID, "byo", domain.ProviderOpenAICompatible, 2),
		},
	)

	rc := testRequest("claude")
	rc.EndpointID = "chat"
	rc.EndpointOverride = &domain.EndpointRoutingOverride{PreferredProviders: []string{"byo"}}
	out, err := applyEndpointOverride(rc, index, policy)
	if err != nil {
		t.Fatalf("scope must be able to add a provider: %v", err)
	}
	if len(out.Targets) != 1 || out.Targets[0].ProviderName != "byo" {
		t.Fatalf("targets = %+v, want only the scoped provider", out.Targets)
	}
	if out.Targets[0].Model != "claude" {
		t.Errorf("model = %q, want the requested model", out.Targets[0].Model)
	}
}

func TestApplyEndpointOverrideErrorNamesModelAndAllowList(t *testing.T) {
	policy := &domain.RoutingPolicy{
		ID: "hosted", Name: "hosted", Enabled: true,
		Strategy: domain.StrategyPriority,
		Targets:  []domain.RouteTarget{{ProviderID: openaiID, Model: "gpt"}},
	}
	rc := testRequest("gpt")
	rc.EndpointID = "chat"
	rc.EndpointOverride = &domain.EndpointRoutingOverride{PreferredProviders: []string{"bynara"}}

	_, err := applyEndpointOverride(rc, endpointIndex(), policy)
	normalized := domain.AsError(err)
	if normalized == nil || normalized.Code != domain.ErrCodeUnavailable {
		t.Fatalf("expected provider_unavailable, got %v", err)
	}
	for _, want := range []string{"chat", "gpt", "bynara"} {
		if !strings.Contains(normalized.Message, want) {
			t.Errorf("message %q should mention %q", normalized.Message, want)
		}
	}
}

func TestApplyEndpointOverrideBudgetsAndFlags(t *testing.T) {
	rc := testRequest("anything")
	rc.EndpointID = "mobile"
	rc.EndpointOverride = &domain.EndpointRoutingOverride{
		Strategy:        domain.StrategyLowestCost,
		MaxCostUSD:      0.05,
		LatencyTargetMS: 500,
		BlockFallback:   true,
	}
	policy := endpointPolicy()
	out, err := applyEndpointOverride(rc, endpointIndex(), policy)
	if err != nil {
		t.Fatalf("budgets: %v", err)
	}
	if out.Strategy != domain.StrategyLowestCost {
		t.Errorf("strategy = %q", out.Strategy)
	}
	if out.Limits.MaxCostPerRequestUSD != 0.05 {
		t.Errorf("cost ceiling = %v", out.Limits.MaxCostPerRequestUSD)
	}
	if rc.LatencyTargetMS != 500 {
		t.Errorf("latency target = %d", rc.LatencyTargetMS)
	}
	if out.Fallback.Enabled {
		t.Error("fallback must be blocked for the scope")
	}
	if policy.Strategy != domain.StrategyPriority || policy.Fallback.Enabled != true {
		t.Error("the stored policy must never be mutated by a scope")
	}
	if policy.Limits.MaxCostPerRequestUSD != 1 {
		t.Error("the stored policy budget must never be mutated by a scope")
	}
}

func TestEngineResolveHonoursEndpointScope(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o", domain.CapChat),
		testModel(anthropicID, "anthropic", "claude-3-5-sonnet", domain.CapChat),
	}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2),
	}
	policy := &domain.RoutingPolicy{
		ID: "base", Name: "base", Enabled: true,
		Strategy: domain.StrategyPriority,
		Targets:  []domain.RouteTarget{{ProviderID: openaiID, Model: "gpt-4o"}},
		Fallback: domain.FallbackPolicy{Enabled: true, MaxAttempts: 2},
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	rc := testRequest("gpt-4o")
	rc.EndpointID = "mobile"
	rc.EndpointOverride = &domain.EndpointRoutingOverride{ForceModel: "claude-3-5-sonnet"}
	decision, err := engine.Resolve(context.Background(), rc)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision.Chosen.ProviderID != anthropicID {
		t.Errorf("scope must pin the forced model, chose %+v", decision.Chosen)
	}
	if !strings.Contains(decision.Reason, "endpoint scope mobile") {
		t.Errorf("reason must cite the scope: %q", decision.Reason)
	}
	if decision.PolicyName != "base" {
		t.Errorf("the stored policy stays attributed: %q", decision.PolicyName)
	}
}
