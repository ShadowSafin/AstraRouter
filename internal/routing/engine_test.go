package routing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shadowsafin/synapass/internal/domain"
	"github.com/shadowsafin/synapass/internal/providers"
)

// ---------------------------------------------------------------------------
// Fixtures
//
// Every test in this file runs without a database, a network or a clock. The
// engine and executor take their collaborators as interfaces and their time from
// injected functions precisely so their behaviour can be asserted directly
// rather than inferred from a running system.
// ---------------------------------------------------------------------------

const (
	openaiID    = "p-openai"
	anthropicID = "p-anthropic"
	localID     = "p-local"
)

// testProvider builds an active provider of the given kind.
func testProvider(id, name string, kind domain.ProviderKind, priority int) domain.Provider {
	return domain.Provider{
		ID:           id,
		Name:         name,
		Kind:         kind,
		BaseURL:      "https://example.invalid/v1",
		Status:       domain.StatusActive,
		Priority:     priority,
		Capabilities: providers.DefaultCapabilities(kind).Slice(),
	}
}

// testModel builds an active model on a provider with a generous window.
func testModel(providerID, providerName, name string, caps ...domain.Capability) domain.Model {
	return domain.Model{
		ID:              providerID + ":" + name,
		ProviderID:      providerID,
		ProviderName:    providerName,
		Name:            name,
		Status:          domain.ModelActive,
		ContextWindow:   128_000,
		MaxOutputTokens: 4096,
		Capabilities:    caps,
	}
}

// testRequest builds an unauthenticated chat request context.
func testRequest(model string) *domain.RequestContext {
	return &domain.RequestContext{
		RequestID:      domain.RequestID(domain.NewID()),
		ReceivedAt:     domain.Now(),
		RequestType:    domain.RequestTypeChatCompletion,
		RequestedModel: model,
		PromptTokens:   100,
	}
}

// stubResolver is a PolicyResolver that returns one fixed answer.
type stubResolver struct {
	policy *domain.RoutingPolicy
	err    error
	calls  int
}

func (s *stubResolver) Resolve(context.Context, *domain.RequestContext) (*domain.RoutingPolicy, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.policy, nil
}

// stubAvailability reports providers as unavailable by id.
type stubAvailability struct {
	unavailable map[string]bool
}

func (s stubAvailability) Available(providerID, _ string) bool { return !s.unavailable[providerID] }

// stubAdapter is a provider adapter whose outcome is scripted per call.
type stubAdapter struct {
	name    string
	kind    domain.ProviderKind
	enabled bool

	mu       sync.Mutex
	calls    int
	outcome  func(call int) error
	onStream func(handler providers.StreamHandler, req *providers.Request) error
}

func newStubAdapter(name string, kind domain.ProviderKind) *stubAdapter {
	return &stubAdapter{name: name, kind: kind, enabled: true}
}

func (s *stubAdapter) Name() string              { return s.name }
func (s *stubAdapter) Kind() domain.ProviderKind { return s.kind }
func (s *stubAdapter) Enabled() bool             { return s.enabled }
func (s *stubAdapter) Capabilities() domain.CapabilitySet {
	return providers.DefaultCapabilities(s.kind)
}

func (s *stubAdapter) HealthCheck(context.Context) domain.ProviderHealth {
	return domain.ProviderHealth{
		ProviderName: s.name,
		State:        domain.HealthHealthy,
		CheckedAt:    domain.Now(),
		Source:       "stub",
	}
}

func (s *stubAdapter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// next records a call and reports the scripted outcome. The call index is
// 1-based so a script reads as "the first call fails".
func (s *stubAdapter) next() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.outcome != nil {
		return s.outcome(s.calls)
	}
	return nil
}

func (s *stubAdapter) response(req *providers.Request) *providers.Response {
	return &providers.Response{
		ID:    "cmpl-" + s.name,
		Model: req.Model,
		Choices: []domain.Choice{{
			Index:   0,
			Message: &domain.ChatMessage{Role: domain.RoleAssistant, Content: domain.NewTextContent(s.name)},
		}},
		Usage: domain.TokenUsage{PromptTokens: 10, CompletionTokens: 5},
	}
}

func (s *stubAdapter) ChatCompletion(_ context.Context, req *providers.Request) (*providers.Response, error) {
	if err := s.next(); err != nil {
		return nil, err
	}
	return s.response(req), nil
}

func (s *stubAdapter) ChatCompletionStream(_ context.Context, req *providers.Request, handler providers.StreamHandler) (*providers.Response, error) {
	if err := s.next(); err != nil {
		return nil, err
	}
	if s.onStream != nil {
		if err := s.onStream(handler, req); err != nil {
			return nil, err
		}
		return s.response(req), nil
	}
	if handler != nil {
		chunk := providers.Chunk{
			ID:    "cmpl-" + s.name,
			Model: req.Model,
			Index: 0,
			Delta: domain.ChatMessage{Role: domain.RoleAssistant, Content: domain.NewTextContent("hi")},
		}
		if err := handler(chunk); err != nil {
			return nil, err
		}
	}
	return s.response(req), nil
}

// testClock is a frozen, manually advanced clock. Sleeping advances it, so
// backoff and deadline behaviour is exercised without real waiting.
type testClock struct {
	mu    sync.Mutex
	t     time.Time
	slept []time.Duration
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.slept = append(c.slept, d)
	c.mu.Unlock()
	return nil
}

func (c *testClock) totalSlept() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total time.Duration
	for _, d := range c.slept {
		total += d
	}
	return total
}

// newRegistry registers each provider with its matching adapter.
func newRegistry(pairs ...struct {
	provider domain.Provider
	adapter  *stubAdapter
}) *providers.Registry {
	registry := providers.NewRegistry()
	for _, pair := range pairs {
		registry.Register(pair.provider, pair.adapter)
	}
	return registry
}

