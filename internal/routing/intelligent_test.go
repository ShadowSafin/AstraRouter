package routing

import (
	"context"
	"testing"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
)

// TestIntelligentRoutingTaskAware verifies score/task hooks influence order
// without breaking the base engine contract.
func TestIntelligentRoutingTaskAware(t *testing.T) {
	cat := NewStaticCatalogue(
		[]domain.Model{
			{ID: "m1", ProviderID: "p1", ProviderName: "cheap", Name: "gpt-4o-mini", Status: domain.ModelActive, Capabilities: []domain.Capability{domain.CapChat}, InputCostPerMillion: 0.15, OutputCostPerMillion: 0.6},
			{ID: "m2", ProviderID: "p2", ProviderName: "pricey", Name: "gpt-4o", Status: domain.ModelActive, Capabilities: []domain.Capability{domain.CapChat}, InputCostPerMillion: 2.5, OutputCostPerMillion: 10},
		},
		[]domain.Provider{
			{ID: "p1", Name: "cheap", Kind: domain.ProviderOpenAI, Status: domain.StatusActive},
			{ID: "p2", Name: "pricey", Kind: domain.ProviderOpenAI, Status: domain.StatusActive},
		},
	)
	policies := &intelStubResolver{policy: &domain.RoutingPolicy{
		ID: "pol", Name: "test", Enabled: true, Strategy: domain.StrategyPriority,
		Targets: []domain.RouteTarget{
			{ProviderName: "cheap", Model: "gpt-4o-mini"},
			{ProviderName: "pricey", Model: "gpt-4o"},
		},
		Fallback: domain.DefaultFallbackPolicy(),
		Retry:    domain.DefaultRetryPolicy(),
		Timeout:  domain.DefaultTimeoutPolicy(),
		Limits:   domain.PolicyLimits{MaxOutputTokens: 100},
	}}
	health := NewHealthTracker(HealthConfig{})
	engine := NewEngine(cat, policies, health,
		WithIntelligent(IntelligentOptions{Scores: stubScores{}, Guardrails: nil}))

	rc := &domain.RequestContext{
		RequestID:       "req-1",
		RequestedModel:  "gpt-4o-mini",
		RequestType:     domain.RequestTypeChatCompletion,
		PromptTokens:    100,
		MaxOutputTokens: 100,
		ReceivedAt:      time.Now(),
		Task:            domain.TaskClassification{Task: domain.TaskBatch},
	}
	dec, err := engine.Resolve(context.Background(), rc)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if dec.Chosen.Model == "" {
		t.Fatalf("no chosen model")
	}
	if dec.TimeoutStrategy == "" || dec.RetryStrategy == "" {
		t.Fatalf("expected derived strategies, got %+v", dec)
	}
	if len(dec.ScoreNotes) == 0 {
		t.Fatalf("expected score notes")
	}
}

type intelStubResolver struct {
	policy *domain.RoutingPolicy
}

func (s *intelStubResolver) Resolve(_ context.Context, _ *domain.RequestContext) (*domain.RoutingPolicy, error) {
	return s.policy, nil
}
func (s *intelStubResolver) List(_ context.Context) ([]domain.RoutingPolicy, error) {
	return []domain.RoutingPolicy{*s.policy}, nil
}
func (s *intelStubResolver) Get(_ context.Context, _ string) (*domain.RoutingPolicy, error) {
	return s.policy, nil
}

type stubScores struct{}

func (stubScores) Score(provider string) (float64, bool) {
	if provider == "cheap" {
		return 0.9, true
	}
	return 0.5, true
}
