package policy

import (
	"context"
	"testing"

	"github.com/shadowsafin/synapass/internal/domain"
)

func testPolicy() *domain.RoutingPolicy {
	p := &domain.RoutingPolicy{
		ID:       "p1",
		Name:     "test",
		Enabled:  true,
		Strategy: domain.StrategyPriority,
		Targets:  []domain.RouteTarget{{ProviderName: "openai", Model: "gpt-4o-mini"}},
	}
	p.Normalize()
	return p
}

func testCtx() *domain.RequestContext {
	return &domain.RequestContext{
		RequestID:      "req-1",
		RequestedModel: "gpt-4o-mini",
		PromptTokens:   100,
	}
}

func TestAllowDefault(t *testing.T) {
	e := NewEngine()
	dec := e.Evaluate(context.Background(), EvaluateInput{Policy: testPolicy(), Request: testCtx()})
	if !dec.Allowed {
		t.Fatalf("expected allow, got deny %s", dec.DenyReason)
	}
}

func TestDenyModel(t *testing.T) {
	e := NewEngine()
	p := testPolicy()
	p.Limits.DeniedModels = []string{"gpt-4o-mini"}
	rc := testCtx()
	dec := e.Evaluate(context.Background(), EvaluateInput{Policy: p, Request: rc})
	if dec.Allowed {
		t.Fatalf("expected deny for denied model")
	}
	if dec.DenyReason != "model_denied" {
		t.Fatalf("unexpected reason %s", dec.DenyReason)
	}
}

func TestDenySize(t *testing.T) {
	e := NewEngine()
	p := testPolicy()
	p.Limits.MaxPromptTokens = 10
	rc := testCtx()
	rc.PromptTokens = 100
	dec := e.Evaluate(context.Background(), EvaluateInput{Policy: p, Request: rc})
	if dec.Allowed {
		t.Fatalf("expected deny for oversized prompt")
	}
}

func TestSensitiveBypassCache(t *testing.T) {
	e := NewEngine()
	p := testPolicy()
	v := true
	_ = v
	p.Limits.RequireCacheBypassSensitive = true
	rc := testCtx()
	rc.DataSensitivity = []string{"pii"}
	dec := e.Evaluate(context.Background(), EvaluateInput{Policy: p, Request: rc})
	if !dec.Allowed {
		t.Fatalf("sensitive should not deny, only bypass cache")
	}
	if dec.UseCache {
		t.Fatalf("expected cache bypass for sensitive")
	}
}

func TestBatchOnly(t *testing.T) {
	e := NewEngine()
	p := testPolicy()
	p.Limits.BatchOnly = true
	rc := testCtx()
	rc.Batch = false
	dec := e.Evaluate(context.Background(), EvaluateInput{Policy: p, Request: rc})
	if dec.Allowed {
		t.Fatalf("expected deny for interactive on batch-only policy")
	}
}