type registration struct {
	provider domain.Provider
	adapter  *stubAdapter
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

func TestEngineSynthesizesTargetsFromRegistry(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o"),
		testModel(anthropicID, "anthropic", "gpt-4o"),
	}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2),
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), nil, nil)
	rc := testRequest("gpt-4o")

	decision, err := engine.Resolve(context.Background(), rc)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if decision.PolicyID != "" {
		t.Errorf("expected the synthesized default policy, got %q", decision.PolicyID)
	}
	if !strings.Contains(decision.Reason, "no stored policy matched") {
		t.Errorf("reason should explain the default policy, got %q", decision.Reason)
	}
	if decision.Chosen.ProviderID != openaiID {
		t.Errorf("priority 1 provider should win, got %q", decision.Chosen.ProviderID)
	}
	if decision.Chain.Length() != 2 {
		t.Errorf("default fallback permits two targets, got %d", decision.Chain.Length())
	}
	if rc.Resolution != decision {
		t.Error("resolution should be stored on the request context")
	}
}

func TestEngineHonoursPolicyTargetOrder(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o"),
		testModel(anthropicID, "anthropic", "claude-3-5-sonnet"),
	}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2),
	}

	policy := &domain.RoutingPolicy{
		ID: "p1", Name: "prefer-anthropic", Enabled: true,
		Strategy: domain.StrategyPriority,
		Targets: []domain.RouteTarget{
			{ProviderID: anthropicID, Model: "claude-3-5-sonnet", Priority: 1},
			{ProviderID: openaiID, Model: "gpt-4o", Priority: 2},
		},
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	decision, err := engine.Resolve(context.Background(), testRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if decision.PolicyName != "prefer-anthropic" {
		t.Errorf("policy name = %q", decision.PolicyName)
	}
	if decision.Chosen.ProviderID != anthropicID {
		t.Errorf("policy order should win over registry priority, got %q", decision.Chosen.ProviderID)
	}
	if decision.Degraded {
		t.Error("a request served by the first declared target is not degraded")
	}
}

func TestEngineRejectsUnknownModel(t *testing.T) {
	engine := NewEngine(NewStaticCatalogue(nil, nil), nil, nil)
	_, err := engine.Resolve(context.Background(), testRequest("does-not-exist"))

	normalized := domain.AsError(err)
	if normalized == nil || normalized.Code != domain.ErrCodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
	if !strings.Contains(normalized.Message, "does-not-exist") {
		t.Errorf("message should name the model, got %q", normalized.Message)
	}
}

func TestEngineRejectsMissingModel(t *testing.T) {
	engine := NewEngine(NewStaticCatalogue(nil, nil), nil, nil)
	_, err := engine.Resolve(context.Background(), testRequest("  "))

	if code := domain.AsError(err).Code; code != domain.ErrCodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %q", code)
	}
}

func TestEngineRejectsNilRequestContext(t *testing.T) {
	engine := NewEngine(NewStaticCatalogue(nil, nil), nil, nil)
	_, err := engine.Resolve(context.Background(), nil)

	if code := domain.AsError(err).Code; code != domain.ErrCodeInternal {
		t.Fatalf("expected internal_error, got %q", code)
	}
}

func TestEngineDenyListExcludesModel(t *testing.T) {
	models := []domain.Model{testModel(openaiID, "openai", "gpt-4o")}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	policy := &domain.RoutingPolicy{
		ID: "deny", Name: "deny", Enabled: true,
		Strategy: domain.StrategyPriority,
		Targets:  []domain.RouteTarget{{ProviderID: openaiID, Model: "gpt-4o"}},
		Limits:   domain.PolicyLimits{DeniedModels: []string{"gpt-4o"}},
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	_, err := engine.Resolve(context.Background(), testRequest("gpt-4o"))

	normalized := domain.AsError(err)
	if normalized.Code != domain.ErrCodeUnavailable {
		t.Fatalf("expected unavailable, got %q", normalized.Code)
	}
	if !strings.Contains(normalized.Message, "denied by policy") {
		t.Errorf("message should cite the deny list, got %q", normalized.Message)
	}
}

func TestEngineAllowListRequiresMatch(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o"),
		testModel(anthropicID, "anthropic", "claude-3-5-sonnet"),
	}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2),
	}

	// The allow list pins every request to Anthropic models, even though the
	// policy's first declared target is an OpenAI model.
	policy := &domain.RoutingPolicy{
		ID: "allow", Name: "allow", Enabled: true,
		Strategy: domain.StrategyPriority,
		Targets: []domain.RouteTarget{
			{ProviderID: openaiID, Model: "gpt-4o", Priority: 1},
			{ProviderID: anthropicID, Model: "claude-3-5-sonnet", Priority: 2},
		},
		Limits: domain.PolicyLimits{AllowedModels: []string{"claude-*"}},
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	decision, err := engine.Resolve(context.Background(), testRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision.Chosen.ProviderID != anthropicID {
		t.Errorf("only the allow-listed model may be chosen, got %q", decision.Chosen.ProviderID)
	}

	var denied bool
	for _, skipped := range decision.Chain.Skipped {
		if skipped.Target.ProviderID != openaiID {
			continue
		}
		denied = true
		if !strings.Contains(skipped.Reason, "allow list") {
			t.Errorf("skip reason = %q", skipped.Reason)
		}
	}
	if !denied {
		t.Error("the out-of-allow-list target must be reported as skipped")
	}
}

func TestEngineRelaxesHealthFilterAndMarksDegraded(t *testing.T) {
	models := []domain.Model{testModel(openaiID, "openai", "gpt-4o")}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	health := NewHealthTracker(HealthConfig{})
	for i := 0; i < domain.DegradeThreshold*3; i++ {
		health.RecordFailure(openaiID, "openai", domain.ErrCodeUpstream, 10*time.Millisecond)
	}
	if !health.CircuitOpen(openaiID) {
		t.Fatal("precondition: the circuit breaker should be open")
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), nil, health)
	decision, err := engine.Resolve(context.Background(), testRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("an unhealthy provider must still route after relaxation: %v", err)
	}

	if !decision.Degraded {
		t.Error("relaxing the health filter must mark the decision degraded")
	}
	if !strings.Contains(decision.Reason, "health filter relaxed") {
		t.Errorf("reason should record the relaxation, got %q", decision.Reason)
	}
	if decision.Chosen.ProviderID != openaiID {
		t.Errorf("chosen = %q", decision.Chosen.ProviderID)
	}
}

func TestEngineRelaxesCostCeilingAndReportsCeiling(t *testing.T) {
	model := testModel(openaiID, "openai", "gpt-4o")
	model.InputCostPerMillion = 1000
	model.OutputCostPerMillion = 1000
	models := []domain.Model{model}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	rc := testRequest("gpt-4o")
	rc.CostCeilingUSD = 0.000001

	engine := NewEngine(NewStaticCatalogue(models, providersList), nil, nil)
	decision, err := engine.Resolve(context.Background(), rc)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !decision.Degraded {
		t.Error("a relaxed cost ceiling must mark the decision degraded")
	}
	if !strings.Contains(decision.Reason, "cost ceiling relaxed") {
		t.Errorf("reason should record the cost relaxation, got %q", decision.Reason)
	}
	if decision.CostCeilingUSD != rc.CostCeilingUSD {
		t.Errorf("ceiling = %v, want %v", decision.CostCeilingUSD, rc.CostCeilingUSD)
	}
}

func TestEngineRejectsOverContextWindow(t *testing.T) {
	model := testModel(openaiID, "openai", "gpt-4o")
	model.ContextWindow = 200
	models := []domain.Model{model}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	rc := testRequest("gpt-4o")
	rc.PromptTokens = 5_000

	engine := NewEngine(NewStaticCatalogue(models, providersList), nil, nil)
	_, err := engine.Resolve(context.Background(), rc)

	normalized := domain.AsError(err)
	if normalized.Code != domain.ErrCodeUnavailable {
		t.Fatalf("expected unavailable, got %q", normalized.Code)
	}
	if !strings.Contains(normalized.Message, "exceeds the") {
		t.Errorf("message should explain the window overflow, got %q", normalized.Message)
	}
}

func TestEngineEnforcesCapabilityIntersection(t *testing.T) {
	plain := testModel(openaiID, "openai", "gpt-4o-mini", domain.CapChat, domain.CapStreaming)
	vision := testModel(openaiID, "openai", "gpt-4o", domain.CapChat, domain.CapStreaming, domain.CapVision)
	models := []domain.Model{plain, vision}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	policy := &domain.RoutingPolicy{
		ID: "vision", Name: "vision", Enabled: true,
		Strategy: domain.StrategyPriority,
		Targets: []domain.RouteTarget{
			{ProviderID: openaiID, Model: "gpt-4o-mini", Priority: 1},
			{ProviderID: openaiID, Model: "gpt-4o", Priority: 2},
		},
	}

	rc := testRequest("gpt-4o")
	rc.RequiredCapabilities = []domain.Capability{domain.CapVision}

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	decision, err := engine.Resolve(context.Background(), rc)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if decision.Chosen.Model != "gpt-4o" {
		t.Errorf("a model without the vision capability must not be chosen, got %q", decision.Chosen.Model)
	}
	if !decision.Degraded {
		t.Error("skipping the operator's first target is a degraded decision")
	}

	// Rejected targets are not dropped: they are retained on the decision so an
	// operator can answer "why not the model I listed first?" from the record.
	var rejected bool
	for _, skipped := range decision.Chain.Skipped {
		if skipped.Target.Model != "gpt-4o-mini" {
			continue
		}
		rejected = true
		if !strings.Contains(skipped.Reason, "vision") {
			t.Errorf("rejection should name the missing capability, got %q", skipped.Reason)
		}
	}
	if !rejected {
		t.Error("the rejected candidate must still be reported")
	}
}

func TestEngineTruncatesChainToFallbackLimit(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o"),
		testModel(anthropicID, "anthropic", "gpt-4o"),
		testModel(localID, "local", "gpt-4o"),
	}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2),
		testProvider(localID, "local", domain.ProviderVLLM, 3),
	}

	policy := &domain.RoutingPolicy{
		ID: "chain", Name: "chain", Enabled: true,
		Strategy: domain.StrategyPriority,
		Fallback: domain.FallbackPolicy{Enabled: true, MaxAttempts: 2},
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	decision, err := engine.Resolve(context.Background(), testRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if decision.Chain.Length() != 2 {
		t.Errorf("chain length = %d, want 2", decision.Chain.Length())
	}
	if len(decision.Chain.Attempts()) != 2 {
		t.Errorf("attempts = %d, want 2", len(decision.Chain.Attempts()))
	}
	if decision.Chain.Limit() != 2 {
		t.Errorf("limit = %d, want 2", decision.Chain.Limit())
	}
}

func TestEngineDisablesFallbackWhenPolicySaysSo(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o"),
		testModel(anthropicID, "anthropic", "gpt-4o"),
	}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2),
	}

	policy := &domain.RoutingPolicy{
		ID: "no-fallback", Name: "no-fallback", Enabled: true,
		Strategy: domain.StrategyPriority,
		Fallback: domain.FallbackPolicy{Enabled: false},
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	decision, err := engine.Resolve(context.Background(), testRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if decision.Chain.Length() != 1 {
		t.Errorf("a disabled fallback policy permits exactly one target, got %d", decision.Chain.Length())
	}
	if decision.Fallback.Enabled {
		t.Error("the decision must carry the fallback policy it was built from verbatim")
	}
}

func TestEngineLowestCostPicksCheapest(t *testing.T) {
	expensive := testModel(openaiID, "openai", "gpt-4o")
	expensive.InputCostPerMillion = 10
	expensive.OutputCostPerMillion = 30

	cheap := testModel(localID, "local", "gpt-4o")
	cheap.InputCostPerMillion = 0.1
	cheap.OutputCostPerMillion = 0.2

	models := []domain.Model{expensive, cheap}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(localID, "local", domain.ProviderVLLM, 2),
	}

	policy := &domain.RoutingPolicy{
		ID: "cheap", Name: "cheap", Enabled: true,
		Strategy: domain.StrategyLowestCost,
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	decision, err := engine.Resolve(context.Background(), testRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if decision.Chosen.ProviderID != localID {
		t.Errorf("lowest_cost should pick the local provider, got %q", decision.Chosen.ProviderID)
	}
	if decision.EstimatedCost.USD <= 0 {
		t.Error("the decision should carry an estimated cost")
	}
}

func TestEngineWeightedRoutingIsDeterministicAndSpread(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o"),
		testModel(anthropicID, "anthropic", "gpt-4o"),
	}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 1),
	}

	policy := &domain.RoutingPolicy{
		ID: "weighted", Name: "weighted", Enabled: true,
		Strategy: domain.StrategyWeighted,
		Fallback: domain.FallbackPolicy{Enabled: true, MaxAttempts: 2},
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)

	// The same request id must always land on the same provider, which is what
	// makes a retry or a replay observe the same decision.
	rc := testRequest("gpt-4o")
	rc.RequestID = "stable-request-id"
	first, err := engine.Resolve(context.Background(), rc)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	second, err := engine.Resolve(context.Background(), rc)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if first.Chosen.ProviderID != second.Chosen.ProviderID {
		t.Errorf("weighted routing is not deterministic: %q then %q",
			first.Chosen.ProviderID, second.Chosen.ProviderID)
	}

	// Across many request ids both equal-weight providers must receive traffic,
	// so the rotation is a real rotation and not a constant.
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		spread := testRequest("gpt-4o")
		spread.RequestID = domain.RequestID(domain.NewID())
		decision, err := engine.Resolve(context.Background(), spread)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		seen[decision.Chosen.ProviderID] = true
		if len(decision.Chain.Attempts()) != 2 {
			t.Fatalf("weighted chain should retain both targets, got %d", len(decision.Chain.Attempts()))
		}
	}
	if len(seen) != 2 {
		t.Errorf("weighted routing should reach both providers, saw %v", seen)
	}
}

func TestEngineAppliesLimitsAndDeadline(t *testing.T) {
	models := []domain.Model{testModel(openaiID, "openai", "gpt-4o")}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	policy := &domain.RoutingPolicy{
		ID: "limited", Name: "limited", Enabled: true,
		Strategy: domain.StrategyPriority,
		Timeout:  domain.TimeoutPolicy{Total: 30 * time.Second},
		Limits: domain.PolicyLimits{
			MaxOutputTokens: 512,
			LatencyTargetMS: 2500,
		},
	}

	rc := testRequest("gpt-4o")
	rc.MaxOutputTokens = 100_000
	received := rc.ReceivedAt

	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	if _, err := engine.Resolve(context.Background(), rc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if rc.MaxOutputTokens != 512 {
		t.Errorf("the client's maximum must be clamped to the policy ceiling, got %d", rc.MaxOutputTokens)
	}
	if rc.LatencyTargetMS != 2500 {
		t.Errorf("latency target = %d, want 2500", rc.LatencyTargetMS)
	}
	if got := rc.Deadline.Sub(received); got != 30*time.Second {
		t.Errorf("deadline offset = %v, want 30s", got)
	}
}

func TestEngineSkipsProvidersWithoutAdapters(t *testing.T) {
	models := []domain.Model{testModel(openaiID, "openai", "gpt-4o")}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	engine := NewEngine(NewStaticCatalogue(models, providersList), nil, nil,
		WithAvailability(stubAvailability{unavailable: map[string]bool{openaiID: true}}))

	_, err := engine.Resolve(context.Background(), testRequest("gpt-4o"))
	normalized := domain.AsError(err)
	if normalized.Code != domain.ErrCodeUnavailable {
		t.Fatalf("expected unavailable, got %q", normalized.Code)
	}
	if !strings.Contains(normalized.Message, "no working adapter") {
		t.Errorf("message should explain the missing adapter, got %q", normalized.Message)
	}
}

func TestEngineRecordsSkippedTargets(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o"),
		testModel(anthropicID, "anthropic", "gpt-4o"),
	}
	// The Anthropic provider is disabled, so it can serve nothing.
	disabled := testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2)
	disabled.Status = domain.StatusDisabled

	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		disabled,
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), nil, nil)
	decision, err := engine.Resolve(context.Background(), testRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(decision.Chain.Skipped) == 0 {
		t.Fatal("the disabled provider should be reported as skipped")
	}
	if !strings.Contains(decision.Chain.Skipped[0].Reason, "disabled") {
		t.Errorf("skip reason = %q", decision.Chain.Skipped[0].Reason)
	}
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func TestHealthTrackerDegradesThenTripsCircuit(t *testing.T) {
	health := NewHealthTracker(HealthConfig{})

	for i := 0; i < domain.DegradeThreshold; i++ {
		health.RecordFailure(openaiID, "openai", domain.ErrCodeUpstream, 10*time.Millisecond)
	}

	state, known := health.Health(openaiID)
	if !known {
		t.Fatal("health should be known after failures")
	}
	if state.State != domain.HealthDegraded {
		t.Errorf("state after %d failures = %q, want degraded", domain.DegradeThreshold, state.State)
	}
	if health.CircuitOpen(openaiID) {
		t.Error("a merely degraded provider must not be removed from rotation")
	}

	for i := domain.DegradeThreshold; i < domain.DegradeThreshold*3; i++ {
		health.RecordFailure(openaiID, "openai", domain.ErrCodeTimeout, 10*time.Millisecond)
	}

	state, _ = health.Health(openaiID)
	if state.State != domain.HealthUnhealthy {
		t.Errorf("state after %d failures = %q, want unhealthy", domain.DegradeThreshold*3, state.State)
	}
	if !health.CircuitOpen(openaiID) {
		t.Error("the circuit breaker should be open")
	}

	// A real success is the only thing that clears the breaker.
	health.RecordSuccess(openaiID, "openai", 5*time.Millisecond)
	if health.CircuitOpen(openaiID) {
		t.Error("a successful call must clear the circuit breaker")
	}
	state, _ = health.Health(openaiID)
	if state.ConsecutiveFailures != 0 {
		t.Errorf("consecutive failures = %d, want 0", state.ConsecutiveFailures)
	}
}

func TestHealthTrackerProbeDoesNotClearCircuit(t *testing.T) {
	health := NewHealthTracker(HealthConfig{})
	for i := 0; i < domain.DegradeThreshold*3; i++ {
		health.RecordFailure(openaiID, "openai", domain.ErrCodeUpstream, 10*time.Millisecond)
	}

	health.RecordProbe(domain.ProviderHealth{
		ProviderID:     openaiID,
		ProviderName:   "openai",
		State:          domain.HealthHealthy,
		ProbeLatencyMS: 42,
		CheckedAt:      domain.Now(),
	})

	if !health.CircuitOpen(openaiID) {
		t.Error("a synthetic probe must not let observed-broken traffic back in")
	}
	state, _ := health.Health(openaiID)
	if state.State != domain.HealthUnhealthy {
		t.Errorf("state = %q, want unhealthy", state.State)
	}
}

func TestHealthTrackerUnknownProviderIsNotAssessed(t *testing.T) {
	health := NewHealthTracker(HealthConfig{})
	if _, known := health.Health("nobody"); known {
		t.Error("an unobserved provider has no assessment")
	}
	if health.CircuitOpen("nobody") {
		t.Error("an unobserved provider's circuit is closed")
	}
}

func TestHealthTrackerLatencyEstimates(t *testing.T) {
	health := NewHealthTracker(HealthConfig{})
	health.RecordSuccess(openaiID, "openai", 100*time.Millisecond)
	health.RecordSuccess(openaiID, "openai", 300*time.Millisecond)

	estimates := health.LatencyEstimates()
	if got := estimates[openaiID]; got != 200*time.Millisecond {
		t.Errorf("average latency = %v, want 200ms", got)
	}
	if _, ok := estimates[anthropicID]; ok {
		t.Error("a provider with no observations must not appear")
	}
}

func TestHealthTrackerSnapshotIsSorted(t *testing.T) {
	health := NewHealthTracker(HealthConfig{})
	health.RecordSuccess("z", "zeta", time.Millisecond)
	health.RecordSuccess("a", "alpha", time.Millisecond)
	health.RecordSuccess("m", "mu", time.Millisecond)

	snapshot := health.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("snapshot length = %d, want 3", len(snapshot))
	}
	for i := 1; i < len(snapshot); i++ {
		if snapshot[i-1].ProviderName > snapshot[i].ProviderName {
			t.Fatalf("snapshot is not ordered: %v", snapshot)
		}
	}
}

// ---------------------------------------------------------------------------
// Executor
// ---------------------------------------------------------------------------

// resolveWith builds an engine, resolves a request and returns the context with
// its decision attached.
func resolveWith(t *testing.T, engine *Engine, rc *domain.RequestContext) *domain.RequestContext {
	t.Helper()
	if _, err := engine.Resolve(context.Background(), rc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return rc
}

func TestExecutorRetriesWithinProviderThenSucceeds(t *testing.T) {
	models := []domain.Model{testModel(openaiID, "openai", "gpt-4o")}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	adapter := newStubAdapter("openai", domain.ProviderOpenAI)
	adapter.outcome = func(call int) error {
		if call == 1 {
			return domain.NewError(domain.ErrCodeRateLimited, "slow down")
		}
		return nil
	}

	policy := &domain.RoutingPolicy{
		ID: "retry", Name: "retry", Enabled: true,
		Strategy: domain.StrategyPriority,
		Retry:    domain.RetryPolicy{MaxAttempts: 2, InitialBackoff: 100 * time.Millisecond},
	}

	clock := newTestClock()
	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	rc := resolveWith(t, engine, testRequest("gpt-4o"))
	rc.Deadline = clock.now().Add(5 * time.Minute)

	registry := newRegistry(registration{provider: providersList[0], adapter: adapter})
	executor := NewExecutor(registry, NewHealthTracker(HealthConfig{}),
		WithClock(clock.now, clock.sleep), WithJitter(false))

	result, err := executor.Execute(context.Background(), rc, &providers.Request{Model: "gpt-4o"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", result.Attempts)
	}
	if len(result.Trace.Attempts) != 2 {
		t.Fatalf("trace attempts = %d, want 2", len(result.Trace.Attempts))
	}
	if !result.Trace.Attempts[0].RetryTriggered {
		t.Error("the first attempt should be marked as retried")
	}
	if result.Trace.Attempts[0].ErrorCode != domain.ErrCodeRateLimited {
		t.Errorf("first attempt code = %q", result.Trace.Attempts[0].ErrorCode)
	}
	if result.Trace.Outcome != domain.OutcomeSuccess {
		t.Errorf("outcome = %q, want success", result.Trace.Outcome)
	}
	if clock.totalSlept() != 100*time.Millisecond {
		t.Errorf("backoff = %v, want 100ms", clock.totalSlept())
	}
	if result.FallbackUsed {
		t.Error("retrying the same provider is not a fallback")
	}
}

func TestExecutorFailsOverToSecondProvider(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o"),
		testModel(anthropicID, "anthropic", "gpt-4o"),
	}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2),
	}

	primary := newStubAdapter("openai", domain.ProviderOpenAI)
	primary.outcome = func(int) error { return domain.NewError(domain.ErrCodeUpstream, "primary is down") }
	secondary := newStubAdapter("anthropic", domain.ProviderAnthropic)

	// Retry is disabled so this test isolates failover from same-provider retry.
	policy := &domain.RoutingPolicy{
		ID: "failover", Name: "failover", Enabled: true,
		Strategy: domain.StrategyPriority,
		Fallback: domain.FallbackPolicy{Enabled: true, MaxAttempts: 2},
		Retry:    domain.RetryPolicy{MaxAttempts: 1},
	}

	clock := newTestClock()
	engine := NewEngine(NewStaticCatalogue(models, providersList), &stubResolver{policy: policy}, nil)
	rc := resolveWith(t, engine, testRequest("gpt-4o"))
	rc.Deadline = clock.now().Add(5 * time.Minute)

	registry := newRegistry(
		registration{provider: providersList[0], adapter: primary},
		registration{provider: providersList[1], adapter: secondary},
	)
	executor := NewExecutor(registry, NewHealthTracker(HealthConfig{}),
		WithClock(clock.now, clock.sleep), WithJitter(false))

	result, err := executor.Execute(context.Background(), rc, &providers.Request{Model: "gpt-4o"}, nil)
	if err != nil {
		t.Fatalf("the chain should have recovered on the second provider: %v", err)
	}

	if !result.FallbackUsed {
		t.Error("the result should report that fallback was used")
	}
	if result.Trace.Outcome != domain.OutcomeFallback {
		t.Errorf("outcome = %q, want fallback", result.Trace.Outcome)
	}
	if result.Response.ID != "cmpl-anthropic" {
		t.Errorf("response should come from the fallback provider, got %q", result.Response.ID)
	}
	if !result.Trace.Attempts[0].FallbackTriggered {
		t.Error("the failed attempt should be marked as triggering fallback")
	}
	if primary.callCount() != 1 {
		t.Errorf("primary calls = %d, want 1", primary.callCount())
	}
	if secondary.callCount() != 1 {
		t.Errorf("secondary calls = %d, want 1", secondary.callCount())
	}
}

func TestExecutorStopsOnNonFallbackableError(t *testing.T) {
	models := []domain.Model{
		testModel(openaiID, "openai", "gpt-4o"),
		testModel(anthropicID, "anthropic", "gpt-4o"),
	}
	providersList := []domain.Provider{
		testProvider(openaiID, "openai", domain.ProviderOpenAI, 1),
		testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2),
	}

	primary := newStubAdapter("openai", domain.ProviderOpenAI)
	primary.outcome = func(int) error {
		return domain.NewError(domain.ErrCodeInvalidRequest, "messages must not be empty")
	}
	secondary := newStubAdapter("anthropic", domain.ProviderAnthropic)

	engine := NewEngine(NewStaticCatalogue(models, providersList), nil, nil)
	rc := resolveWith(t, engine, testRequest("gpt-4o"))

	registry := newRegistry(
		registration{provider: providersList[0], adapter: primary},
		registration{provider: providersList[1], adapter: secondary},
	)
	executor := NewExecutor(registry, NewHealthTracker(HealthConfig{}))

	result, err := executor.Execute(context.Background(), rc, &providers.Request{Model: "gpt-4o"}, nil)
	normalized := domain.AsError(err)
	if normalized.Code != domain.ErrCodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %q", normalized.Code)
	}
	if result.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", result.Attempts)
	}
	if secondary.callCount() != 0 {
		t.Error("a request-level error must not be sent to another provider")
	}
}

func TestExecutorRespectsExhaustedDeadline(t *testing.T) {
	models := []domain.Model{testModel(openaiID, "openai", "gpt-4o")}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	adapter := newStubAdapter("openai", domain.ProviderOpenAI)

	clock := newTestClock()
	engine := NewEngine(NewStaticCatalogue(models, providersList), nil, nil)
	rc := resolveWith(t, engine, testRequest("gpt-4o"))
	rc.Deadline = clock.now().Add(-time.Second)

	registry := newRegistry(registration{provider: providersList[0], adapter: adapter})
	executor := NewExecutor(registry, NewHealthTracker(HealthConfig{}),
		WithClock(clock.now, clock.sleep), WithJitter(false))

	_, err := executor.Execute(context.Background(), rc, &providers.Request{Model: "gpt-4o"}, nil)
	if code := domain.AsError(err).Code; code != domain.ErrCodeTimeout {
		t.Fatalf("expected timeout, got %q", code)
	}
	if adapter.callCount() != 0 {
		t.Error("the adapter must not be called once the budget is spent")
	}
}

func TestExecutorSkipsFallbackOverCostCeiling(t *testing.T) {
	cheap := domain.RouteTarget{
		ProviderID: openaiID, ProviderName: "openai", Model: "gpt-4o-mini",
	}
	pricey := domain.RouteTarget{
		ProviderID:           anthropicID,
		ProviderName:         "anthropic",
		Model:                "claude-3-5-sonnet",
		InputCostPerMillion:  1000,
		OutputCostPerMillion: 1000,
	}

	adapter := newStubAdapter("openai", domain.ProviderOpenAI)
	adapter.outcome = func(int) error { return domain.NewError(domain.ErrCodeUpstream, "boom") }
	fallbackAdapter := newStubAdapter("anthropic", domain.ProviderAnthropic)

	clock := newTestClock()
	rc := &domain.RequestContext{
		RequestID:       domain.RequestID("ceiling-request"),
		ReceivedAt:      clock.now(),
		RequestType:     domain.RequestTypeChatCompletion,
		RequestedModel:  "gpt-4o",
		PromptTokens:    100,
		MaxOutputTokens: 1000,
		Deadline:        clock.now().Add(5 * time.Minute),
	}
	rc.Resolution = &domain.RouteDecision{
		RequestID: rc.RequestID,
		Strategy:  domain.StrategyPriority,
		Chosen:    cheap,
		Chain:     domain.FallbackChain{Targets: []domain.RouteTarget{cheap, pricey}, MaxAttempts: 2},
		Retry:     domain.RetryPolicy{MaxAttempts: 1},
		Timeout:   domain.TimeoutPolicy{PerAttempt: time.Minute},
		Fallback:  domain.FallbackPolicy{Enabled: true, MaxAttempts: 2, BudgetAware: true},
		// A ceiling far below the second target's projected cost.
		CostCeilingUSD: 0.001,
	}

	registry := newRegistry(
		registration{provider: testProvider(openaiID, "openai", domain.ProviderOpenAI, 1), adapter: adapter},
		registration{provider: testProvider(anthropicID, "anthropic", domain.ProviderAnthropic, 2), adapter: fallbackAdapter},
	)
	executor := NewExecutor(registry, NewHealthTracker(HealthConfig{}),
		WithClock(clock.now, clock.sleep), WithJitter(false))

	result, err := executor.Execute(context.Background(), rc, &providers.Request{Model: "gpt-4o"}, nil)
	if err == nil {
		t.Fatal("both targets should fail so the skip is observable")
	}
	if result.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", result.Attempts)
	}
	if fallbackAdapter.callCount() != 0 {
		t.Error("a fallback over the cost ceiling must never be attempted")
	}

	var skipped bool
	for _, span := range result.Trace.Spans {
		if span.Kind == domain.SpanFallback && strings.HasPrefix(span.Name, "skip:") {
			skipped = true
			if span.Attributes["reason"] != "projected cost exceeds ceiling" {
				t.Errorf("skip reason = %q", span.Attributes["reason"])
			}
		}
	}
	if !skipped {
		t.Errorf("the trace should record why the fallback was skipped, got %+v", result.Trace.Spans)
	}
}

func TestExecutorReportsStreamStartedError(t *testing.T) {
	models := []domain.Model{testModel(openaiID, "openai", "gpt-4o")}
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	adapter := newStubAdapter("openai", domain.ProviderOpenAI)
	adapter.onStream = func(handler providers.StreamHandler, req *providers.Request) error {
		if err := handler(providers.Chunk{ID: "chunk-1", Model: req.Model, Index: 0}); err != nil {
			return err
		}
		return domain.NewError(domain.ErrCodeUpstream, "connection reset mid-stream")
	}

	engine := NewEngine(NewStaticCatalogue(models, providersList), nil, nil)
	rc := resolveWith(t, engine, testRequest("gpt-4o"))

	registry := newRegistry(registration{provider: providersList[0], adapter: adapter})
	executor := NewExecutor(registry, NewHealthTracker(HealthConfig{}))

	var chunks int
	_, err := executor.Execute(context.Background(), rc, &providers.Request{Model: "gpt-4o"},
		func(providers.Chunk) error {
			chunks++
			return nil
		})

	if !IsStreamStarted(err) {
		t.Fatalf("expected a stream-started error, got %v", err)
	}
	if chunks != 1 {
		t.Errorf("chunks delivered = %d, want 1", chunks)
	}
	var started *StreamStartedError
	if !errors.As(err, &started) {
		t.Fatal("errors.As should expose the StreamStartedError")
	}
	if started.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", started.Attempts)
	}
}

func TestExecutorResolvesProviderByName(t *testing.T) {
	adapter := newStubAdapter("openai", domain.ProviderOpenAI)
	providersList := []domain.Provider{testProvider(openaiID, "openai", domain.ProviderOpenAI, 1)}

	target := domain.RouteTarget{ProviderName: "openai", Model: "gpt-4o"}
	clock := newTestClock()
	rc := &domain.RequestContext{
		RequestID:       domain.RequestID("by-name"),
		ReceivedAt:      clock.now(),
		RequestType:     domain.RequestTypeChatCompletion,
		RequestedModel:  "gpt-4o",
		MaxOutputTokens: 128,
		Deadline:        clock.now().Add(time.Minute),
	}
	rc.Resolution = &domain.RouteDecision{
		RequestID: rc.RequestID,
		Strategy:  domain.StrategyPriority,
		Chosen:    target,
		Chain:     domain.FallbackChain{Targets: []domain.RouteTarget{target}, MaxAttempts: 1},
		Retry:     domain.RetryPolicy{MaxAttempts: 1},
		Timeout:   domain.TimeoutPolicy{PerAttempt: time.Minute},
		Fallback:  domain.FallbackPolicy{Enabled: true, MaxAttempts: 1},
	}

	registry := newRegistry(registration{provider: providersList[0], adapter: adapter})
	executor := NewExecutor(registry, NewHealthTracker(HealthConfig{}), WithClock(clock.now, clock.sleep))

	result, err := executor.Execute(context.Background(), rc, &providers.Request{Model: "gpt-4o"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Response.ID != "cmpl-openai" {
		t.Errorf("response = %q", result.Response.ID)
	}
}

func TestExecutorReportsMissingProvider(t *testing.T) {
	target := domain.RouteTarget{ProviderID: "ghost", ProviderName: "ghost", Model: "gpt-4o"}
	clock := newTestClock()
	rc := &domain.RequestContext{
		RequestID:       domain.RequestID("ghost-request"),
		ReceivedAt:      clock.now(),
		RequestType:     domain.RequestTypeChatCompletion,
		RequestedModel:  "gpt-4o",
		MaxOutputTokens: 128,
		Deadline:        clock.now().Add(time.Minute),
	}
	rc.Resolution = &domain.RouteDecision{
		RequestID: rc.RequestID,
		Strategy:  domain.StrategyPriority,
		Chosen:    target,
		Chain:     domain.FallbackChain{Targets: []domain.RouteTarget{target}, MaxAttempts: 1},
		Retry:     domain.RetryPolicy{MaxAttempts: 1},
		Timeout:   domain.TimeoutPolicy{PerAttempt: time.Minute},
		Fallback:  domain.FallbackPolicy{Enabled: true, MaxAttempts: 1},
	}

	executor := NewExecutor(providers.NewRegistry(), NewHealthTracker(HealthConfig{}),
		WithClock(clock.now, clock.sleep), WithJitter(false))

	_, err := executor.Execute(context.Background(), rc, &providers.Request{Model: "gpt-4o"}, nil)
	normalized := domain.AsError(err)
	if normalized.Code != domain.ErrCodeUnavailable {
		t.Fatalf("expected unavailable, got %q", normalized.Code)
	}
	if !strings.Contains(normalized.Message, "is not configured") {
		t.Errorf("message = %q", normalized.Message)
	}
}

func TestExecutorRequiresAResolution(t *testing.T) {
	executor := NewExecutor(providers.NewRegistry(), NewHealthTracker(HealthConfig{}))
	_, err := executor.Execute(context.Background(), &domain.RequestContext{}, &providers.Request{}, nil)

	if code := domain.AsError(err).Code; code != domain.ErrCodeInternal {
		t.Fatalf("expected internal_error, got %q", code)
	}
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestJitterIsDeterministicAndBounded(t *testing.T) {
	const base = 100 * time.Millisecond

	first := jitter(base, "seed-a", 1)
	second := jitter(base, "seed-a", 1)
	if first != second {
		t.Errorf("jitter must be deterministic: %v then %v", first, second)
	}

	if first < 80*time.Millisecond || first >= 120*time.Millisecond {
		t.Errorf("jittered delay %v is outside [0.8,1.2) of the base", first)
	}

	// Different attempts and different requests must not produce the same
	// factor, otherwise the de-synchronization jitter exists for is lost.
	if jitter(base, "seed-a", 2) == first {
		t.Error("jitter should vary between attempts")
	}
	if jitter(base, "seed-b", 1) == first {
		t.Error("jitter should vary between requests")
	}
}

func TestRankCandidatesIsStable(t *testing.T) {
	candidates := []domain.Candidate{
		{Target: domain.RouteTarget{ProviderName: "b", Model: "m", Priority: 2}, Eligible: true},
		{Target: domain.RouteTarget{ProviderName: "a", Model: "m", Priority: 1}, Eligible: true},
	}

	first := rankCandidates(candidates, domain.StrategyPriority, "seed", nil, nil)
	second := rankCandidates(candidates, domain.StrategyPriority, "seed", nil, nil)

	if first[0].Target.ProviderName != "a" {
		t.Errorf("priority 1 should rank first, got %q", first[0].Target.ProviderName)
	}
	for i := range first {
		if first[i].Target.ProviderName != second[i].Target.ProviderName {
			t.Fatalf("ranking is not stable at index %d", i)
		}
	}
	// The input must not be mutated in place.
	if candidates[0].Target.ProviderName != "b" {
		t.Error("rankCandidates must not reorder its argument")
	}
}

func TestRankByWeightTreatsMissingWeightAsOne(t *testing.T) {
	candidates := []domain.Candidate{
		{Target: domain.RouteTarget{ProviderName: "a", Model: "m"}, Eligible: true},
		{Target: domain.RouteTarget{ProviderName: "b", Model: "m"}, Eligible: true},
	}

	ranked := rankByWeight(candidates, "seed")
	if len(ranked) != 2 {
		t.Fatalf("ranked length = %d, want 2", len(ranked))
	}
	// Score records the rotation position, so the ordering is self-describing.
	if ranked[0].Score != 0 || ranked[1].Score != 1 {
		t.Errorf("scores = %v, %v", ranked[0].Score, ranked[1].Score)
	}
}

// TestProviderCapabilityDefaultsMatchAdapters guards the deliberate duplication
// between the routing package and the providers package: routing must not import
// adapter code, but the two capability tables must agree or a provider would be
// routable on one view of the world and unservable on the other.
func TestProviderCapabilityDefaultsMatchAdapters(t *testing.T) {
	kinds := []domain.ProviderKind{
		domain.ProviderOpenAI,
		domain.ProviderAnthropic,
		domain.ProviderOllama,
		domain.ProviderVLLM,
		domain.ProviderOpenAICompatible,
	}

	for _, kind := range kinds {
		want := providers.DefaultCapabilities(kind).Slice()
		got := providerCapabilityDefault(kind).Slice()

		if len(want) != len(got) {
			t.Fatalf("%s: capabilities differ: providers=%v routing=%v", kind, want, got)
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("%s: capabilities differ: providers=%v routing=%v", kind, want, got)
			}
		}
	}
}
